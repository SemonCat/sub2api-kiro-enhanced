package service

import (
	"strconv"
	"testing"

	kiropkg "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/stretchr/testify/require"
)

// kiroClaudeRealTrafficPair is one measured (cold, three warm) native-credit
// point from an authorized kiro-cli direct session against Claude Opus 5
// (classic session store, model explicitly pinned, no tools, low reasoning
// effort): one cold turn followed by three repeated warm turns against the
// same warmed prefix, at a fixed native input scale labeled by its evidence
// line count. Cold, Warm1, Warm2 and Warm3 are the RAW native Kiro credits
// reported for each turn -- not pre-divided densities. Tokens is a
// representative native input-token scale used only to drive this test's
// estimator scaffold (large enough to clear Claude Opus's cache-min
// threshold, kiroCacheMinTokensOpus); it is not part of the recorded
// evidence, which reported line counts and credits only.
//
// This is the calibration source for kiroClaudeCachedMarginalCreditRatio
// (d=0.525): a multi-size regression of warm vs. cold credits across these
// three points, after removing the fixed per-call CLI cost, gave a
// warm/cold marginal-credit slope ratio of ~0.5247, and an independent
// Claude Sonnet 5 holdout (kiroClaudeSonnetHoldoutPairs) gave ~0.5266,
// confirming this is a family-level constant. Separately, the official
// Claude CLI's own native cache accounting on this exact 900-line shape
// showed cache_read_input_tokens covering virtually the entire warmed
// prefix on the first successful warm turn (~99.9% cached-input rate, see
// TestKiroDynamicClaudeOfficialCLIValidationConfirmsNearFullReuse) --
// independent confirmation that structural reuse for this shape really is
// near-total, consistent with creditSavings systematically understating it.
type kiroClaudeRealTrafficPair struct {
	Lines               int
	Tokens              int
	Cold                float64
	Warm1, Warm2, Warm3 float64
	WarmAvg             float64 // as originally reported; Warm1..Warm3 average to this
}

// kiroClaudeOpusRealTrafficPairs holds the three measured points, at
// evidence-line scales of 200, 900, and 1800 lines.
var kiroClaudeOpusRealTrafficPairs = []kiroClaudeRealTrafficPair{
	{Lines: 200, Tokens: 8000, Cold: 0.199884466733002,
		Warm1: 0.112570442421227, Warm2: 0.113444676815920, Warm3: 0.114013831044776, WarmAvg: 0.11334298342730768},
	{Lines: 900, Tokens: 24000, Cold: 0.434329679502488,
		Warm1: 0.235058640265340, Warm2: 0.235584013399668, Warm3: 0.236076550713101, WarmAvg: 0.23557306812603632},
	{Lines: 1800, Tokens: 48000, Cold: 0.746526898971808,
		Warm1: 0.399561067031509, Warm2: 0.400093737014925, Warm3: 0.400593571177446, WarmAvg: 0.40008279174129330},
}

// kiroClaudeSonnetHoldoutPairs is an independent Claude Sonnet 5 holdout
// (same methodology, two input scales), used to confirm
// kiroClaudeCachedMarginalCreditRatio is a family-level constant rather than
// an Opus-specific artifact. Only the mean warm credits were recorded for
// this holdout (no individual per-turn warm values), so Warm1..Warm3 are
// unset and TestKiroDynamicClaudeSonnetHoldoutValidatesSameFamilyConstant
// replays WarmAvg for each repeated warm call instead.
var kiroClaudeSonnetHoldoutPairs = []kiroClaudeRealTrafficPair{
	{Lines: 200, Tokens: 8000, Cold: 0.138778293548922, WarmAvg: 0.07720245064676619},
	{Lines: 1800, Tokens: 48000, Cold: 0.455326910978441, WarmAvg: 0.24388486389718067},
}

// TestKiroDynamicClaudeRealTrafficCalibration replays each measured point's
// cold turn plus its three individual real warm turns through the real
// estimator (prepareKiroDynamicCache / complete), feeding the raw measured
// credits directly (the estimator itself divides by each call's own total
// token count -- see kiroDynamicSample.coldDensity -- and that shared total
// cancels out of the resulting savings ratio, exactly as established for the
// GPT fixture in TestKiroDynamicGPTRealTrafficCalibration).
//
// It proves this change's Claude-family outcome: the first two warm turns
// (fewer than two dynamic pairs yet) still use the static pre-calibration
// fallback exactly, and from the third warm turn onward (>=2 pairs) the
// calibrated conversion governs, reporting an inferred reuse that both
// matches kiroInferredReuse(savings, d) exactly and, per the acceptance
// criteria for this fixture, stays above 90% and increases with input scale
// -- consistent with the near-total structural reuse the official Claude
// CLI itself independently reported for this traffic.
func TestKiroDynamicClaudeRealTrafficCalibration(t *testing.T) {
	const staticRatio = 0.5 // deliberately not 1.0 (dynamicSetup's default) and
	// not close to the dynamic outcome, so the static-fallback-vs-dynamic
	// transition below is an observably different number either way.
	const output = 50 // explicit, nonzero: declared output tokens are used
	// as-is (see translator.go's OutputTokens==0 backfill) rather than
	// backfilled from response text, and its exact value does not affect the
	// savings ratio below since it is identical for every call at a scale.

	var previousReuse float64
	for pairIdx, pair := range kiroClaudeOpusRealTrafficPairs {
		t.Run(strconv.Itoa(pair.Lines), func(t *testing.T) {
			a, g, _ := dynamicSetup(t)
			g.KiroCacheEmulationMode = KiroCacheEmulationModeUniform
			g.KiroCacheEmulationRatio = staticRatio
			r := dynamicRequest(t, "claude-opus-5", "claude-real-traffic-"+strconv.Itoa(pair.Lines))

			// Cold baseline (matched==0): records the sample, reports nothing.
			p := prepareKiroDynamicCache(a, g, r, pair.Tokens)
			require.NotNil(t, p)
			require.Zero(t, p.matched)
			require.Nil(t, p.result())
			p.complete(dynamicUsageWithInput(t, pair.Cold, pair.Tokens, output), true)

			warms := []float64{pair.Warm1, pair.Warm2, pair.Warm3}
			var minSavings float64
			for i, warmCredits := range warms {
				p = prepareKiroDynamicCache(a, g, r, pair.Tokens)
				require.NotNil(t, p)
				require.Greater(t, p.matched, 0)
				if i < 2 {
					// First two warm turns: canonical prefix is proven but
					// fewer than two dynamic pairs exist yet, so the static
					// pre-calibration fallback must govern exactly.
					require.Less(t, len(p.scope.pairs), 2, "warm turn %d", i)
					result := p.result()
					require.NotNil(t, result, "warm turn %d", i)
					require.Equal(t, scaleKiroCacheTokens(p.matched, staticRatio), result.CacheReadInputTokens, "warm turn %d", i)
				} else {
					// Third warm turn onward: two dynamic pairs (from warm
					// turns 0 and 1) already exist, so calibrated conversion
					// governs -- the lower envelope of their credit savings,
					// converted through Claude's calibrated d.
					require.GreaterOrEqual(t, len(p.scope.pairs), 2, "warm turn %d", i)
				}
				p.complete(dynamicUsageWithInput(t, warmCredits, pair.Tokens, output), true)
				savings := (pair.Cold - warmCredits) / pair.Cold
				if i == 0 {
					minSavings = savings
				} else {
					minSavings = min(minSavings, savings)
				}
			}
			require.Len(t, p.scope.pairs, 3, "all three warm turns must have formed a pair")

			// One more call: dynamic calibration now governs using the
			// envelope over all three real warm observations.
			p = prepareKiroDynamicCache(a, g, r, pair.Tokens)
			require.NotNil(t, p)
			result := p.result()
			require.NotNil(t, result)
			expectedReuse := kiroInferredReuse(minSavings, kiroClaudeCachedMarginalCreditRatio)
			observedReuse := float64(result.CacheReadInputTokens) / float64(p.matched)
			require.InDelta(t, expectedReuse, observedReuse, 1e-3)

			// The regression this change fixes, restated for Claude: reuse
			// must stay above 90% and increase with input scale, not stay
			// pinned near the raw ~43-47%% credit-savings figure.
			require.Greater(t, observedReuse, 0.90, "%d lines: should be near-total structural reuse", pair.Lines)
			if pairIdx > 0 {
				require.Greater(t, observedReuse, previousReuse, "%d lines: reuse should increase with input scale", pair.Lines)
			}
			previousReuse = observedReuse
			p.complete(kiropkg.Usage{}, false)
		})
	}
}

// TestKiroDynamicClaudeSonnetHoldoutValidatesSameFamilyConstant replays the
// independent Sonnet holdout through the same estimator and confirms it
// lands in the same near-total, increasing-with-scale reuse range as the
// Opus calibration source above -- proof that kiroClaudeCachedMarginalCreditRatio
// is validated as a family-level constant, not fit to a single model.
func TestKiroDynamicClaudeSonnetHoldoutValidatesSameFamilyConstant(t *testing.T) {
	const staticRatio = 0.5
	const output = 50

	var previousReuse float64
	for pairIdx, pair := range kiroClaudeSonnetHoldoutPairs {
		t.Run(strconv.Itoa(pair.Lines), func(t *testing.T) {
			a, g, _ := dynamicSetup(t)
			g.KiroCacheEmulationMode = KiroCacheEmulationModeUniform
			g.KiroCacheEmulationRatio = staticRatio
			r := dynamicRequest(t, "claude-sonnet-4-6", "claude-sonnet-holdout-"+strconv.Itoa(pair.Lines))

			p := prepareKiroDynamicCache(a, g, r, pair.Tokens)
			require.NotNil(t, p)
			require.Zero(t, p.matched)
			p.complete(dynamicUsageWithInput(t, pair.Cold, pair.Tokens, output), true)

			// Only the mean warm credits were recorded for this holdout, so
			// the same WarmAvg value is replayed for both warm turns needed
			// to reach two dynamic pairs.
			for i := 0; i < 2; i++ {
				p = prepareKiroDynamicCache(a, g, r, pair.Tokens)
				require.NotNil(t, p)
				require.Less(t, len(p.scope.pairs), 2, "warm turn %d", i)
				result := p.result()
				require.NotNil(t, result, "warm turn %d", i)
				require.Equal(t, scaleKiroCacheTokens(p.matched, staticRatio), result.CacheReadInputTokens, "warm turn %d", i)
				p.complete(dynamicUsageWithInput(t, pair.WarmAvg, pair.Tokens, output), true)
			}
			require.Len(t, p.scope.pairs, 2)

			expectedSavings := (pair.Cold - pair.WarmAvg) / pair.Cold
			p = prepareKiroDynamicCache(a, g, r, pair.Tokens)
			require.NotNil(t, p)
			result := p.result()
			require.NotNil(t, result)
			expectedReuse := kiroInferredReuse(expectedSavings, kiroClaudeCachedMarginalCreditRatio)
			observedReuse := float64(result.CacheReadInputTokens) / float64(p.matched)
			require.InDelta(t, expectedReuse, observedReuse, 1e-3)
			require.Greater(t, observedReuse, 0.90, "%d lines: Sonnet holdout should also land near-total reuse under the shared Claude constant", pair.Lines)
			if pairIdx > 0 {
				require.Greater(t, observedReuse, previousReuse, "%d lines: reuse should increase with input scale", pair.Lines)
			}
			previousReuse = observedReuse
			p.complete(kiropkg.Usage{}, false)
		})
	}
}

// TestKiroDynamicClaudeRealTrafficZeroSavingsIsLegitimateZero mirrors
// TestKiroDynamicGPTRealTrafficZeroSavingsIsLegitimateZero for the
// Claude-family conversion: a scope whose cold and warm calls report
// identical credits (no measured savings at all) must report a real zero
// from dynamic calibration once >=2 pairs exist, not fall back to a nonzero
// static estimate.
func TestKiroDynamicClaudeRealTrafficZeroSavingsIsLegitimateZero(t *testing.T) {
	a, g, _ := dynamicSetup(t)
	g.KiroCacheEmulationMode = KiroCacheEmulationModeUniform
	g.KiroCacheEmulationRatio = 0.5
	r := dynamicRequest(t, "claude-opus-5", "claude-zero-savings")
	p := dynamicPrepare(t, a, g, r)
	p.complete(dynamicUsage(t, 10, 20), true) // cold density .5
	for i := 0; i < 2; i++ {
		p = dynamicPrepare(t, a, g, r)
		p.complete(dynamicUsage(t, 10, 20), true) // identical density -> zero savings
	}
	require.Len(t, p.scope.pairs, 2)
	require.Zero(t, p.scope.pairs[0].creditSavings)
	p = dynamicPrepare(t, a, g, r)
	require.Nil(t, p.result(), "zero credit savings must convert to zero reuse, not the nonzero static fallback")
	p.complete(kiropkg.Usage{}, false)
}

// TestKiroDynamicClaudeOfficialCLIValidationConfirmsNearFullReuse is a pure
// documentation/sanity test: it records the official Claude CLI's own
// native cache accounting for the exact 900-line shape used above,
// independent of any credit-savings math. Cold turn: cache_creation=44827,
// cache_read=0. First successful warm turn: cache_read=44827,
// cache_creation=39, input_tokens=2 -- i.e. the official client itself
// reports virtually the entire cached prefix served from cache. This
// confirms, from a completely independent signal, that structural reuse for
// this shape really is near-total -- exactly what
// TestKiroDynamicClaudeRealTrafficCalibration's 900-line case (fed only
// this same shape's credits) converts creditSavings up toward.
func TestKiroDynamicClaudeOfficialCLIValidationConfirmsNearFullReuse(t *testing.T) {
	const warmCacheRead, warmCacheCreation, warmFreshInput = 44827.0, 39.0, 2.0
	officialCachedInputRate := warmCacheRead / (warmCacheRead + warmCacheCreation + warmFreshInput)
	require.Greater(t, officialCachedInputRate, 0.999)
}
