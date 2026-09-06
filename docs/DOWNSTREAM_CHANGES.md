# Downstream changes and evidence

The comparison target is Kiro upstream `5d4ea03887ea219ea77ab447d59efc0c02a816ef`, not an unpinned moving branch. [README](../README.md#what-this-downstream-includes) summarizes user-visible behavior and lineage.

| Area | Status versus pinned Kiro upstream | Implementation and regression evidence |
| --- | --- | --- |
| Auth surfaces and profile failover | Extended | `backend/internal/service/kiro_runtime.go`, `kiro_error_classifier.go`, `kiro_auth_surface_test.go`, `kiro_websearch_failover_test.go` |
| Stable conversation IDs and sticky routing | Preserved from Kiro upstream | `backend/internal/service/kiro_runtime.go` (`stableKiroConversationID`), `kiro_session_test.go`, `kiro_sticky_session_test.go` |
| Protocol compatibility | Kiro adapters preserved; Responses wire/namespace and cache integration extended | `backend/internal/pkg/apicompat/anthropic_to_responses_response.go`, `anthropic_to_responses_stream_test.go`, `responses_namespace_custom_child_test.go`; `backend/internal/service/kiro_dynamic_cache_integration_test.go` |
| Credit-based reuse estimation | Added; replaces fixed-ratio estimator | `backend/internal/service/kiro_dynamic_cache.go`, `kiro_dynamic_cache_test.go`, `kiro_dynamic_cache_integration_test.go` |
| Metering integrity and trace privacy | Hardened | `backend/internal/pkg/kiro/translator.go`, `translator_test.go`; `backend/internal/service/kiro_runtime_state_test.go`, `kiro_websearch_failover_test.go` |
| Empty Responses completion guard | Added to Anthropic-to-Responses adapter; native OpenAI guard preserved | `backend/internal/pkg/apicompat/anthropic_to_responses_response.go` (`makeResponsesTerminalEvent`), `anthropic_to_responses_stream_test.go`; `backend/internal/service/openai_silent_refusal.go`, `openai_gateway_responses_empty_completed_test.go` |
| Build and release | Preserved from Kiro upstream | `.dockerignore`, `Dockerfile`, `.github/workflows/release.yml`, `backend/scripts/resolve-version.sh`, `Makefile` |

## Cache semantics and limits

Cache usage is an estimate derived from native Kiro credit observations, not provider-reported cache-token counts. The group cache-emulation enable switch controls it. Historical ratio and mode configuration remains accepted for compatibility, but does not set the inferred savings.

The estimator hashes the ordered, translated model-visible request semantics and scopes observations to account/principal, profile, endpoint, model, and inference shape. OAuth token rotation does not itself change account identity. For `claude-opus-5`, the reuse window is five minutes; for `gpt-5.6-terra`, it is thirty minutes and includes the native conversation ID. Other models are currently ineligible. These values describe this implementation, not a provider service-level guarantee.

Learning requires successful, complete native metering observations and sufficient comparable cold/warm evidence. Invalid framing/CRC, malformed events, failures, incomplete streams, and failed client writes must not teach reuse. The in-memory tracker is bounded; a restart loses calibration. Savings remain conservative when evidence is unavailable. Native credits remain separately recorded, and internal estimator metadata is excluded from public protocol output. Local integration tests cover streaming and buffered responses for all three protocols and fallback/web-search behavior.

Build/release behavior is preserved from the imported snapshot. The Docker build defaults to public dependency mirrors; review or override build arguments for your environment. Upstream release URLs elsewhere in the repository retain their original destinations until a separate publication change is deliberately made.
