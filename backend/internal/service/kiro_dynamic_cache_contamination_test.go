package service

import (
	"testing"
	"time"

	kiropkg "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/stretchr/testify/require"
)

// Exercise prepare/complete with native metering fixtures, never seeded pairs.
// A request-local credit outlier must not become a scope-wide cache verdict.
func TestKiroDynamicCalibrationObservationOwnership(t *testing.T) {
	for _, model := range []string{"gpt-5.6-luna", "claude-opus-5"} {
		for _, anomaly := range []struct {
			name    string
			credits float64
		}{
			{"low-savings", 7.367}, {"zero-savings", 10},
		} {
			t.Run(model+"/"+anomaly.name, func(t *testing.T) {
				a, g, _ := dynamicSetup(t)
				g.KiroCacheEmulationRatio = .8
				source := dynamicRequest(t, model, "source")
				dynamicTrain(t, a, g, source)
				before := dynamicPrepare(t, a, g, source)
				require.Equal(t, 10000, before.matched)
				require.Equal(t, 10000, before.result().CacheReadInputTokens)
				before.complete(dynamicUsage(t, anomaly.credits, 20), true)

				probes := []struct {
					name    string
					account *Account
					request kiropkg.KiroRequestContext
					want    int
				}{
					{"same-lineage", a, source, 10000},
					{"other-shape", a, mutateDynamicRequest(t, source, func(p *kiropkg.KiroPayload) {
						p.InferenceConfig = &kiropkg.KiroInferenceConfig{MaxTokens: 2048}
					}), 8000},
				}
				// These requests have independently proven warmth but no calibration.
				// Claude shares the outer scope across conversations; its content lineage
				// must still own its credit evidence. GPT retains conversation isolation.
				session := mutateDynamicRequest(t, dynamicRequest(t, model, "other-session"), func(p *kiropkg.KiroPayload) {
					p.ConversationState.History[0].UserInputMessage.Content += " distinct history"
				})
				otherAccount := *a
				otherAccount.ID++
				for _, probe := range []struct {
					name    string
					account *Account
					request kiropkg.KiroRequestContext
				}{
					{"other-session", a, session}, {"other-account", &otherAccount, source},
				} {
					cold := dynamicPrepare(t, probe.account, g, probe.request)
					require.Zero(t, cold.matched)
					cold.complete(dynamicUsage(t, 10, 20), true)
					probes = append(probes, struct {
						name    string
						account *Account
						request kiropkg.KiroRequestContext
						want    int
					}{probe.name, probe.account, probe.request, 8000})
				}
				for _, probe := range probes {
					t.Run(probe.name, func(t *testing.T) {
						p := dynamicPrepare(t, probe.account, g, probe.request)
						defer p.complete(kiropkg.Usage{}, false)
						require.Equal(t, 10000, p.matched, "structural warmth is unchanged")
						got := 0
						if p.result() != nil {
							got = p.result().CacheReadInputTokens
						}
						require.Equal(t, probe.want, got, "one unrelated or uncorroborated pair must not suppress reuse")
					})
				}
			})
		}
	}
}

func TestKiroDynamicSingleZeroDoesNotPoisonGrowingPrefix(t *testing.T) {
	for _, model := range []string{"gpt-5.6-luna", "claude-opus-5"} {
		t.Run(model, func(t *testing.T) {
			a, g, now := dynamicSetup(t)
			r := dynamicRequest(t, model, "growing")
			dynamicTrain(t, a, g, r)
			p := dynamicPrepare(t, a, g, r)
			p.complete(dynamicUsage(t, 10, 20), true) // one zero-savings pair
			for turn := 4; turn <= 10; turn++ {
				*now = now.Add(time.Second)
				r = kiroRealTrafficGrow(t, r, "growing", turn)
				p = dynamicPrepare(t, a, g, r)
				require.GreaterOrEqual(t, p.matched, 9600, "turn %d structural ceiling", turn)
				require.Less(t, p.matched, 10000, "new turn is uncached")
				require.NotNil(t, p.result(), "turn %d", turn)
				require.Equal(t, p.matched, p.result().CacheReadInputTokens, "turn %d", turn)
				require.Equal(t, 10000-p.matched, p.result().InputTokens)
				p.complete(dynamicUsage(t, 2, 20), true)
			}
		})
	}
}

func TestKiroDynamicCalibrationPairsExpireIndependentlyOfColdBaseline(t *testing.T) {
	a, g, now := dynamicSetup(t)
	r := dynamicRequest(t, "gpt-5.6-luna", "expiry")
	p := dynamicPrepare(t, a, g, r)
	p.complete(dynamicUsage(t, 10, 20), true)
	*now = now.Add(time.Minute)
	for i := 0; i < 2; i++ {
		p = dynamicPrepare(t, a, g, r)
		p.complete(dynamicUsage(t, 2, 20), true)
	}
	*now = now.Add(29*time.Minute + time.Second)
	p = dynamicPrepare(t, a, g, r)
	require.Equal(t, 10000, p.result().CacheReadInputTokens, "pairs and warmth outlive the cold baseline")
	p.complete(dynamicUsage(t, 10, 20), true)
	require.Len(t, dynamicPairs(p), 2, "expired baseline cannot form a new pair")
	*now = now.Add(time.Minute)
	p = dynamicPrepare(t, a, g, r)
	require.Empty(t, p.scope.samples, "expired pairs release their sample slot even while warmth remains")
	require.Equal(t, 10000, p.matched)
	p.complete(kiropkg.Usage{}, false)
}

func TestKiroDynamicCalibrationWindowsAreLineageLocal(t *testing.T) {
	a, g, _ := dynamicSetup(t)
	g.KiroCacheEmulationRatio = .2
	source := kiroRealTrafficRequest(t, "source")
	dynamicTrain(t, a, g, source)
	other := kiroRealTrafficRequest(t, "other")
	for i := 0; i < 12; i++ {
		p := dynamicPrepare(t, a, g, other)
		p.complete(dynamicUsage(t, 10, 20), true)
	}
	p := dynamicPrepare(t, a, g, source)
	require.Len(t, p.scope.samples, 2)
	require.Len(t, dynamicPairs(p), 2, "another lineage cannot evict this calibration")
	require.Equal(t, 10000, p.result().CacheReadInputTokens)
	p.complete(kiropkg.Usage{}, false)
	p = dynamicPrepare(t, a, g, other)
	require.Len(t, dynamicPairs(p), kiroDynamicMaxPairs)
	require.Nil(t, p.result(), "repeated zero savings governs only its own lineage")
	p.complete(kiropkg.Usage{}, false)
}
