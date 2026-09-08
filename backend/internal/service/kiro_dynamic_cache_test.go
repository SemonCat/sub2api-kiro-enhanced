package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image/color"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	kiropkg "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/stretchr/testify/require"
)

func dynamicFrame(t *testing.T, event string, payload any) []byte {
	t.Helper()
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	var headers bytes.Buffer
	require.NoError(t, headers.WriteByte(byte(len(":event-type"))))
	_, err = headers.WriteString(":event-type")
	require.NoError(t, err)
	require.NoError(t, headers.WriteByte(7))
	require.NoError(t, binary.Write(&headers, binary.BigEndian, uint16(len(event))))
	_, err = headers.WriteString(event)
	require.NoError(t, err)
	var frame bytes.Buffer
	require.NoError(t, binary.Write(&frame, binary.BigEndian, uint32(16+headers.Len()+len(b))))
	require.NoError(t, binary.Write(&frame, binary.BigEndian, uint32(headers.Len())))
	require.NoError(t, binary.Write(&frame, binary.BigEndian, crc32.ChecksumIEEE(frame.Bytes())))
	_, err = frame.Write(headers.Bytes())
	require.NoError(t, err)
	_, err = frame.Write(b)
	require.NoError(t, err)
	require.NoError(t, binary.Write(&frame, binary.BigEndian, crc32.ChecksumIEEE(frame.Bytes())))
	return frame.Bytes()
}
func dynamicEvents(t *testing.T, credits float64, output int) []byte {
	t.Helper()
	b := dynamicFrame(t, "assistantResponseEvent", map[string]any{"content": "hello"})
	b = append(b, dynamicFrame(t, "metadataEvent", map[string]any{"outputTokens": output})...)
	return append(b, dynamicFrame(t, "meteringEvent", map[string]any{"usage": credits, "unit": "credit"})...)
}
func dynamicUsage(t *testing.T, credits float64, output int) kiropkg.Usage {
	t.Helper()
	result, err := kiropkg.ParseNonStreamingEventStreamWithContext(bytes.NewReader(dynamicEvents(t, credits, output)), "claude-opus-5", kiropkg.KiroRequestContext{})
	require.NoError(t, err)
	return result.Usage
}

// dynamicUsageWithInput mirrors dynamicUsage but also reports a native input
// token count, matching production traffic where the native input token
// count dominates the total (real sessions observed 10k-146k input tokens
// against 6-1647 output tokens). dynamicUsage alone leaves InputTokens at 0,
// which is only representative of a degenerate/synthetic case; calibration
// math that depends on the input/output mix must be exercised with this
// helper instead.
func dynamicUsageWithInput(t *testing.T, credits float64, input, output int) kiropkg.Usage {
	t.Helper()
	usage := dynamicUsage(t, credits, output)
	usage.InputTokens = input
	usage.TotalTokens = input + usage.OutputTokens
	return usage
}

// dynamicUsageEmptyOutput reports a metering-complete call with no generated
// text and an explicit zero output token count, so the native fallback that
// otherwise re-estimates output tokens from response text (see
// translator.go's `usage.OutputTokens == 0` backfill) has nothing to recover
// either. Combined with a zero (default) native input token count, this is
// the only way to reach a genuine zero-total-tokens observation through the
// real parser.
func dynamicUsageEmptyOutput(t *testing.T, credits float64) kiropkg.Usage {
	t.Helper()
	b := dynamicFrame(t, "assistantResponseEvent", map[string]any{"content": ""})
	b = append(b, dynamicFrame(t, "metadataEvent", map[string]any{"outputTokens": 0})...)
	b = append(b, dynamicFrame(t, "meteringEvent", map[string]any{"usage": credits, "unit": "credit"})...)
	result, err := kiropkg.ParseNonStreamingEventStreamWithContext(bytes.NewReader(b), "claude-opus-5", kiropkg.KiroRequestContext{})
	require.NoError(t, err)
	return result.Usage
}
func dynamicRequest(t *testing.T, model, conversation string) kiropkg.KiroRequestContext {
	t.Helper()
	payload := kiropkg.KiroPayload{ProfileArn: "test-profile-a", ConversationState: kiropkg.KiroConversationState{
		ConversationID: conversation, ChatTriggerType: "MANUAL",
		History:        []kiropkg.KiroHistoryMessage{{UserInputMessage: &kiropkg.KiroUserInputMessage{ModelID: model, Origin: "AI_EDITOR", Content: strings.Repeat("stable semantic history ", 2500)}}},
		CurrentMessage: kiropkg.KiroCurrentMessage{UserInputMessage: kiropkg.KiroUserInputMessage{ModelID: model, Origin: "AI_EDITOR", Content: "next question"}},
	}}
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	return kiropkg.KiroRequestContext{CachePayload: b, CacheEndpoint: "https://native.test/generate"}
}
func mutateDynamicRequest(t *testing.T, request kiropkg.KiroRequestContext, change func(*kiropkg.KiroPayload)) kiropkg.KiroRequestContext {
	t.Helper()
	var p kiropkg.KiroPayload
	require.NoError(t, json.Unmarshal(request.CachePayload, &p))
	change(&p)
	b, err := json.Marshal(p)
	require.NoError(t, err)
	request.CachePayload = b
	return request
}
func dynamicSetup(t *testing.T) (*Account, *Group, *time.Time) {
	t.Helper()
	old := globalKiroDynamicTracker
	now := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	globalKiroDynamicTracker = newKiroDynamicTracker(func() time.Time { return now })
	t.Cleanup(func() { globalKiroDynamicTracker = old })
	return &Account{ID: 1, Platform: PlatformKiro, Type: AccountTypeOAuth}, kiroCacheGroup(1), &now
}
func dynamicPrepare(t *testing.T, a *Account, g *Group, r kiropkg.KiroRequestContext) *kiroCacheEmulationPlan {
	t.Helper()
	p := prepareKiroDynamicCache(a, g, r, 10000)
	require.NotNil(t, p)
	return p
}

// dynamicTrain bootstraps a scope to >=2 dynamic pairs (the first call
// establishes the cold sample with matched==0; the second and third each
// have matched>0 against the freshly-warmed prefix and contribute one pair
// on completion). It intentionally does not assert on the intermediate
// prepared plans' result(): once matched>0, the static pre-calibration
// fallback (see kiroCacheEmulationPlan / kiroDynamicTracker.prepare) can
// report a nonzero estimate for the second and third calls even before the
// two-pair dynamic gate opens -- that transition is covered explicitly by
// TestKiroDynamicStaticFallbackBeforeCalibration and
// TestKiroDynamicStaticFallbackTransitionsToDynamic.
func dynamicTrain(t *testing.T, a *Account, g *Group, r kiropkg.KiroRequestContext) {
	t.Helper()
	for _, credits := range []float64{10, 2, 2} {
		p := dynamicPrepare(t, a, g, r)
		p.complete(dynamicUsage(t, credits, 20), true)
	}
}
func TestKiroDynamicCalibrationAndFrozenUsage(t *testing.T) {
	a, g, _ := dynamicSetup(t)
	r := dynamicRequest(t, "claude-opus-5", "a")
	dynamicTrain(t, a, g, r)
	p := dynamicPrepare(t, a, g, r)
	require.Equal(t, 8000, p.result().CacheReadInputTokens)
	require.Equal(t, 2000, p.result().InputTokens)
	require.Zero(t, p.result().CacheCreationInputTokens)
	frozen := *p.result()
	p.complete(dynamicUsage(t, 8, 20), true)
	require.Equal(t, frozen, *p.result())
	p.complete(dynamicUsage(t, 1, 20), true) // idempotent late observer
	next := dynamicPrepare(t, a, g, r)
	require.InDelta(t, 2000, next.result().CacheReadInputTokens, 1)
	next.complete(kiropkg.Usage{}, false)
	// A bounded window can recover from low/negative observations.
	p = dynamicPrepare(t, a, g, r)
	p.complete(dynamicUsage(t, 20, 20), true)
	p = dynamicPrepare(t, a, g, r)
	require.Nil(t, p.result())
	p.complete(kiropkg.Usage{}, false)
	for i := 0; i < 8; i++ {
		p = dynamicPrepare(t, a, g, r)
		p.complete(dynamicUsage(t, 2, 20), true)
	}
	p = dynamicPrepare(t, a, g, r)
	require.Equal(t, 8000, p.result().CacheReadInputTokens)
	p.complete(kiropkg.Usage{}, false)
	require.Len(t, p.scope.pairs, 8)
	require.Zero(t, p.scope.active)
}
func TestKiroDynamicModelSessionAndSlidingTTL(t *testing.T) {
	for _, tc := range []struct {
		model           string
		ttl             time.Duration
		sessionRequired bool
	}{
		{"claude-opus-5", 5 * time.Minute, false},
		{"claude-opus-4.8", 5 * time.Minute, false},
		{"claude-opus-4.6", 5 * time.Minute, false},
		{"claude-opus-4-8-thinking", 5 * time.Minute, false},
		{"claude-opus-4-6-20260101-thinking", 5 * time.Minute, false},
		{"claude-opus-5-thinking", 5 * time.Minute, false},
		{"claude-sonnet-4-6", 5 * time.Minute, false},
		{"claude-haiku-4-5-20251001", 5 * time.Minute, false},
		{"claude-future-99", 5 * time.Minute, false},
		{"gpt-5.6-terra", 30 * time.Minute, true},
		{"gpt-5.6-sol", 30 * time.Minute, true},
		{"gpt-5.6-luna", 30 * time.Minute, true},
		{"gpt-5", 30 * time.Minute, true},
		{"gpt-future-99-20260906", 30 * time.Minute, true},
	} {
		model := tc.model
		t.Run(model, func(t *testing.T) {
			a, g, now := dynamicSetup(t)
			r := dynamicRequest(t, model, "a")
			dynamicTrain(t, a, g, r)
			trained := dynamicPrepare(t, a, g, r)
			require.NotNil(t, trained.result())
			if tc.sessionRequired {
				// GPT-family: dynamicTrain's synthetic credit savings (.8)
				// converts to an inferred reuse of .8/(1-.523) ~= 1.68,
				// clamped to 1 -- i.e. full structural reuse of the matched
				// prefix, not the raw .8 savings figure Claude-family
				// traffic would report for the same pairs.
				require.Equal(t, 10000, trained.result().CacheReadInputTokens)
				require.Zero(t, trained.result().InputTokens)
			} else {
				require.Equal(t, 8000, trained.result().CacheReadInputTokens)
				require.Equal(t, 2000, trained.result().InputTokens)
			}
			require.Zero(t, trained.result().CacheCreationInputTokens)
			require.Equal(t, tc.ttl, trained.ttl)
			trained.complete(kiropkg.Usage{}, false)
			missing := prepareKiroDynamicCache(a, g, dynamicRequest(t, model, ""), 10000)
			if tc.sessionRequired {
				require.Nil(t, missing)
			} else {
				require.NotNil(t, missing)
				require.Equal(t, trained.key, missing.key)
				require.NotNil(t, missing.result())
				missing.complete(kiropkg.Usage{}, false)
			}
			other := mutateDynamicRequest(t, r, func(p *kiropkg.KiroPayload) { p.ConversationState.ConversationID = "b" })
			p := dynamicPrepare(t, a, g, other)
			if !tc.sessionRequired {
				require.Equal(t, trained.key, p.key)
				require.Equal(t, 10000, p.matched)
				require.NotNil(t, p.result())
			} else {
				require.NotEqual(t, trained.key, p.key)
				require.Zero(t, p.matched)
				require.Nil(t, p.result())
			}
			p.complete(kiropkg.Usage{}, false)
			ttl := tc.ttl
			require.Equal(t, ttl, kiroDynamicTTL(model))
			*now = now.Add(ttl - time.Second)
			p = dynamicPrepare(t, a, g, r)
			require.Equal(t, 10000, p.matched)
			p.complete(dynamicUsage(t, 2, 20), true)
			*now = now.Add(ttl - time.Second)
			p = dynamicPrepare(t, a, g, r)
			require.Equal(t, 10000, p.matched)
			p.complete(kiropkg.Usage{}, false)
			// A prepare/abort does not slide TTL.
			*now = now.Add(time.Second)
			p = dynamicPrepare(t, a, g, r)
			require.Zero(t, p.matched)
			require.Nil(t, p.result())
			p.complete(kiropkg.Usage{}, false)
		})
	}
}
func TestKiroDynamicScopeIsolationAndCredentialRotation(t *testing.T) {
	a, g, _ := dynamicSetup(t)
	r := dynamicRequest(t, "claude-opus-5", "a")
	dynamicTrain(t, a, g, r)
	a.Credentials = map[string]any{"access_token": "rotated-access", "refresh_token": "rotated-refresh"}
	p := dynamicPrepare(t, a, g, r)
	require.Equal(t, 10000, p.matched)
	p.complete(kiropkg.Usage{}, false)
	for _, change := range []string{"account", "principal", "profile", "endpoint", "model"} {
		t.Run(change, func(t *testing.T) {
			other := *a
			other.Credentials = map[string]any{}
			request := r
			switch change {
			case "account":
				other.ID = 2
			case "principal":
				other.Credentials["principal_id"] = "new-principal"
			case "profile":
				request = mutateDynamicRequest(t, r, func(p *kiropkg.KiroPayload) { p.ProfileArn = "test-profile-b" })
			case "endpoint":
				request.CacheEndpoint = "https://fallback.test/generate"
			case "model":
				request = dynamicRequest(t, "gpt-5.6-terra", "a")
			}
			p := dynamicPrepare(t, &other, g, request)
			require.Zero(t, p.matched)
			require.Nil(t, p.result())
			p.complete(kiropkg.Usage{}, false)
		})
	}
	// API-key replacement without a profile is a principal replacement.
	a.Type = AccountTypeAPIKey
	a.Credentials = map[string]any{"api_key": "fake-key-a", "kiro_api_key": "unused-legacy-key"}
	r = mutateDynamicRequest(t, r, func(p *kiropkg.KiroPayload) { p.ProfileArn = "" })
	dynamicTrain(t, a, g, r)
	a.Credentials["api_key"] = "fake-key-b"
	p = dynamicPrepare(t, a, g, r)
	require.Zero(t, p.matched)
	p.complete(kiropkg.Usage{}, false)
}
func TestKiroDynamicUnknownDisabledAndFixedRatiosIgnored(t *testing.T) {
	a, g, _ := dynamicSetup(t)
	for _, model := range []string{"", "unknown", "gemini-3", "claude", "gpt", "other-claude-opus-5", "other-gpt-5.6-sol", "provider/claude-opus-5", "provider/gpt-5.6-luna", "CLAUDE-opus-5", "GPT-5.6-sol", " claude-opus-5"} {
		require.Zero(t, kiroDynamicTTL(model), model)
		require.Nil(t, prepareKiroDynamicCache(a, g, dynamicRequest(t, model, "a"), 10000), model)
	}
	r := dynamicRequest(t, "claude-opus-5", "a")
	dynamicTrain(t, a, g, r)
	g.KiroCacheEmulationRatio = 0
	g.KiroCacheReadEmulationRatio = 0
	g.KiroCacheCreationEmulationRatio = 0
	p := dynamicPrepare(t, a, g, r)
	require.Equal(t, 8000, p.result().CacheReadInputTokens)
	p.complete(kiropkg.Usage{}, false)
	g.KiroCacheEmulationEnabled = false
	require.Nil(t, prepareKiroDynamicCache(a, g, r, 10000))
}
func TestKiroDynamicInvalidCreditsFailuresAndOutputVariation(t *testing.T) {
	a, g, now := dynamicSetup(t)
	r := dynamicRequest(t, "claude-opus-5", "a")
	valid := dynamicUsage(t, 10, 20)
	for _, credits := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		p := dynamicPrepare(t, a, g, r)
		usage := valid
		usage.KiroCredits = credits
		p.complete(usage, true)
		require.Empty(t, p.scope.prefixes)
		require.Zero(t, p.scope.active)
	}
	p := dynamicPrepare(t, a, g, r)
	p.complete(valid, false)
	require.Empty(t, p.scope.prefixes)
	p = dynamicPrepare(t, a, g, r)
	p.complete(valid, true) // matched==0: records a cold sample, density 10/20 = .5
	// A repeated call against its own freshly-warmed terminal prefix now
	// calibrates even though output length varies wildly (21, then 500).
	// Before this fix the sample key also included the literal output token
	// count, so pairing required cold and warm calls to report an *identical*
	// output length -- append-only agent traffic almost never repeats an
	// output length exactly, so no pair was ever recorded and the estimator
	// stayed pinned at zero for real traffic. Density (credits per native
	// total token) removes that dependency.
	p = dynamicPrepare(t, a, g, r)
	p.complete(dynamicUsage(t, 2, 21), true) // density 2/21 vs cold .5 -> ratio ~.81
	require.Len(t, p.scope.pairs, 1)
	require.InDelta(t, 0.8095238095238095, p.scope.pairs[0].creditSavings, 1e-9)
	p = dynamicPrepare(t, a, g, r)
	p.complete(dynamicUsage(t, 2, 500), true) // density 2/500 vs cold .5 -> ratio capped at .9
	require.Len(t, p.scope.pairs, 2)
	require.InDelta(t, .9, p.scope.pairs[1].creditSavings, 1e-9)
	// An explicit outputTokens:0 does not by itself starve the denominator:
	// the native parser backfills output tokens from actual response text
	// ("hello", from dynamicUsage) when the declared count is zero. The
	// resulting density (2 credits / ~1 token) is far above the cold
	// baseline, so the ratio floors at 0 instead of going negative -- the
	// estimator never claims a warm call is more expensive than cold.
	p = dynamicPrepare(t, a, g, r)
	p.complete(dynamicUsage(t, 2, 0), true)
	require.Len(t, p.scope.pairs, 3)
	require.InDelta(t, 0, p.scope.pairs[2].creditSavings, 1e-9)
	// A genuinely empty response (no output text at all) plus a zero native
	// input token count leaves no total-token denominator whatsoever; the
	// observation is safely dropped instead of dividing by zero. Production
	// traffic always carries a positive native input-token count, so this
	// only guards a defensive edge case that the parser itself can't rule out.
	p = dynamicPrepare(t, a, g, r)
	p.complete(dynamicUsageEmptyOutput(t, 2), true)
	require.Len(t, p.scope.pairs, 3)
	p = dynamicPrepare(t, a, g, r)
	*now = now.Add(5 * time.Minute)
	p.complete(valid, true) // slow response must not resurrect expired cache
	require.Zero(t, p.scope.active)
	p = dynamicPrepare(t, a, g, r)
	require.Zero(t, p.matched)
	p.complete(kiropkg.Usage{}, false)
	*now = now.Add(31 * time.Minute)
	globalKiroDynamicTracker.mu.Lock()
	globalKiroDynamicTracker.prune(*now)
	require.Empty(t, globalKiroDynamicTracker.scopes)
	globalKiroDynamicTracker.mu.Unlock()
}
func TestKiroDynamicConcurrentPreparationAndCompletion(t *testing.T) {
	a, g, _ := dynamicSetup(t)
	r := dynamicRequest(t, "claude-opus-5", "a")
	usage := dynamicUsage(t, 10, 20)
	plans := make([]*kiroCacheEmulationPlan, 32)
	var wg sync.WaitGroup
	for i := range plans {
		wg.Add(1)
		go func(i int) { defer wg.Done(); plans[i] = prepareKiroDynamicCache(a, g, r, 10000) }(i)
	}
	wg.Wait()
	for _, p := range plans {
		require.NotNil(t, p)
		require.Zero(t, p.matched)
		wg.Add(1)
		go func(p *kiroCacheEmulationPlan) { defer wg.Done(); p.complete(usage, true); p.complete(usage, true) }(p)
	}
	wg.Wait()
	require.Zero(t, plans[0].scope.active)
	require.Empty(t, plans[0].scope.samples)
	require.Empty(t, plans[0].scope.pairs)
}
func TestKiroDynamicOutboundSemanticPrefixAndToolOrder(t *testing.T) {
	a, g, _ := dynamicSetup(t)
	r := dynamicRequest(t, "claude-opus-5", "a")
	dynamicTrain(t, a, g, r)
	changed := mutateDynamicRequest(t, r, func(p *kiropkg.KiroPayload) {
		p.ConversationState.CurrentMessage.UserInputMessage.Content = "changed final turn"
	})
	p := dynamicPrepare(t, a, g, changed)
	require.Greater(t, p.matched, 0)
	require.Less(t, p.matched, 10000)
	require.LessOrEqual(t, p.result().CacheReadInputTokens, p.matched)
	p.complete(kiropkg.Usage{}, false)
	changed = mutateDynamicRequest(t, r, func(p *kiropkg.KiroPayload) {
		p.ConversationState.History[0].UserInputMessage.Content += "current time changed"
	})
	p = dynamicPrepare(t, a, g, changed)
	require.Zero(t, p.matched)
	p.complete(kiropkg.Usage{}, false)
	tools := []kiropkg.KiroToolWrapper{{ToolSpecification: kiropkg.KiroToolSpecification{Name: "first"}}, {ToolSpecification: kiropkg.KiroToolSpecification{Name: "second"}}}
	toolRequest := mutateDynamicRequest(t, r, func(p *kiropkg.KiroPayload) {
		p.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext = &kiropkg.KiroUserInputMessageContext{Tools: tools}
	})
	p = dynamicPrepare(t, a, g, toolRequest)
	p.complete(dynamicUsage(t, 10, 20), true)
	swapped := mutateDynamicRequest(t, toolRequest, func(p *kiropkg.KiroPayload) {
		c := p.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
		c.Tools[0], c.Tools[1] = c.Tools[1], c.Tools[0]
	})
	p = dynamicPrepare(t, a, g, swapped)
	require.Zero(t, p.matched)
	p.complete(kiropkg.Usage{}, false)
	// Native content never passes through billing-header or presentation filters.
	prof, _, ok := buildKiroOutboundCacheProfile(r.CachePayload, 10000)
	require.True(t, ok)
	require.Greater(t, len(prof.blocks), 1)
	var obj map[string]any
	require.NoError(t, json.Unmarshal(r.CachePayload, &obj))
	// Reorder root fields and add whitespace; native semantics remain identical.
	raw, err := json.MarshalIndent(obj, "", "  ")
	require.NoError(t, err)
	equivalent, _, ok := buildKiroOutboundCacheProfile(raw, 10000)
	require.True(t, ok)
	require.Equal(t, prof.blocks, equivalent.blocks)
	advanced := mutateDynamicRequest(t, r, func(p *kiropkg.KiroPayload) {
		current := p.ConversationState.CurrentMessage.UserInputMessage
		p.ConversationState.History = append(p.ConversationState.History, kiropkg.KiroHistoryMessage{UserInputMessage: &current}, kiropkg.KiroHistoryMessage{AssistantResponseMessage: &kiropkg.KiroAssistantResponseMessage{Content: "answer"}})
		p.ConversationState.CurrentMessage.UserInputMessage.Content = "next turn"
	})
	advancedProfile, _, ok := buildKiroOutboundCacheProfile(advanced.CachePayload, 12000)
	require.True(t, ok)
	require.Equal(t, prof.blocks, advancedProfile.blocks[:len(prof.blocks)])
}

func TestKiroDynamicCapacityAndCleanup(t *testing.T) {
	a, g, now := dynamicSetup(t)
	r := dynamicRequest(t, "claude-opus-5", "a")
	profile, _, ok := buildKiroOutboundCacheProfile(r.CachePayload, 10000)
	require.True(t, ok)
	tracker := globalKiroDynamicTracker
	keyFor := func(n int) [32]byte { var k [32]byte; binary.BigEndian.PutUint64(k[:8], uint64(n)); return k }
	// Per-account quota rejects new namespaces without evicting any other account.
	for i := 1; i <= kiroDynamicMaxAccountScopes; i++ {
		p := tracker.prepare(a.ID, keyFor(i), keyFor(i), profile, 5*time.Minute, 0, false)
		require.NotNil(t, p)
		p.complete(kiropkg.Usage{}, false)
	}
	require.Nil(t, tracker.prepare(a.ID, keyFor(10000), keyFor(1), profile, 5*time.Minute, 0, false))
	for i := kiroDynamicMaxAccountScopes + 1; i <= kiroDynamicMaxScopes; i++ {
		p := tracker.prepare(int64(i), keyFor(i), keyFor(i), profile, 5*time.Minute, 0, false)
		require.NotNil(t, p)
		p.complete(kiropkg.Usage{}, false)
	}
	require.Len(t, tracker.scopes, kiroDynamicMaxScopes)
	require.Nil(t, tracker.prepare(99999, keyFor(10001), keyFor(1), profile, 5*time.Minute, 0, false))
	*now = now.Add(31 * time.Minute)
	tracker.mu.Lock()
	tracker.prune(*now)
	require.Empty(t, tracker.scopes)
	tracker.mu.Unlock()
	// Many disjoint native shapes within one namespace hit prefix and sample caps.
	usage := dynamicUsage(t, 10, 20)
	for i := 1; i <= kiroDynamicMaxPrefixes+10; i++ {
		copyProfile := *profile
		copyProfile.blocks = append([]kiroCacheBlock(nil), profile.blocks...)
		for j := range copyProfile.blocks {
			copyProfile.blocks[j].prefixFingerprint = keyFor(10000*i + j)
		}
		p := tracker.prepare(a.ID, keyFor(1), keyFor(i), &copyProfile, 5*time.Minute, 0, false)
		require.NotNil(t, p)
		p.complete(usage, true)
	}
	scope := tracker.scopes[keyFor(1)]
	require.Len(t, scope.prefixes, kiroDynamicMaxPrefixes)
	require.Len(t, scope.samples, kiroDynamicMaxSamples)
	*now = now.Add(5 * time.Minute)
	tracker.mu.Lock()
	tracker.prune(*now)
	require.Empty(t, scope.prefixes)
	require.Len(t, scope.samples, kiroDynamicMaxSamples)
	tracker.mu.Unlock()
	*now = now.Add(26 * time.Minute)
	tracker.mu.Lock()
	tracker.prune(*now)
	require.Empty(t, tracker.scopes)
	tracker.mu.Unlock()
	// Expired pair evidence does not survive merely because the scope is touched.
	dynamicTrain(t, a, g, r)
	p := dynamicPrepare(t, a, g, r)
	p.complete(kiropkg.Usage{}, false)
	*now = now.Add(31 * time.Minute)
	p = dynamicPrepare(t, a, g, r)
	require.Nil(t, p.result())
	require.Empty(t, p.scope.pairs)
	p.complete(kiropkg.Usage{}, false)
}

func TestKiroDynamicZeroRatiosAuthSnapshot(t *testing.T) {
	a, g, _ := dynamicSetup(t)
	for _, mode := range []string{KiroCacheEmulationModeUniform, KiroCacheEmulationModeIndependent} {
		g.KiroCacheEmulationMode = mode
		g.KiroCacheEmulationRatio = 0
		g.KiroCacheReadEmulationRatio = 0
		g.KiroCacheCreationEmulationRatio = 0
		NormalizeGroupRuntimeFields(g)
		require.True(t, g.EffectiveKiroCacheEmulationEnabled())
		svc := &APIKeyService{}
		snapshot := svc.snapshotFromAPIKey(context.Background(), &APIKey{ID: 1, User: &User{ID: 1}, Group: g})
		require.NotNil(t, snapshot)
		require.True(t, snapshot.Group.KiroCacheEmulationEnabled)
		restored := svc.snapshotToAPIKey("test-key", snapshot)
		require.True(t, restored.Group.EffectiveKiroCacheEmulationEnabled())
		p := dynamicPrepare(t, a, restored.Group, dynamicRequest(t, "claude-opus-5", "a"))
		p.complete(kiropkg.Usage{}, false)
	}
}

func TestKiroDynamicNativeCompletionGates(t *testing.T) {
	good := dynamicEvents(t, 10, 20)
	corrupt := append([]byte(nil), good...)
	corrupt[len(corrupt)-1] ^= 1
	missingMeter := dynamicFrame(t, "assistantResponseEvent", map[string]any{"content": "partial"})
	exception := append(dynamicFrame(t, "internalServerException", map[string]any{"message": "failed"}), good...)
	invalidMeter := append(dynamicFrame(t, "meteringEvent", map[string]any{"usage": "NaN"}), good...)
	malformed := append(dynamicFrame(t, "assistantResponseEvent", []string{"invalid event object"}), good...)
	for name, raw := range map[string][]byte{"complete": good, "missing-meter": missingMeter, "crc": corrupt, "exception": exception, "invalid-meter": invalidMeter, "malformed": malformed, "truncated": append(append([]byte(nil), good...), 1)} {
		for _, stream := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "-buffered", true: "-stream"}[stream], func(t *testing.T) {
				a, g, _ := dynamicSetup(t)
				p := dynamicPrepare(t, a, g, dynamicRequest(t, "claude-opus-5", "a"))
				var usage kiropkg.Usage
				var err error
				if stream {
					var result *kiropkg.StreamResult
					result, err = kiropkg.StreamEventStreamAsAnthropicWithContext(context.Background(), bytes.NewReader(raw), io.Discard, "claude-opus-5", 10000, kiropkg.KiroRequestContext{})
					if result != nil {
						usage = result.Usage
					}
				} else {
					var result *kiropkg.ParseResult
					result, err = kiropkg.ParseNonStreamingEventStreamWithContext(bytes.NewReader(raw), "claude-opus-5", kiropkg.KiroRequestContext{})
					if result != nil {
						usage = result.Usage
					}
				}
				p.complete(usage, err == nil)
				if name == "complete" {
					require.NotEmpty(t, p.scope.prefixes)
				} else {
					require.Empty(t, p.scope.prefixes)
					require.Empty(t, p.scope.samples)
				}
				require.Zero(t, p.scope.active)
			})
		}
	}
}

func TestKiroDynamicNativeImagesNumbersAndModelVisibleTime(t *testing.T) {
	_, _, _ = dynamicSetup(t)
	request := dynamicRequest(t, "claude-opus-5", "a")
	imageA := strings.SplitN(kiroPNGDataURL(t, 80, 80, color.RGBA{R: 255, A: 255}), ",", 2)[1]
	imageB := strings.SplitN(kiroPNGDataURL(t, 80, 80, color.RGBA{B: 255, A: 255}), ",", 2)[1]
	withImage := func(data string) kiropkg.KiroRequestContext {
		return mutateDynamicRequest(t, request, func(p *kiropkg.KiroPayload) {
			p.ConversationState.History[0].UserInputMessage.Images = []kiropkg.KiroImage{{Format: "png", Source: kiropkg.KiroImageSource{Bytes: data}}}
		})
	}
	pa, _, ok := buildKiroOutboundCacheProfile(withImage(imageA).CachePayload, 10000)
	require.True(t, ok)
	pb, _, ok := buildKiroOutboundCacheProfile(withImage(imageB).CachePayload, 10000)
	require.True(t, ok)
	require.NotEqual(t, pa.blocks[0].prefixFingerprint, pb.blocks[0].prefixFingerprint)
	require.Equal(t, pa.blocks[0].cumulativeTokens, pb.blocks[0].cumulativeTokens, "same dimensions use visual tokens, not base64 length")
	// Distinct integer tool arguments beyond float64 precision remain distinct.
	numeric := func(number string) kiropkg.KiroRequestContext {
		return mutateDynamicRequest(t, request, func(p *kiropkg.KiroPayload) {
			p.ConversationState.History[0] = kiropkg.KiroHistoryMessage{AssistantResponseMessage: &kiropkg.KiroAssistantResponseMessage{Content: strings.Repeat("history ", 5000), ToolUses: []kiropkg.KiroToolUse{{ToolUseID: "tool-a", Name: "lookup", Input: map[string]any{"id": json.Number(number)}}}}}
		})
	}
	pa, _, ok = buildKiroOutboundCacheProfile(numeric("9007199254740992").CachePayload, 10000)
	require.True(t, ok)
	pb, _, ok = buildKiroOutboundCacheProfile(numeric("9007199254740993").CachePayload, 10000)
	require.True(t, ok)
	require.NotEqual(t, pa.blocks[0].prefixFingerprint, pb.blocks[0].prefixFingerprint)
	temporal := func(value string) kiropkg.KiroRequestContext {
		return mutateDynamicRequest(t, request, func(p *kiropkg.KiroPayload) {
			p.ConversationState.History[0].UserInputMessage.Content = "[Context: Current time is " + value + "]\n" + p.ConversationState.History[0].UserInputMessage.Content
		})
	}
	pa, _, ok = buildKiroOutboundCacheProfile(temporal("2026-09-06 01:00:00 UTC").CachePayload, 10000)
	require.True(t, ok)
	pb, _, ok = buildKiroOutboundCacheProfile(temporal("2026-09-06 01:00:01 UTC").CachePayload, 10000)
	require.True(t, ok)
	require.NotEqual(t, pa.blocks[0].prefixFingerprint, pb.blocks[0].prefixFingerprint)
}

func TestKiroDynamicGrowingConversationCalibratesWithoutReplay(t *testing.T) {
	a, g, _ := dynamicSetup(t)
	base := mutateDynamicRequest(t, dynamicRequest(t, "claude-opus-5", "a"), func(p *kiropkg.KiroPayload) { p.InferenceConfig = &kiropkg.KiroInferenceConfig{MaxTokens: 1000} })
	grow := func(r kiropkg.KiroRequestContext, turn string) kiropkg.KiroRequestContext {
		return mutateDynamicRequest(t, r, func(p *kiropkg.KiroPayload) {
			current := p.ConversationState.CurrentMessage.UserInputMessage
			p.ConversationState.History = append(p.ConversationState.History, kiropkg.KiroHistoryMessage{UserInputMessage: &current}, kiropkg.KiroHistoryMessage{AssistantResponseMessage: &kiropkg.KiroAssistantResponseMessage{Content: "previous answer"}})
			p.ConversationState.CurrentMessage.UserInputMessage.Content = strings.Repeat(turn+" new input ", 50)
		})
	}
	p := dynamicPrepare(t, a, g, base)
	p.complete(dynamicUsage(t, 10, 20), true)
	second := grow(base, "second")
	p = dynamicPrepare(t, a, g, second)
	// Fewer than two dynamic pairs exist yet, but the canonical prefix chain
	// already proves a real match: the static pre-calibration fallback fires
	// here using kiroCacheGroup(1)'s ratio of 1.0, which passes the matched
	// count through unchanged (see scaleKiroCacheTokens's ratio>=1 branch).
	require.NotNil(t, p.result())
	require.Equal(t, p.matched, p.result().CacheReadInputTokens)
	require.Greater(t, p.matched, 0)
	require.Less(t, p.matched, 10000)
	p.complete(dynamicUsage(t, 3, 20), true) // warm base cost2 plus appended uncached cost1
	require.Len(t, p.scope.pairs, 1)
	require.InDelta(t, .7, p.scope.pairs[0].creditSavings, 1e-9)
	third := grow(second, "third")
	p = dynamicPrepare(t, a, g, third)
	// Still only one dynamic pair recorded -- the fallback fires again.
	require.NotNil(t, p.result())
	require.Equal(t, p.matched, p.result().CacheReadInputTokens)
	p.complete(dynamicUsage(t, 4, 20), true)
	require.Len(t, p.scope.pairs, 2)
	require.InDelta(t, .6, p.scope.pairs[1].creditSavings, 1e-9)
	fourth := grow(third, "fourth")
	p = dynamicPrepare(t, a, g, fourth)
	require.NotNil(t, p.result())
	require.Equal(t, int(float64(p.matched)*.6), p.result().CacheReadInputTokens)
	p.complete(kiropkg.Usage{}, false)
	require.Len(t, p.scope.samples, 1, "learning used the original cold prefix without full replay")
	for _, kind := range []string{"output", "config", "prefix"} {
		t.Run(kind, func(t *testing.T) {
			a, g, _ := dynamicSetup(t)
			p := dynamicPrepare(t, a, g, base)
			p.complete(dynamicUsage(t, 10, 20), true)
			request := second
			output := 20
			switch kind {
			case "output":
				output = 21
			case "config":
				request = mutateDynamicRequest(t, request, func(p *kiropkg.KiroPayload) { p.InferenceConfig.MaxTokens = 2000 })
			case "prefix":
				request = mutateDynamicRequest(t, request, func(p *kiropkg.KiroPayload) {
					p.ConversationState.History[0].UserInputMessage.Content += "changed interior system content"
				})
			}
			p = dynamicPrepare(t, a, g, request)
			p.complete(dynamicUsage(t, 3, output), true)
			if kind == "output" {
				// A different output length no longer blocks pairing: density
				// (credits per native total token) cancels the output-length
				// term, so cold .5 (10/20) vs warm 3/21 still calibrates. This
				// is the exact regression scenario -- agent traffic varies
				// output per turn far more than the fixed 20 used elsewhere in
				// this test, and the pre-fix code required an identical
				// output count to ever record a pair.
				require.Len(t, p.scope.pairs, 1)
				require.InDelta(t, 0.7142857142857143, p.scope.pairs[0].creditSavings, 1e-9)
			} else {
				// "config" changes the inference-config shape hash and
				// "prefix" changes the fingerprint chain itself, so neither
				// finds the recorded cold sample regardless of output length.
				require.Empty(t, p.scope.pairs)
			}
		})
	}
}

func TestKiroDynamicUsesActualOutboundAPIKeyHash(t *testing.T) {
	a, g, _ := dynamicSetup(t)
	a.Type = AccountTypeAPIKey
	a.Credentials = map[string]any{"api_key": "fake-key-a"}
	r := mutateDynamicRequest(t, dynamicRequest(t, "claude-opus-5", "a"), func(p *kiropkg.KiroPayload) { p.ProfileArn = "" })
	r.CachePrincipalHash = sha256.Sum256([]byte("fake-key-a"))
	dynamicTrain(t, a, g, r)
	// Account storage can change after dispatch; the accepted attempt still used A.
	a.Credentials["api_key"] = "fake-key-b"
	p := dynamicPrepare(t, a, g, r)
	require.Equal(t, 10000, p.matched)
	p.complete(kiropkg.Usage{}, false)
	r.CachePrincipalHash = sha256.Sum256([]byte("fake-key-b"))
	p = dynamicPrepare(t, a, g, r)
	require.Zero(t, p.matched)
	p.complete(kiropkg.Usage{}, false)
}

func TestKiroDynamicFamilyCalibrationRemainsModelSpecific(t *testing.T) {
	a, g, _ := dynamicSetup(t)
	keys := make(map[[32]byte]string)
	for _, model := range []string{"claude-opus-5", "claude-opus-4.8", "claude-opus-4.6", "gpt-5.6-sol", "gpt-5.6-luna", "gpt-5.6-terra"} {
		r := dynamicRequest(t, model, "same-conversation")
		cold := dynamicPrepare(t, a, g, r)
		require.NotContains(t, keys, cold.key, model)
		keys[cold.key] = model
		require.Zero(t, cold.matched)
		require.Empty(t, cold.scope.pairs)
		require.Nil(t, cold.result())
		cold.complete(kiropkg.Usage{}, false)
		dynamicTrain(t, a, g, r)
		warm := dynamicPrepare(t, a, g, r)
		require.NotNil(t, warm.result())
		if kiroModelIsGPTFamily(model) {
			// GPT-family converts the .8 credit-savings pairs to a clamped
			// full reuse (see TestKiroDynamicModelSessionAndSlidingTTL).
			require.Equal(t, 10000, warm.result().CacheReadInputTokens, model)
		} else {
			require.Equal(t, 8000, warm.result().CacheReadInputTokens, model)
		}
		warm.complete(kiropkg.Usage{}, false)
	}
}

// TestKiroGPTInferredReuseConversionFormula is the focused unit test for the
// pure conversion function: r = clamp(s/(1-d), 0, 1) with
// d = kiroGPTCachedMarginalCreditRatio. It exercises the formula directly,
// independent of the estimator's prepare/complete plumbing.
func TestKiroGPTInferredReuseConversionFormula(t *testing.T) {
	const d = kiroGPTCachedMarginalCreditRatio
	for _, tc := range []struct {
		name     string
		savings  float64
		expected float64
	}{
		{"zeroSavingsMeansZeroReuse", 0, 0},
		{"boundarySavingsSaturatesToFullReuse", 1 - d, 1}, // s == 1-d is exactly r == 1
		{"perPairCapAlsoSaturates", .9, 1},                // the pair-level .9 cap still saturates
		{"midRangeSavingsScalesLinearly", 0.2, 0.2 / (1 - d)},
		{"fullSavingsClampsToOne", 1, 1},
		{"defensiveNegativeSavingsClampsToZero", -0.1, 0}, // creditSavings is never negative in practice, but the pure function must not report negative reuse
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.InDelta(t, tc.expected, kiroGPTInferredReuse(tc.savings), 1e-9)
		})
	}
	require.Less(t, kiroGPTInferredReuse(.1), kiroGPTInferredReuse(.3), "higher observed savings must never produce lower inferred reuse")
}

// TestKiroDynamicCreditSavingsConversionAppliesOnlyToGPTFamily is the focused
// test for family routing: given identical observed credit-savings pairs,
// GPT-family traffic converts them through kiroGPTInferredReuse while
// Claude-family traffic (uncalibrated for this factor) continues to use the
// raw credit-savings fraction directly as the reuse fraction, exactly as
// before this change.
func TestKiroDynamicCreditSavingsConversionAppliesOnlyToGPTFamily(t *testing.T) {
	for _, tc := range []struct {
		model string
		gpt   bool
	}{
		{"claude-opus-5", false},
		{"gpt-5.6-luna", true},
	} {
		t.Run(tc.model, func(t *testing.T) {
			a, g, _ := dynamicSetup(t)
			r := dynamicRequest(t, tc.model, "family-routing")
			dynamicTrain(t, a, g, r) // cold density .5, two warm density .1 -> creditSavings .8 each
			p := dynamicPrepare(t, a, g, r)
			require.Len(t, p.scope.pairs, 2)
			require.InDelta(t, .8, p.scope.pairs[0].creditSavings, 1e-9)
			result := p.result()
			require.NotNil(t, result)
			require.Equal(t, tc.gpt, kiroModelIsGPTFamily(tc.model))
			if tc.gpt {
				require.Equal(t, p.matched, result.CacheReadInputTokens,
					"GPT-family must convert .8 credit savings (s/(1-d)~=1.68) to a clamped full reuse, not report .8 itself")
				require.Zero(t, result.InputTokens)
			} else {
				require.Equal(t, int(float64(p.matched)*.8), result.CacheReadInputTokens,
					"Claude-family remains uncalibrated for this factor: raw credit savings is still used directly as reuse")
			}
			p.complete(kiropkg.Usage{}, false)
		})
	}
}

// kiroRealTrafficRequest seeds a session with content distinct from any other
// label, unlike dynamicRequest's fixed boilerplate history (which is
// byte-identical regardless of its conversation-ID argument -- conversation
// ID is NOT part of the Claude-family fingerprint chain, only genuine
// content is, per buildKiroOutboundCacheProfile). Two concurrent real
// sessions behind one account always start from distinct system/history
// content, so their fingerprint chains diverge from call 0; this reproduces
// that by seeding the label into the first history block itself.
func kiroRealTrafficRequest(t *testing.T, label string) kiropkg.KiroRequestContext {
	t.Helper()
	base := dynamicRequest(t, "claude-opus-5", label+"-conversation")
	return mutateDynamicRequest(t, base, func(p *kiropkg.KiroPayload) {
		p.ConversationState.History[0].UserInputMessage.Content = label + "-seed " + p.ConversationState.History[0].UserInputMessage.Content
	})
}

// kiroRealTrafficGrow appends the given call's turn as history (never
// rewriting anything already sent) and installs a new, distinct current
// turn -- the append-only shape of real agent traffic.
func kiroRealTrafficGrow(t *testing.T, req kiropkg.KiroRequestContext, label string, callIdx int) kiropkg.KiroRequestContext {
	t.Helper()
	return mutateDynamicRequest(t, req, func(p *kiropkg.KiroPayload) {
		current := p.ConversationState.CurrentMessage.UserInputMessage
		p.ConversationState.History = append(p.ConversationState.History,
			kiropkg.KiroHistoryMessage{UserInputMessage: &current},
			kiropkg.KiroHistoryMessage{AssistantResponseMessage: &kiropkg.KiroAssistantResponseMessage{Content: label + " reply " + strconv.Itoa(callIdx)}})
		p.ConversationState.CurrentMessage.UserInputMessage.Content = strings.Repeat(label+"-turn-"+strconv.Itoa(callIdx)+" ", 20)
	})
}

// TestKiroDynamicRealProductionTraffic replays the exact sanitized production
// corpus in kiro_dynamic_cache_realtraffic_test.go: two real, concurrent
// Claude Opus cron sessions (60 and 61 calls) behind the same account, whose
// native cache_read/cache_creation were 0 for every single call and whose
// input token counts are NOT monotonic (compaction/tool-result changes shrink
// them at several points -- verified programmatically below, not merely
// asserted in a comment). This is the exact incident: aggregate reported
// cache rate collapsed from ~69.5% to ~0 for this traffic the day
// kiroDynamicSampleKey started keying samples by literal output token count,
// which real append-only agent traffic (57 and 60 distinct output lengths
// here) essentially never repeats.
//
// Calls from both sessions are interleaved call-by-call (session A's call i,
// then session B's call i) against one shared account/tracker, matching how
// two simultaneous requests on the same outer key are actually processed.
// Per prepareKiroDynamicCache, Claude-family scope keys do not include the
// conversation ID (only GPT does, see TestKiroDynamicModelSessionAndSlidingTTL
// and TestKiroDynamicFamilyCalibrationRemainsModelSpecific) -- so these two
// concurrent conversations intentionally share one calibration pool (pairs),
// exactly as the real production account would, while remaining fully
// isolated at the content level: each session's own distinct message history
// produces its own prefix-fingerprint chain, so one session's blocks can
// never register as "matched" against the other's (asserted below via each
// session's own first call staying a fresh matched==0 cold start even though
// the shared scope is already warm from the other session).
func TestKiroDynamicRealProductionTraffic(t *testing.T) {
	requireNonMonotonicInput := func(t *testing.T, name string, rows []kiroRealTrafficRow) {
		t.Helper()
		decreases := 0
		for i := 1; i < len(rows); i++ {
			if rows[i].Input < rows[i-1].Input {
				decreases++
			}
		}
		require.Greater(t, decreases, 0, "%s: fixture must contain non-monotonic input (found none) -- otherwise this test would not cover the reported production shape", name)
	}
	requireNonMonotonicInput(t, "session A", kiroRealTrafficSessionA)
	requireNonMonotonicInput(t, "session B", kiroRealTrafficSessionB)
	require.Len(t, kiroRealTrafficSessionA, 60)
	require.Len(t, kiroRealTrafficSessionB, 61)

	t.Run("concurrentSharedAccountConservativeAndSustained", func(t *testing.T) {
		a, g, _ := dynamicSetup(t)
		// A deliberately conservative, non-1.0 static ratio: distinct from
		// the 1.0 default dynamicSetup uses elsewhere, so the pre-calibration
		// static fallback below is observably different from "just report
		// the full matched count" and its own bound can be checked precisely.
		const staticRatio = 0.5
		g.KiroCacheEmulationMode = KiroCacheEmulationModeUniform
		g.KiroCacheEmulationRatio = staticRatio
		reqs := map[string]kiropkg.KiroRequestContext{
			"A": kiroRealTrafficRequest(t, "prod-session-a"),
			"B": kiroRealTrafficRequest(t, "prod-session-b"),
		}
		data := map[string][]kiroRealTrafficRow{"A": kiroRealTrafficSessionA, "B": kiroRealTrafficSessionB}
		trueRatio := map[string][]float64{"A": kiroRealTrafficTrueRatio(kiroRealTrafficSessionA), "B": kiroRealTrafficTrueRatio(kiroRealTrafficSessionB)}
		positive := map[string][]bool{"A": make([]bool, len(data["A"])), "B": make([]bool, len(data["B"]))}
		reportedSum := map[string]float64{}
		trueEquivSum := map[string]float64{}
		maxLen := len(kiroRealTrafficSessionA)
		if len(kiroRealTrafficSessionB) > maxLen {
			maxLen = len(kiroRealTrafficSessionB)
		}
		for i := 0; i < maxLen; i++ {
			for _, name := range []string{"A", "B"} {
				rows := data[name]
				if i >= len(rows) {
					continue
				}
				row := rows[i]
				req := reqs[name]
				if i > 0 {
					req = kiroRealTrafficGrow(t, req, name, i)
				}
				reqs[name] = req
				// A dropped/retried attempt at the current prefix depth
				// (native metering never completed, e.g. a mid-stream
				// disconnect) must not corrupt calibration or leak a pair --
				// exercised once per session, mid-run, without consuming a
				// real fixture row.
				if i == 20 {
					aborted := prepareKiroDynamicCache(a, g, req, row.Input)
					require.NotNil(t, aborted, "%s: aborted-attempt prepare at call %d", name, i)
					pairsBefore := len(aborted.scope.pairs)
					aborted.complete(kiropkg.Usage{}, false)
					require.Len(t, aborted.scope.pairs, pairsBefore, "%s: aborted attempt at call %d must not add a pair", name, i)
				}
				p := prepareKiroDynamicCache(a, g, req, row.Input)
				require.NotNil(t, p, "%s: call %d", name, i)
				matched := p.matched
				// p.scope.pairs reflects the state prepare() saw: prepare()
				// itself never mutates pairs, only this call's own complete()
				// (below) can, so reading it here is an accurate snapshot of
				// which branch (dynamic vs. static fallback) governed this
				// call's estimate.
				dynamicActive := len(p.scope.pairs) >= 2
				if i == 0 {
					// First-call cold baseline: even though the shared scope
					// may already be warm from the other session's calls
					// (interleaved), this session's own first call has never
					// contributed a matching prefix fingerprint yet.
					require.Zero(t, matched, "%s: first call must be a fresh cold start (content isolation)", name)
					require.Nil(t, p.result(), "%s: first call must not report a cache estimate", name)
				}
				switch {
				case matched == 0:
					// No fallback without a proven prefix: matched==0 covers
					// every "not yet trustworthy" case (a true prefix miss,
					// an account/model/profile/endpoint/session scope
					// change, or an expired prefix TTL).
					require.Nil(t, p.result(), "%s: call %d: matched==0 must never report a cache estimate", name, i)
				case !dynamicActive:
					// The core new invariant this turn adds: a proven prefix
					// match (matched > 0) with fewer than two dynamic pairs
					// must never report zero merely because native-credit
					// calibration is not ready yet -- the static fallback
					// (staticRatio > 0) is deterministic and always positive
					// here (the fixture's matched counts are all in the
					// thousands, far above where rounding could reach 0).
					result := p.result()
					require.NotNil(t, result, "%s: call %d: matched>0 with <2 pairs must use the static fallback, never report nothing", name, i)
					require.LessOrEqual(t, result.CacheReadInputTokens, matched, "%s: call %d", name, i)
					require.Equal(t, scaleKiroCacheTokens(matched, staticRatio), result.CacheReadInputTokens,
						"%s: call %d: fallback estimate does not match the static ratio/rounding semantics", name, i)
					require.Greater(t, result.CacheReadInputTokens, 0, "%s: call %d", name, i)
					positive[name][i] = true
					reportedSum[name] += float64(result.CacheReadInputTokens)
					trueEquivSum[name] += float64(matched) * trueRatio[name][i]
				default:
					// Dynamic calibration (>=2 pairs) governs and takes
					// precedence over the fallback. It may legitimately
					// report nil (a genuinely calibrated "no reuse" verdict,
					// as established by TestKiroDynamicCalibrationAndFrozenUsage)
					// or a positive estimate bounded by the .9 lower-envelope
					// cap.
					if result := p.result(); result != nil {
						require.LessOrEqual(t, result.CacheReadInputTokens, matched, "%s: call %d", name, i)
						ratio := float64(result.CacheReadInputTokens) / float64(matched)
						require.LessOrEqual(t, ratio, .9+1e-9, "%s: call %d exceeds the .9 dynamic lower-envelope cap", name, i)
						positive[name][i] = true
						reportedSum[name] += float64(result.CacheReadInputTokens)
						trueEquivSum[name] += float64(matched) * trueRatio[name][i]
					}
				}
				p.complete(dynamicUsageWithInput(t, row.Credits, row.Input, row.Output), true)
			}
		}
		for _, name := range []string{"A", "B"} {
			// Immediate, sustained calibration: with the static fallback in
			// place, only call 0 (the genuine cold start, matched==0) may
			// ever report zero -- every call from index 1 onward has
			// matched>0 (the conversation only ever appends) and therefore
			// reports a positive estimate immediately, whether from the
			// static fallback (<2 pairs) or dynamic calibration (>=2 pairs).
			// This is the exact regression this turn's requirement targets:
			// calls after the first proven prefix match must never return
			// zero merely because dynamic calibration is not ready yet. (A
			// small allowance beyond index 0 is kept in case dynamic
			// calibration -- once active -- legitimately computes an exact
			// zero ratio for a given call, a real "no reuse" verdict rather
			// than "not ready"; replaying this fixture never hits that case.)
			lastZero := -1
			for i, ok := range positive[name] {
				if !ok {
					lastZero = i
				}
			}
			require.LessOrEqual(t, lastZero, 0, "%s: only the genuine cold start (call 0) may report zero once the static fallback is active", name)
			tailStart := len(positive[name]) - 10
			for i := tailStart; i < len(positive[name]); i++ {
				require.True(t, positive[name][i], "%s: call %d in the final stretch reported no cache reuse", name, i)
			}
			// Conservative vs. the real observed native-credit reduction: the
			// total cache-equivalent tokens this estimator ever claimed for
			// the session must stay meaningfully below what the raw
			// native-credit series actually implies (independently computed
			// in kiroRealTrafficTrueRatio, never seen by the estimator).
			require.Greater(t, reportedSum[name], 0.0, "%s: estimator should have reported nonzero cache reuse", name)
			require.Less(t, reportedSum[name]/trueEquivSum[name], .95, "%s: aggregate estimate is not conservative relative to observed native-credit reduction", name)
		}
	})

	t.Run("accountIsolationWithRealTrafficShape", func(t *testing.T) {
		// A second account replaying the exact same real fixture starts
		// completely fresh, regardless of how warm the first account's scope
		// (from the subtest above, or from data prepared later in this same
		// subtest) has become -- native content and calibration never cross
		// an account boundary.
		a1, g, _ := dynamicSetup(t)
		a2 := &Account{ID: a1.ID + 1, Platform: PlatformKiro, Type: AccountTypeOAuth}
		r1 := dynamicRequest(t, "claude-opus-5", "iso-a")
		r2 := dynamicRequest(t, "claude-opus-5", "iso-a") // identical conversation on purpose
		for i, row := range kiroRealTrafficSessionA[:5] {
			if i > 0 {
				r1 = kiroRealTrafficGrow(t, r1, "iso", i)
			}
			p := prepareKiroDynamicCache(a1, g, r1, row.Input)
			require.NotNil(t, p)
			p.complete(dynamicUsageWithInput(t, row.Credits, row.Input, row.Output), true)
		}
		p2 := prepareKiroDynamicCache(a2, g, r2, kiroRealTrafficSessionA[0].Input)
		require.NotNil(t, p2)
		require.Zero(t, p2.matched, "a distinct account must never inherit another account's calibration or prefixes")
		require.Nil(t, p2.result())
		p2.complete(kiropkg.Usage{}, false)
	})
}

// TestKiroDynamicUnstablePrefixMidSessionRecalibratesUnderVariableOutput
// combines two realistic disruptions in one growing, variable-output session:
// an interior edit (e.g. context compaction rewriting an earlier turn) breaks
// the fingerprint chain for one call, and output length keeps varying across
// every call as in production. The estimator must fall back to no-cache for
// the disrupted call without corrupting state, then recalibrate from scratch
// -- exactly as if the session had started cold again.
func TestKiroDynamicUnstablePrefixMidSessionRecalibratesUnderVariableOutput(t *testing.T) {
	a, g, _ := dynamicSetup(t)
	base := dynamicRequest(t, "claude-opus-5", "unstable")
	outputs := []int{6, 1647, 42, 300, 980, 55, 1200}
	grow := func(req kiropkg.KiroRequestContext, turnIdx int) kiropkg.KiroRequestContext {
		return mutateDynamicRequest(t, req, func(p *kiropkg.KiroPayload) {
			current := p.ConversationState.CurrentMessage.UserInputMessage
			p.ConversationState.History = append(p.ConversationState.History,
				kiropkg.KiroHistoryMessage{UserInputMessage: &current},
				kiropkg.KiroHistoryMessage{AssistantResponseMessage: &kiropkg.KiroAssistantResponseMessage{Content: "reply " + strconv.Itoa(turnIdx)}})
			p.ConversationState.CurrentMessage.UserInputMessage.Content = strings.Repeat("turn-"+strconv.Itoa(turnIdx)+" ", 20)
		})
	}
	r := base
	input := 10000
	sawPositive := false
	for i := 0; i < 6; i++ {
		if i > 0 {
			r = grow(r, i)
			input += 1500
		}
		p := prepareKiroDynamicCache(a, g, r, input)
		require.NotNil(t, p)
		if result := p.result(); result != nil && result.CacheReadInputTokens > 0 {
			sawPositive = true
		}
		p.complete(dynamicUsageWithInput(t, 1, input, outputs[i%len(outputs)]), true)
	}
	require.True(t, sawPositive, "session should have calibrated before the disruption")
	// Interior edit: rewrites the earliest history message, breaking every
	// downstream fingerprint. No prior breakpoint can match.
	disrupted := mutateDynamicRequest(t, r, func(p *kiropkg.KiroPayload) {
		p.ConversationState.History[0].UserInputMessage.Content += " (edited by context compaction)"
	})
	input += 1500
	p := prepareKiroDynamicCache(a, g, disrupted, input)
	require.NotNil(t, p)
	require.Zero(t, p.matched, "an interior edit must not be reported as matched")
	require.Nil(t, p.result())
	p.complete(dynamicUsageWithInput(t, 1, input, 33), true) // matched==0: records a fresh cold sample
	// Recalibration: continue growing from the disrupted state; a new
	// baseline forms and reuse resumes within a few calls, same as a cold
	// start.
	r = disrupted
	positive := false
	for i := 1; i <= 4; i++ {
		r = grow(r, 100+i)
		input += 1500
		p = prepareKiroDynamicCache(a, g, r, input)
		require.NotNil(t, p)
		if result := p.result(); result != nil && result.CacheReadInputTokens > 0 {
			positive = true
		}
		p.complete(dynamicUsageWithInput(t, 1, input, outputs[i%len(outputs)]), true)
	}
	require.True(t, positive, "session should recalibrate after the disruption clears")
}

// TestKiroDynamicVariableOutputOutlierResistance extends the fixed-output
// outlier-recovery coverage in TestKiroDynamicCalibrationAndFrozenUsage to
// the variable-output path this fix introduces: a single anomalously cheap
// warm observation must not let the estimator claim more than the
// lower-envelope policy already allows, and the anomaly must age out of the
// 8-pair window as later, unremarkable observations push it out.
func TestKiroDynamicVariableOutputOutlierResistance(t *testing.T) {
	a, g, _ := dynamicSetup(t)
	r := dynamicRequest(t, "claude-opus-5", "outlier")
	// Cold sample: density 10/20 = .5.
	p := dynamicPrepare(t, a, g, r)
	p.complete(dynamicUsage(t, 10, 20), true)
	warm := func(credits float64, output int) *kiroCacheEmulationPlan {
		p := dynamicPrepare(t, a, g, r)
		p.complete(dynamicUsage(t, credits, output), true)
		return p
	}
	// A normal, varying-output run of warm calls settles at the .9 cap.
	warm(2, 250) // density 2/250=.008 -> ratio .984 capped to .9
	warm(3, 900) // density 3/900=.00333 -> ratio .99333 capped to .9
	require.InDelta(t, .9, p.scope.pairs[len(p.scope.pairs)-1].creditSavings, 1e-9)
	// Outlier: a warm call whose density (3.5/10=.35) is much closer to cold
	// (.5) than the others -- e.g. a partial cache miss upstream -- drags the
	// lower envelope down hard and immediately, with no smoothing.
	outlierPlan := warm(3.5, 10) // ratio (.5-.35)/.5 = .3
	require.Len(t, outlierPlan.scope.pairs, 3)
	outlierRatio := outlierPlan.scope.pairs[2].creditSavings
	require.InDelta(t, .3, outlierRatio, 1e-9)
	require.Less(t, outlierRatio, .5)
	next := dynamicPrepare(t, a, g, r)
	require.NotNil(t, next.result())
	require.InDelta(t, outlierRatio, float64(next.result().CacheReadInputTokens)/float64(next.matched), 1e-3,
		"the lower envelope must apply the outlier immediately, never a smoothed/averaged ratio")
	next.complete(kiropkg.Usage{}, false)
	// Eight more unremarkable warm calls (varying output throughout) are
	// needed to push the outlier (the 3rd of what becomes 11 pairs ever
	// recorded) out of the fixed 8-pair window.
	for i := 0; i < 8; i++ {
		warm(2, 300+i*37)
	}
	require.Len(t, p.scope.pairs, 8)
	for _, pair := range p.scope.pairs {
		require.NotEqual(t, outlierRatio, pair.creditSavings, "outlier must have aged out of the fixed-size window")
	}
	recovered := dynamicPrepare(t, a, g, r)
	require.NotNil(t, recovered.result())
	require.Greater(t, float64(recovered.result().CacheReadInputTokens)/float64(recovered.matched), outlierRatio,
		"once the outlier ages out, the estimate recovers toward the unremarkable trend")
	recovered.complete(kiropkg.Usage{}, false)
}

// TestKiroDynamicStaticFallbackBeforeCalibration is the focused test for the
// static pre-estimator fallback: canonical prefix matching has already
// proven matched > 0, but native-credit calibration has not yet produced two
// valid dynamic pairs. The fallback must report the group's
// EffectiveKiroCacheReadEmulationRatio applied to the proven matched count,
// with the exact bounds/rounding of the original static estimator
// (scaleKiroCacheTokens): a non-integral ratio rounds to the nearest token,
// bounded to [0, matched].
func TestKiroDynamicStaticFallbackBeforeCalibration(t *testing.T) {
	a, g, _ := dynamicSetup(t)
	const staticRatio = 0.4
	g.KiroCacheEmulationMode = KiroCacheEmulationModeUniform
	g.KiroCacheEmulationRatio = staticRatio
	r := dynamicRequest(t, "claude-opus-5", "fallback-before-calibration")

	// Call 1: fresh scope, matched==0 -- no prefix has ever been proven yet,
	// so there is nothing for the fallback to scale.
	p := dynamicPrepare(t, a, g, r)
	require.Zero(t, p.matched)
	require.Nil(t, p.result())
	require.Empty(t, p.scope.pairs)
	p.complete(dynamicUsage(t, 10, 20), true) // records the cold sample only

	// Call 2: the exact same content is now a proven prefix match
	// (matched == the full profile), but zero dynamic pairs exist yet.
	p = dynamicPrepare(t, a, g, r)
	require.Equal(t, 10000, p.matched)
	require.Empty(t, p.scope.pairs, "fewer than two pairs must still be true here")
	result := p.result()
	require.NotNil(t, result, "matched>0 with zero pairs must still report the static fallback, not nothing")
	require.Equal(t, scaleKiroCacheTokens(p.matched, staticRatio), result.CacheReadInputTokens)
	require.Equal(t, 4000, result.CacheReadInputTokens) // round(10000*0.4)
	require.Equal(t, p.matched-result.CacheReadInputTokens, result.InputTokens)
	require.Zero(t, result.CacheCreationInputTokens, "the read fallback never fabricates a cache-creation estimate")
	p.complete(dynamicUsage(t, 2, 20), true) // forms pair 1 (still <2 pairs)

	// Call 3: one dynamic pair exists now, still fewer than the two required
	// -- the fallback must still govern, using the identical static ratio.
	p = dynamicPrepare(t, a, g, r)
	require.Equal(t, 10000, p.matched)
	require.Len(t, p.scope.pairs, 1)
	result = p.result()
	require.NotNil(t, result)
	require.Equal(t, 4000, result.CacheReadInputTokens)
	p.complete(kiropkg.Usage{}, false)
}

// TestKiroDynamicStaticFallbackTransitionsToDynamic proves the precedence
// rule explicitly: the moment dynamic calibration produces two valid pairs,
// its estimate governs even though it differs sharply from the static
// fallback ratio that governed the calls immediately before it.
func TestKiroDynamicStaticFallbackTransitionsToDynamic(t *testing.T) {
	a, g, _ := dynamicSetup(t)
	const staticRatio = 0.3 // deliberately far from the dynamic ratio below (.8)
	g.KiroCacheEmulationMode = KiroCacheEmulationModeUniform
	g.KiroCacheEmulationRatio = staticRatio
	r := dynamicRequest(t, "claude-opus-5", "fallback-transition")

	p := dynamicPrepare(t, a, g, r)
	require.Zero(t, p.matched)
	p.complete(dynamicUsage(t, 10, 20), true) // cold density .5

	// Fallback governs for the next two calls (0 then 1 pairs).
	for i := 0; i < 2; i++ {
		p = dynamicPrepare(t, a, g, r)
		require.Less(t, len(p.scope.pairs), 2)
		result := p.result()
		require.NotNil(t, result, "call %d", i)
		require.Equal(t, 3000, result.CacheReadInputTokens, "call %d: static fallback = round(10000*0.3)", i)
		p.complete(dynamicUsage(t, 2, 20), true) // density .1 -> pair ratio .8 each time
	}
	require.Len(t, p.scope.pairs, 2)

	// The instant two pairs exist, dynamic calibration (ratio .8, far above
	// the static .3) takes over unconditionally.
	p = dynamicPrepare(t, a, g, r)
	require.Len(t, p.scope.pairs, 2)
	result := p.result()
	require.NotNil(t, result)
	require.Equal(t, 8000, result.CacheReadInputTokens, "dynamic estimate (ratio .8) must govern, not the static .3 fallback")
	require.NotEqual(t, scaleKiroCacheTokens(p.matched, staticRatio), result.CacheReadInputTokens)
	p.complete(kiropkg.Usage{}, false)
}

// TestKiroDynamicStaticFallbackIndependentVsUniformRatio confirms the
// fallback consults exactly EffectiveKiroCacheReadEmulationRatio in both
// group ratio modes: in uniform mode that resolves to the single configured
// ratio, and in independent mode it resolves to the read-specific ratio,
// never the (deliberately very different) creation-specific ratio.
func TestKiroDynamicStaticFallbackIndependentVsUniformRatio(t *testing.T) {
	t.Run("uniform", func(t *testing.T) {
		a, g, _ := dynamicSetup(t)
		g.KiroCacheEmulationMode = KiroCacheEmulationModeUniform
		g.KiroCacheEmulationRatio = 0.6
		require.InDelta(t, 0.6, g.EffectiveKiroCacheReadEmulationRatio(), 1e-9)
		r := dynamicRequest(t, "claude-opus-5", "fallback-uniform")
		p := dynamicPrepare(t, a, g, r)
		p.complete(dynamicUsage(t, 10, 20), true)
		p = dynamicPrepare(t, a, g, r)
		result := p.result()
		require.NotNil(t, result)
		require.Equal(t, 6000, result.CacheReadInputTokens) // round(10000*0.6)
		p.complete(kiropkg.Usage{}, false)
	})
	t.Run("independent", func(t *testing.T) {
		a, g, _ := dynamicSetup(t)
		g.KiroCacheEmulationMode = KiroCacheEmulationModeIndependent
		g.KiroCacheReadEmulationRatio = 0.25
		g.KiroCacheCreationEmulationRatio = 0.9 // deliberately different; must be ignored by the read fallback
		require.InDelta(t, 0.25, g.EffectiveKiroCacheReadEmulationRatio(), 1e-9)
		r := dynamicRequest(t, "claude-opus-5", "fallback-independent")
		p := dynamicPrepare(t, a, g, r)
		p.complete(dynamicUsage(t, 10, 20), true)
		p = dynamicPrepare(t, a, g, r)
		result := p.result()
		require.NotNil(t, result)
		require.Equal(t, 2500, result.CacheReadInputTokens) // round(10000*0.25), not *0.9
		require.Zero(t, result.CacheCreationInputTokens, "the read fallback never emits a cache-creation estimate from the creation ratio")
		p.complete(kiropkg.Usage{}, false)
	})
}

// TestKiroDynamicStaticFallbackNoFallbackWithoutProvenMatch checks every
// "not yet trustworthy" condition the task calls out explicitly: none of
// them may report a fallback estimate, even with an aggressive static ratio
// configured that would obviously produce a nonzero value if it fired.
func TestKiroDynamicStaticFallbackNoFallbackWithoutProvenMatch(t *testing.T) {
	const aggressiveRatio = 1.0 // if the fallback fired here, it would be maximally visible

	t.Run("truePrefixMiss", func(t *testing.T) {
		a, g, _ := dynamicSetup(t)
		g.KiroCacheEmulationRatio = aggressiveRatio
		// Warm one conversation, then present completely unrelated content
		// under the same account/model -- a genuine prefix miss, not a mere
		// scope-key change.
		trained := dynamicRequest(t, "claude-opus-5", "prefix-miss-trained")
		p := dynamicPrepare(t, a, g, trained)
		p.complete(dynamicUsage(t, 10, 20), true)
		unrelated := mutateDynamicRequest(t, trained, func(payload *kiropkg.KiroPayload) {
			// Large enough to stay above the (opus) 4096-token minimum
			// cacheable threshold -- otherwise the profile would have no
			// cacheable breakpoint at all and prepareKiroDynamicCache would
			// return no plan whatsoever, which is a different case
			// (invalidUnparseableRequest/ineligible) than the true prefix
			// miss this subtest targets.
			payload.ConversationState.History[0].UserInputMessage.Content = strings.Repeat("completely unrelated history ", 2500)
			payload.ConversationState.CurrentMessage.UserInputMessage.Content = "completely different current turn"
		})
		p = dynamicPrepare(t, a, g, unrelated)
		require.Zero(t, p.matched, "genuinely different content must never match")
		require.Nil(t, p.result())
		p.complete(kiropkg.Usage{}, false)
	})

	t.Run("scopeChange", func(t *testing.T) {
		a, g, _ := dynamicSetup(t)
		g.KiroCacheEmulationRatio = aggressiveRatio
		r := dynamicRequest(t, "claude-opus-5", "scope-change")
		p := dynamicPrepare(t, a, g, r)
		p.complete(dynamicUsage(t, 10, 20), true)
		other := &Account{ID: a.ID + 1, Platform: PlatformKiro, Type: AccountTypeOAuth}
		p = dynamicPrepare(t, other, g, r)
		require.Zero(t, p.matched, "a different account must never inherit another account's warmth")
		require.Nil(t, p.result())
		p.complete(kiropkg.Usage{}, false)
	})

	t.Run("expiredPrefixTTL", func(t *testing.T) {
		a, g, now := dynamicSetup(t)
		g.KiroCacheEmulationRatio = aggressiveRatio
		r := dynamicRequest(t, "claude-opus-5", "expired-ttl")
		p := dynamicPrepare(t, a, g, r)
		p.complete(dynamicUsage(t, 10, 20), true)
		*now = now.Add(kiroDynamicTTL("claude-opus-5") + time.Second) // past the 5-minute Claude TTL
		p = dynamicPrepare(t, a, g, r)
		require.Zero(t, p.matched, "an expired prefix must never be reported as matched")
		require.Nil(t, p.result())
		p.complete(kiropkg.Usage{}, false)
	})

	t.Run("failedAbortedResponse", func(t *testing.T) {
		a, g, _ := dynamicSetup(t)
		g.KiroCacheEmulationRatio = aggressiveRatio
		r := dynamicRequest(t, "claude-opus-5", "failed-response")
		p := dynamicPrepare(t, a, g, r)
		p.complete(kiropkg.Usage{}, false) // failed: never warms the scope
		p = dynamicPrepare(t, a, g, r)
		require.Zero(t, p.matched, "a failed/aborted response must never contribute warmth")
		require.Nil(t, p.result())
		p.complete(kiropkg.Usage{}, false)
		// Contrast: an actually successful completion of the same content
		// does warm the scope, and the fallback then fires on the next call.
		p = dynamicPrepare(t, a, g, r)
		require.Zero(t, p.matched)
		p.complete(dynamicUsage(t, 10, 20), true)
		p = dynamicPrepare(t, a, g, r)
		require.Greater(t, p.matched, 0)
		require.NotNil(t, p.result(), "a genuinely successful completion must warm the scope for the fallback")
		p.complete(kiropkg.Usage{}, false)
	})

	t.Run("invalidUnparseableRequest", func(t *testing.T) {
		a, g, _ := dynamicSetup(t)
		g.KiroCacheEmulationRatio = aggressiveRatio
		bad := kiropkg.KiroRequestContext{CachePayload: []byte("not json"), CacheEndpoint: "https://native.test/generate"}
		require.Nil(t, prepareKiroDynamicCache(a, g, bad, 10000), "an unparseable request must return no plan at all, not merely no estimate")
	})

	t.Run("disabledGroup", func(t *testing.T) {
		a, g, _ := dynamicSetup(t)
		g.KiroCacheEmulationRatio = aggressiveRatio
		g.KiroCacheEmulationEnabled = false
		r := dynamicRequest(t, "claude-opus-5", "disabled-group")
		require.Nil(t, prepareKiroDynamicCache(a, g, r, 10000), "a disabled group must return no plan at all, not merely no estimate")
	})
}
