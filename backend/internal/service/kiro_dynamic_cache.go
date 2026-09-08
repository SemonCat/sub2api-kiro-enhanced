package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/anthropictokenizer"
	kiropkg "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
)

const (
	kiroDynamicMaxScopes        = 1024
	kiroDynamicMaxAccountScopes = 32
	kiroDynamicMaxPrefixes      = 256
	kiroDynamicMaxSamples       = 64
	kiroDynamicCalibrationTTL   = 30 * time.Minute

	// kiroGPTCachedMarginalCreditRatio and kiroClaudeCachedMarginalCreditRatio
	// (d) are each family's fraction of the full marginal credit cost that a
	// cache-hit ("warm") token still bills at. A call's observed native-credit
	// *savings* s (see kiroDynamicPair.creditSavings) systematically
	// understates the true structural token-reuse fraction r for a calibrated
	// family: only the reused portion bills at d instead of 1, so
	// s = r*(1-d), giving r = s/(1-d) (see kiroInferredReuse). Treating s
	// itself as r -- the pre-calibration behavior, still used for any
	// uncalibrated family -- pins reuse estimates to s's own range even when
	// the underlying prefix was almost fully reused.
	//
	// kiroGPTCachedMarginalCreditRatio: measured via an authorized
	// side-by-side comparison of kiro-cli direct traffic against the
	// official Codex CLI ("Luna") at matched native input scales of
	// ~21k/40k/62k tokens: Kiro's own observed warm/cold marginal-credit
	// ratio was 52.3%, while the official client's own reported cached-input
	// rate for the same requests was ~95-98% (see
	// kiroGPTRealTrafficPairs / TestKiroDynamicGPTRealTrafficCalibration).
	// GPT/Luna-scoped evidence only.
	kiroGPTCachedMarginalCreditRatio = 0.523

	// kiroClaudeCachedMarginalCreditRatio: measured independently via an
	// authorized kiro-cli direct session (classic session store, model
	// explicitly pinned, no tools, low reasoning effort) against Claude Opus
	// 5, replaying one cold plus three warm turns at three input scales
	// (~200/900/1800 evidence lines): a multi-size regression of warm vs.
	// cold credits after removing the fixed per-call CLI cost gave a
	// warm/cold marginal-credit slope ratio of ~0.5247. An independent
	// Claude Sonnet 5 holdout (two input scales, same methodology) gave a
	// slope ratio of ~0.5266, confirming this is a family-level constant
	// rather than an Opus-specific artifact. Separately, the official Claude
	// CLI's own reported cache metering on the 900-line shape showed
	// cache_read_input_tokens covering the entire warmed prefix on the first
	// successful warm turn (~99.9% cached-input rate), confirming near-total
	// structural reuse -- consistent with creditSavings systematically
	// understating it in the same way as the GPT/Luna evidence above (see
	// kiroClaudeRealTrafficPairs / TestKiroDynamicClaudeRealTrafficCalibration).
	kiroClaudeCachedMarginalCreditRatio = 0.525
)

// kiroModelCreditCalibration looks up the calibrated cached marginal credit
// ratio d for model's Kiro family (see kiroGPTCachedMarginalCreditRatio and
// kiroClaudeCachedMarginalCreditRatio's doc comments for the evidence behind
// each). ok is false for any family without independent empirical
// calibration -- the dynamic estimator then leaves creditSavings unconverted
// for that family (see kiroDynamicTracker.prepare), exactly as it did before
// any family was calibrated. This is the single source of truth for which
// families are calibrated and at what ratio; kiroDynamicTTL below encodes
// the same family prefixes for its own (unrelated) purpose of choosing a
// scope TTL.
func kiroModelCreditCalibration(model string) (d float64, ok bool) {
	switch {
	case strings.HasPrefix(model, "gpt-"):
		return kiroGPTCachedMarginalCreditRatio, true
	case strings.HasPrefix(model, "claude-"):
		return kiroClaudeCachedMarginalCreditRatio, true
	default:
		return 0, false
	}
}

// kiroInferredReuse converts an observed native-credit savings fraction s
// (kiroDynamicPair.creditSavings) into an inferred structural token-reuse
// fraction r for a calibrated family, using that family's cached marginal
// credit ratio d (see kiroModelCreditCalibration): r = s/(1-d), clamped to
// [0,1] since a token is either structurally reused or it is not.
func kiroInferredReuse(creditSavings, d float64) float64 {
	reuse := creditSavings / (1 - d)
	return math.Min(1, math.Max(0, reuse))
}

type kiroDynamicSampleKey struct {
	shape  [32]byte // inference configuration discriminator
	prefix [32]byte
}
type kiroDynamicSample struct {
	// coldDensity is native credits per native total token (InputTokens +
	// OutputTokens), observed on a call whose matched prefix was zero (no
	// reuse at all). Real agent traffic keeps output length unconstrained per
	// turn (single digits to 1000+ tokens observed for the same warmed
	// prefix), so raw credits are not comparable across cold/warm calls with
	// different output lengths. Dividing by each call's own total token count
	// cancels the output-length term to first order without asserting any
	// input/output price split. This is the minimum density observed for the
	// (shape, prefix) pair: since ratio = 1 - warm/cold is monotonically
	// decreasing in cold, the smallest cold value yields the smallest
	// (most conservative) ratio, so `min` -- not mean -- keeps the estimator
	// from ever overclaiming savings.
	coldDensity float64
	expires     time.Time
}
type kiroDynamicPair struct {
	// creditSavings is the observed native-credit savings fraction s =
	// (coldDensity-warmDensity)/coldDensity for one (shape, prefix)
	// cold/warm observation pair, floored at 0 and capped at .9 (see
	// kiroDynamicTracker.complete). This is NOT the structural token-reuse
	// fraction: for a calibrated family (see kiroModelCreditCalibration) it
	// must first pass through kiroInferredReuse before it is applied to a
	// matched token count. An uncalibrated family uses it directly as the
	// reuse fraction, unchanged from the pre-calibration estimator.
	creditSavings float64
	expires       time.Time
}

type kiroDynamicScope struct {
	account  int64
	expires  time.Time
	prefixes map[[32]byte]time.Time
	samples  map[kiroDynamicSampleKey]kiroDynamicSample
	pairs    []kiroDynamicPair
	active   int
	epoch    uint64
}
type kiroDynamicTracker struct {
	mu     sync.Mutex
	now    func() time.Time
	scopes map[[32]byte]*kiroDynamicScope
}

func newKiroDynamicTracker(now func() time.Time) *kiroDynamicTracker {
	return &kiroDynamicTracker{now: now, scopes: make(map[[32]byte]*kiroDynamicScope)}
}

var globalKiroDynamicTracker = newKiroDynamicTracker(time.Now)

type kiroCacheEmulationPlan struct {
	usage     *kiroCacheEmulationUsage
	tracker   *kiroDynamicTracker
	key       [32]byte
	scope     *kiroDynamicScope
	profile   *kiroCacheProfile
	shape     [32]byte
	ttl       time.Duration
	started   time.Time
	matched   int
	epoch     uint64
	exclusive bool
	once      sync.Once
}

func (p *kiroCacheEmulationPlan) result() *kiroCacheEmulationUsage {
	if p == nil {
		return nil
	}
	return p.usage
}
func kiroDynamicTTL(model string) time.Duration {
	// Route by the native outbound model ID; keep the exact model in scope.
	switch {
	case strings.HasPrefix(model, "claude-"):
		return 5 * time.Minute
	case strings.HasPrefix(model, "gpt-"):
		return 30 * time.Minute
	default:
		return 0
	}
}

// This profile hashes the native translated semantics, in order. Current and
// historical user messages use the same wrapper so advancing a turn preserves
// the prefix ladder. Tools are a prelude, not sorted or rewritten. No inbound
// metadata, raw byte prefixes, token credentials or generated conversation IDs
// (except GPT's actual session scope) participate.
func buildKiroOutboundCacheProfile(payload []byte, input int) (*kiroCacheProfile, kiropkg.KiroPayload, bool) {
	var native kiropkg.KiroPayload
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if decoder.Decode(&native) != nil {
		return nil, native, false
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, native, false
	}
	model := native.ConversationState.CurrentMessage.UserInputMessage.ModelID
	ttl := kiroDynamicTTL(model)
	if ttl == 0 || input <= 0 {
		return nil, native, false
	}
	var blocks []kiroPendingBlock
	add := func(v any) {
		tokenValue := v
		imageTokens := 0
		if history, ok := v.(kiropkg.KiroHistoryMessage); ok && history.UserInputMessage != nil && len(history.UserInputMessage.Images) > 0 {
			user := *history.UserInputMessage
			for _, image := range user.Images {
				imageTokens += kiropkg.EstimateImageTokens(context.Background(), "image/"+image.Format, image.Source.Bytes)
			}
			user.Images = nil
			history.UserInputMessage = &user
			tokenValue = history
		}
		raw, err := canonicalJSON(tokenValue)
		if err != nil {
			return
		}
		blocks = append(blocks, kiroPendingBlock{value: v, tokens: anthropictokenizer.CountTokens(string(raw)) + imageTokens, breakpointTTL: &ttl})
	}
	current := native.ConversationState.CurrentMessage.UserInputMessage
	if current.UserInputMessageContext != nil {
		for _, tool := range current.UserInputMessageContext.Tools {
			add(map[string]any{"tool": tool})
		}
	}
	// All model-visible native message fields are retained. Tools in the current
	// context are already represented in the ordered prelude.
	for _, history := range native.ConversationState.History {
		add(history)
	}
	if current.UserInputMessageContext != nil {
		c := *current.UserInputMessageContext
		c.Tools = nil
		current.UserInputMessageContext = &c
		if len(c.ToolResults) == 0 {
			current.UserInputMessageContext = nil
		}
	}
	add(kiropkg.KiroHistoryMessage{UserInputMessage: &current})
	prelude := map[string]any{"model": model, "fields": native.AdditionalModelRequestFields,
		"task": native.ConversationState.AgentTaskType, "trigger": native.ConversationState.ChatTriggerType,
		"continuation": native.ConversationState.AgentContinuationID}
	profile, ok := buildKiroCacheProfileFromBlocks(model, input, prelude, blocks)
	if ok {
		profile.scaleBreakpointsToInputTokens = true
	}
	return profile, native, ok
}

func prepareKiroDynamicCache(account *Account, group *Group, request kiropkg.KiroRequestContext, input int) *kiroCacheEmulationPlan {
	// Ratio/mode fields remain accepted by config/API/UI for compatibility, but
	// are deprecated and deliberately ignored by this estimator.
	if account == nil || account.ID <= 0 || group == nil || !group.EffectiveKiroCacheEmulationEnabled() || request.CacheEndpoint == "" {
		return nil
	}
	profile, native, ok := buildKiroOutboundCacheProfile(request.CachePayload, input)
	if !ok {
		return nil
	}
	model := native.ConversationState.CurrentMessage.UserInputMessage.ModelID
	session := ""
	if strings.HasPrefix(model, "gpt-") {
		session = native.ConversationState.ConversationID
		if session == "" {
			return nil
		}
	}
	// Account identity is stable across OAuth access/refresh rotation. A stable
	// provider principal, when supplied, further isolates account replacement.
	var principalHash [32]byte
	if native.ProfileArn == "" && account.Type == AccountTypeAPIKey {
		principalHash = request.CachePrincipalHash
		if principalHash == ([32]byte{}) {
			// Matches GatewayService.GetAccessToken's credential selection. Runtime
			// supplies the hash of the actual outbound key, including retries.
			principal := account.GetCredential("api_key")
			if principal == "" {
				return nil
			}
			principalHash = sha256.Sum256([]byte(principal))
		}
	}
	scopeJSON, _ := canonicalJSON([]any{account.ID, principalHash, account.GetCredential("user_id"), account.GetCredential("principal_id"), native.ProfileArn, request.CacheEndpoint, model, session})
	shapeJSON, _ := canonicalJSON(native.InferenceConfig)
	calibrationD, calibrated := kiroModelCreditCalibration(model)
	return globalKiroDynamicTracker.prepare(account.ID, sha256.Sum256(scopeJSON), sha256.Sum256(shapeJSON), profile, kiroDynamicTTL(model), group.EffectiveKiroCacheReadEmulationRatio(), calibrationD, calibrated)
}

// scaleKiroCacheTokens applies the pre-estimator static ratio to a proven
// matched-token count, with the exact bounds/rounding of the original static
// cache-read estimator: a non-positive ratio or token count reports nothing,
// a ratio >=1 passes the full matched count through unrounded, and anything
// in between rounds to the nearest token.
func scaleKiroCacheTokens(tokens int, ratio float64) int {
	if tokens <= 0 || ratio <= 0 {
		return 0
	}
	if ratio >= 1 {
		return tokens
	}
	return int(math.Round(float64(tokens) * ratio))
}

func (t *kiroDynamicTracker) prepare(account int64, key, shape [32]byte, profile *kiroCacheProfile, ttl time.Duration, staticReadRatio, calibrationD float64, calibrated bool) *kiroCacheEmulationPlan {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prune(now)
	scope := t.scopes[key]
	if scope == nil {
		count := 0
		for _, s := range t.scopes {
			if s.account == account {
				count++
			}
		}
		// Reject new scopes when full; one account cannot evict another's warmth.
		if len(t.scopes) >= kiroDynamicMaxScopes || count >= kiroDynamicMaxAccountScopes {
			return nil
		}
		scope = &kiroDynamicScope{account: account, expires: now.Add(kiroDynamicCalibrationTTL), prefixes: make(map[[32]byte]time.Time), samples: make(map[kiroDynamicSampleKey]kiroDynamicSample)}
		t.scopes[key] = scope
	}
	matched := 0
	for _, bp := range profile.cacheableBreakpoints() {
		if scope.prefixes[profile.blocks[bp.blockIndex].prefixFingerprint].After(now) {
			matched = max(matched, profile.cacheTokensForBreakpoint(bp.cumulativeTokens))
		}
	}
	scope.epoch++
	plan := &kiroCacheEmulationPlan{tracker: t, key: key, scope: scope, profile: profile, shape: shape, ttl: ttl, started: now, matched: matched, epoch: scope.epoch, exclusive: scope.active == 0}
	scope.active++
	// matched > 0 is the only gate below: canonical prefix matching has
	// already proven a genuine repeated prefix against this scope's live
	// (non-expired) warmth. matched == 0 covers every other "not yet
	// trustworthy" case by construction -- an unparseable/ineligible request
	// or a disabled group never reaches here (prepareKiroDynamicCache returns
	// nil first), and an account/model/profile/endpoint/session scope change
	// or an expired prefix TTL simply finds no live prefix to match, leaving
	// matched at 0. A failed/aborted response never reaches this scope's
	// prefixes either, since complete() only records them on success. None of
	// those cases fall back to the static estimator below.
	if matched > 0 {
		if len(scope.pairs) >= 2 {
			// Lower envelope of the last eight matched pairs' credit savings.
			// Downward evidence applies immediately; old outliers age out so
			// calibration can recover. This is the authoritative dynamic
			// estimate: once it exists it takes precedence over the static
			// fallback below even when it evaluates to a legitimate zero (a
			// real, calibrated "no reuse" verdict is not the same as "no
			// dynamic ratio available").
			savings := .9
			for _, pair := range scope.pairs {
				savings = math.Min(savings, pair.creditSavings)
			}
			// creditSavings is the native-credit savings fraction s, not the
			// structural token-reuse fraction r. For a calibrated family,
			// s systematically understates r because cache-hit tokens still
			// bill at that family's cached marginal credit ratio d instead of
			// 0 -- convert before applying it to the matched (canonical
			// prefix) bound. An uncalibrated family has no independent
			// empirical d and keeps using s directly as r, unchanged from
			// before any family was calibrated.
			reuse := savings
			if calibrated {
				reuse = kiroInferredReuse(savings, calibrationD)
			}
			read := int(float64(matched) * reuse)
			read = min(max(read, 0), profile.totalInputTokens)
			if read > 0 {
				plan.usage = &kiroCacheEmulationUsage{InputTokens: profile.totalInputTokens - read, CacheReadInputTokens: read}
			}
		} else {
			// Static pre-estimator fallback: native-credit calibration has
			// not yet produced two valid pairs for this scope, but canonical
			// prefix matching has already proven a real repeated prefix.
			// Reporting a deliberately less-precise static estimate here --
			// using the group's existing EffectiveKiroCacheReadEmulationRatio
			// and the pre-regression static estimator's own bounds/rounding
			// (scaleKiroCacheTokens) -- is preferable to indefinitely
			// reporting zero for genuinely warmed traffic. The moment dynamic
			// calibration produces >=2 pairs, the branch above takes over
			// unconditionally (including when it computes a valid ratio of
			// 0) and this fallback no longer runs for this scope.
			read := scaleKiroCacheTokens(matched, staticReadRatio)
			read = min(max(read, 0), profile.totalInputTokens)
			if read > 0 {
				plan.usage = &kiroCacheEmulationUsage{InputTokens: profile.totalInputTokens - read, CacheReadInputTokens: read}
			}
		}
	}
	return plan
}

// complete is idempotent. The caller invokes it only after native EOF, parser
// success and successful writes. Failed/aborted requests never renew warmth or
// calibrate. Frozen plan.usage is never mutated, including by late metering.
func (p *kiroCacheEmulationPlan) complete(usage kiropkg.Usage, success bool) {
	if p == nil {
		return
	}
	p.once.Do(func() {
		t := p.tracker
		now := t.now()
		t.mu.Lock()
		defer t.mu.Unlock()
		s := p.scope
		s.active--
		if !success || !usage.CacheObservationComplete() || usage.KiroCredits <= 0 || math.IsNaN(usage.KiroCredits) || math.IsInf(usage.KiroCredits, 0) || usage.OutputTokens < 0 {
			return
		}
		if t.scopes[p.key] != s {
			return
		}
		// TTL is anchored at observation start, conservatively excluding long streams.
		expires := p.started.Add(p.ttl)
		if !expires.After(now) {
			return
		}
		if p.exclusive && p.epoch == s.epoch {
			// Real agent traffic is append-only with unconstrained output
			// length per turn, so cold and warm calls essentially never share
			// an output token count (see kiroDynamicSample.coldDensity for
			// why raw credits are not comparable across them). Normalizing by
			// each call's own native total token count removes that
			// dependency, so pairing keys on (shape, prefix) only -- output
			// no longer gates whether an observation can calibrate.
			total := usage.TotalTokens
			if total <= 0 {
				total = usage.InputTokens + usage.OutputTokens
			}
			if total > 0 {
				density := usage.KiroCredits / float64(total)
				k := kiroDynamicSampleKey{shape: p.shape, prefix: p.profile.blocks[len(p.profile.blocks)-1].prefixFingerprint}
				sample, exists := s.samples[k]
				if exists && !sample.expires.After(now) {
					delete(s.samples, k)
					exists = false
				}
				if p.matched == 0 {
					if !exists || density < sample.coldDensity {
						sample = kiroDynamicSample{coldDensity: density, expires: now.Add(kiroDynamicCalibrationTTL)}
					}
					if exists || len(s.samples) < kiroDynamicMaxSamples {
						s.samples[k] = sample
					}
				} else {
					// Pair only a canonical cold terminal prefix that occurs on
					// this request's warm ladder and shares the inference
					// configuration. Appended uncached input and this call's
					// own output both inflate the observed density above the
					// density a pure cache hit would show, so the derived
					// ratio UNDERestimates savings; no input/output price
					// slope is assumed or fitted.
					breakpoints := p.profile.cacheableBreakpoints()
					for i := len(breakpoints) - 1; i >= 0; i-- {
						bp := breakpoints[i]
						if p.profile.cacheTokensForBreakpoint(bp.cumulativeTokens) > p.matched {
							continue
						}
						k.prefix = p.profile.blocks[bp.blockIndex].prefixFingerprint
						cold, found := s.samples[k]
						if !found || !cold.expires.After(now) || cold.coldDensity <= 0 {
							continue
						}
						savings := math.Min(.9, math.Max(0, (cold.coldDensity-density)/cold.coldDensity))
						s.pairs = append(s.pairs, kiroDynamicPair{creditSavings: savings, expires: now.Add(kiroDynamicCalibrationTTL)})
						if len(s.pairs) > 8 {
							s.pairs = s.pairs[len(s.pairs)-8:]
						}
						break
					}
				}
			}
		}
		s.expires = now.Add(kiroDynamicCalibrationTTL)
		for _, bp := range p.profile.cacheableBreakpoints() {
			fp := p.profile.blocks[bp.blockIndex].prefixFingerprint
			if old, exists := s.prefixes[fp]; exists {
				if expires.After(old) {
					s.prefixes[fp] = expires
				}
			} else if len(s.prefixes) < kiroDynamicMaxPrefixes {
				s.prefixes[fp] = expires
			}
		}
	})
}

func (t *kiroDynamicTracker) prune(now time.Time) {
	for key, s := range t.scopes {
		if !s.expires.After(now) && s.active == 0 {
			delete(t.scopes, key)
			continue
		}
		for fp, expires := range s.prefixes {
			if !expires.After(now) {
				delete(s.prefixes, fp)
			}
		}
		for k, sample := range s.samples {
			if !sample.expires.After(now) {
				delete(s.samples, k)
			}
		}
		pairs := s.pairs[:0]
		for _, pair := range s.pairs {
			if pair.expires.After(now) {
				pairs = append(pairs, pair)
			}
		}
		s.pairs = pairs
	}
}
