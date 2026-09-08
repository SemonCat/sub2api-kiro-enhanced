package service

import (
	"strconv"
	"testing"

	kiropkg "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/stretchr/testify/require"
)

// kiroGPTRealTrafficPair is one measured (cold, warm) native-credit point
// from an authorized side-by-side comparison of kiro-cli direct traffic
// against the official Codex CLI ("Luna") at a fixed native input-token
// scale. Cold and WarmAvg are the RAW native Kiro credits reported for a
// single call at that scale -- not pre-divided densities: Cold is the cold
// (matched==0) call's reported credits and WarmAvg is the mean of the
// repeated warm calls' reported credits against the same warmed prefix.
// This is the calibration source for kiroGPTCachedMarginalCreditRatio
// (d=0.523): WarmAvg/Cold at these three scales runs ~53-55%, consistent
// with d, while the official client's own reported cached-input rate for
// this same traffic was ~95-98%, independent of Kiro's own credit metering.
type kiroGPTRealTrafficPair struct {
	Tokens  int
	Cold    float64
	WarmAvg float64
}

// kiroGPTRealTrafficPairs holds the three measured points, at native input
// scales of approximately 21k, 40k, and 62k tokens.
var kiroGPTRealTrafficPairs = []kiroGPTRealTrafficPair{
	{Tokens: 21000, Cold: 0.004629, WarmAvg: 0.002568},
	{Tokens: 40000, Cold: 0.010715, WarmAvg: 0.005819},
	{Tokens: 62000, Cold: 0.017587, WarmAvg: 0.009349},
}

// TestKiroDynamicGPTRealTrafficCalibration replays each measured cold/warm
// credit pair above through the real estimator (prepareKiroDynamicCache /
// complete). The estimator itself divides each call's raw credits by that
// call's own native total token count (see kiroDynamicSample.coldDensity) to
// form the density it actually calibrates from; since the cold and warm
// calls at one scale share the same native input token count and the same
// explicit, nonzero output token count, that shared total cancels out of the
// resulting (cold-warm)/cold savings ratio, so feeding the raw measured
// credits in directly (rather than reconstructing a density) reproduces the
// exact same calibration this fixture represents.
//
// It proves the exact regression this change targets: before this fix, the
// estimator reported creditSavings itself (s, ~44-47% for these three
// points) as the cached-token fraction, silently capping GPT structural
// reuse near that range even though the official client's own reported
// cached-input rate for the same traffic was ~95-98%. After the
// s -> r = s/(1-d) conversion, the same three points saturate near the full
// matched-prefix bound instead.
func TestKiroDynamicGPTRealTrafficCalibration(t *testing.T) {
	const staticRatio = 0.5 // deliberately not 1.0 (dynamicSetup's default) and
	// not close to the dynamic outcome, so the static-fallback-vs-dynamic
	// transition below is an observably different number either way.
	const output = 50 // explicit, nonzero: declared output tokens are used
	// as-is (see translator.go's OutputTokens==0 backfill) rather than
	// backfilled from response text, and its exact value does not affect the
	// savings ratio below since it is identical for the cold and warm calls
	// at each scale.

	for _, pair := range kiroGPTRealTrafficPairs {
		t.Run(strconv.Itoa(pair.Tokens), func(t *testing.T) {
			a, g, _ := dynamicSetup(t)
			g.KiroCacheEmulationMode = KiroCacheEmulationModeUniform
			g.KiroCacheEmulationRatio = staticRatio
			r := dynamicRequest(t, "gpt-5.6-luna", "gpt-real-traffic-"+strconv.Itoa(pair.Tokens))

			// Cold baseline (matched==0): records the sample, reports nothing.
			p := prepareKiroDynamicCache(a, g, r, pair.Tokens)
			require.NotNil(t, p)
			require.Zero(t, p.matched)
			require.Nil(t, p.result())
			p.complete(dynamicUsageWithInput(t, pair.Cold, pair.Tokens, output), true)

			// First two warm calls: the canonical prefix is now proven
			// (matched>0) but fewer than two dynamic pairs exist yet, so the
			// static pre-calibration fallback must govern exactly, per its
			// own bounds/rounding (scaleKiroCacheTokens).
			for i := 0; i < 2; i++ {
				p = prepareKiroDynamicCache(a, g, r, pair.Tokens)
				require.NotNil(t, p)
				require.Greater(t, p.matched, 0)
				require.Less(t, len(p.scope.pairs), 2, "call %d", i)
				result := p.result()
				require.NotNil(t, result, "call %d", i)
				require.Equal(t, scaleKiroCacheTokens(p.matched, staticRatio), result.CacheReadInputTokens, "call %d", i)
				p.complete(dynamicUsageWithInput(t, pair.WarmAvg, pair.Tokens, output), true)
			}
			require.Len(t, p.scope.pairs, 2, "two valid pairs must exist before dynamic calibration governs")

			expectedSavings := (pair.Cold - pair.WarmAvg) / pair.Cold
			require.Greater(t, expectedSavings, 0.40, "sanity: fixture should sit in the ~45-53%% raw-savings range the pre-fix estimator was pinned to")
			require.Less(t, expectedSavings, 0.53)
			expectedReuse := kiroGPTInferredReuse(expectedSavings)

			// Third call: dynamic calibration (>=2 pairs) now governs,
			// including the case of a legitimate zero -- it is simply not
			// zero for this fixture, since the underlying traffic is
			// genuinely near-fully reused.
			p = prepareKiroDynamicCache(a, g, r, pair.Tokens)
			require.NotNil(t, p)
			require.Len(t, p.scope.pairs, 2)
			result := p.result()
			require.NotNil(t, result)
			require.InDelta(t, expectedReuse, float64(result.CacheReadInputTokens)/float64(p.matched), 1e-3)
			require.NotEqual(t, scaleKiroCacheTokens(p.matched, staticRatio), result.CacheReadInputTokens,
				"dynamic calibration must govern here, not the static fallback")

			// The regression this change fixes: GPT structural reuse must no
			// longer be pinned at the raw ~45-53%% credit-savings figure --
			// it must saturate up near the matched-prefix limit instead.
			observedRatio := float64(result.CacheReadInputTokens) / float64(p.matched)
			require.Greater(t, observedRatio, 0.53, "must exceed the raw credit-savings range this fix removes")
			require.LessOrEqual(t, observedRatio, 1.0+1e-9)
			require.Greater(t, observedRatio, 0.85, "should saturate near the matched-prefix limit for this near-full-reuse fixture")
			p.complete(kiropkg.Usage{}, false)
		})
	}
}

// TestKiroDynamicGPTRealTrafficZeroSavingsIsLegitimateZero confirms the other
// half of "including legitimate zero": a GPT-family scope whose cold and warm
// calls report identical credits (no measured savings at all) must report a
// real zero from dynamic calibration once >=2 pairs exist, not fall back to
// a nonzero static estimate.
func TestKiroDynamicGPTRealTrafficZeroSavingsIsLegitimateZero(t *testing.T) {
	a, g, _ := dynamicSetup(t)
	g.KiroCacheEmulationMode = KiroCacheEmulationModeUniform
	g.KiroCacheEmulationRatio = 0.5
	r := dynamicRequest(t, "gpt-5.6-luna", "gpt-zero-savings")
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
