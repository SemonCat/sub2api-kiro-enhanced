**[English](README.md)** | [繁體中文](README_TW.md) | [简体中文](README_CN.md)

# sub2api-kiro-enhanced

sub2api-kiro-enhanced is an AI API gateway with Kiro support, derived from [Wei-Shaw/sub2api](https://github.com/Wei-Shaw/sub2api) and [nianzs/sub2api's Kiro implementation](https://github.com/nianzs/sub2api). This downstream is maintained independently. It retains the upstream gateway and Kiro adapters, extends auth-surface routing and protocol compatibility, and replaces fixed-ratio Kiro cache simulation with estimates calibrated from native credits.

## What this downstream includes

The inherited, extended, and added labels refer to the pinned Kiro upstream revision `5d4ea03887ea219ea77ab447d59efc0c02a816ef`.

- **Auth-surface routing and failover (extended):** for IAM Identity Center and external IdP tokens, auto mode tries CodeWhisperer, then Amazon Q, then Kiro Runtime. The router rebuilds endpoint/profile payloads for each surface and after token refresh. An auth-surface mismatch can trigger fallback. Model, quota, and policy errors retain their classification; a 403 does not always mean the account is invalid. API-key accounts stay on Amazon Q.
- **Stable conversations and sticky accounts (inherited):** Kiro upstream supplies the deterministic conversation IDs and sticky account selection used to keep turns in the same conversation.
- **API compatibility (inherited and extended):** Kiro adapters support OpenAI Responses, Chat Completions, and Anthropic Messages. This downstream fixes Responses stream wire/namespace handling and adds cache-accounting test coverage for buffered and streaming responses across all three protocols. These adapters translate between protocols; complete native API equivalence is not guaranteed.
- **Native-credit-calibrated cache estimation (added):** the estimator replaces fixed-ratio simulation with conservative reuse estimates learned from successful native Kiro metering and comparable cold/warm observations. Native credits are recorded separately. Scope and TTL are defined below.
- **Failure and trace hardening (extended):** the estimator excludes observations with malformed framing/CRC, malformed events, failed or incomplete streams, missing metering, or failed client writes. It isolates fallback attempts and web-search generations and avoids double-counting fully cached input. Kiro event traces record types and sizes without prompt/reasoning excerpts.
- **Empty-response guards (added and inherited):** the Anthropic-to-Responses stream adapter emits `response.failed` with `upstream_empty_response` when completion has neither output items nor output tokens. The inherited native OpenAI Responses guard treats completion without semantic output or usage as a failover error before an empty success reaches the client.
- **Build/release fixes (inherited):** the Docker dependency-install and legal-document build context fixes come from Kiro upstream, along with the version-resolution scripts and release workflow. That workflow does not automatically commit version files.

[Downstream changes](docs/DOWNSTREAM_CHANGES.md) documents the implementation and regression tests.

## Architecture and scope

The Go/Gin/Ent backend handles gateway routing and protocol translation; the Vue/TypeScript frontend provides administration. PostgreSQL stores persistent data and Redis supports shared runtime state. See [backend](backend/), [frontend](frontend/), and [deployment files](deploy/).

Kiro and cache internals remain inside sub2api. External OpenAI/Anthropic response schemas and field sets are unchanged. sub2api excludes internal estimator metadata from public output and reports estimates through existing usage fields.

The estimator hashes the request semantics visible to the model after translation, preserving their order. Observations are scoped by account/principal, profile, endpoint, model, and inference shape. OAuth token rotation alone does not change account identity.

| Eligible model ID | Reuse window (TTL) | Additional scope |
| --- | --- | --- |
| `claude-opus-5` | 5 minutes | Common scope above |
| `gpt-5.6-terra` | 30 minutes | Also includes native conversation ID |

These reuse windows describe the estimator’s policy and do not guarantee provider cache retention. Other models are ineligible. Cache-token counts are sub2api estimates; they are not provider-reported measurements. The estimator assigns no savings without sufficient evidence. The bounded in-memory tracker loses calibration on restart.

## Quick start and deployment

Build this checkout to run the Kiro changes described above. Use the Go version in [backend/go.mod](backend/go.mod) (currently `1.27.0`), Node.js 20 as used by CI, pnpm 9, PostgreSQL 15+, and Redis 7+. From the repository root:

```sh
pnpm --dir frontend install --frozen-lockfile --ignore-scripts
pnpm --dir frontend run build
cd backend
VERSION="$(./scripts/resolve-version.sh)"
go build -tags embed -ldflags="-X main.Version=${VERSION}" -o sub2api ./cmd/server
./sub2api
```

For a fresh source installation, use the first-run wizard at `http://localhost:8080` to configure the database, Redis, and administrator account. Do not pre-create `config.yaml`: the server skips the wizard when a configuration exists, and the legacy `default.admin_email` / `default.admin_password` fields do not create an administrator.

For containers, service operation, and backups, see the [deployment guide](deploy/README.md), [Dockerfile](Dockerfile), [local-directory Compose file](deploy/docker-compose.local.yml), and [named-volume Compose file](deploy/docker-compose.yml). Existing image references, download scripts, and release URLs still point to upstream publishers. Do not assume they provide a downstream release. Build this checkout and configure the deployment to use your build.

## Configuration caveats

- Enable or disable Kiro estimation with the group’s cache-emulation switch. Legacy ratio/mode fields remain accepted for compatibility but do not set inferred savings. For current cache semantics, use [Downstream changes](docs/DOWNSTREAM_CHANGES.md) alongside older deployment notes.
- Set and securely retain `JWT_SECRET`, `TOTP_ENCRYPTION_KEY`, and `POSTGRES_PASSWORD` when using the Compose deployment. Stable JWT/TOTP keys preserve sessions and access to stored two-factor authentication secrets across restarts.
- Review [the example configuration](deploy/config.example.yaml) and [environment template](deploy/.env.example) before deployment. The Docker build defaults to public dependency mirrors; review or override its existing build arguments for your environment.

## Remotes and lineage

| Remote | Source | Role |
| --- | --- | --- |
| `upstream` | `https://github.com/Wei-Shaw/sub2api.git` | Official project; compare every sync cycle |
| `kiro-upstream` | `https://github.com/nianzs/sub2api.git` | Primary source for periodic Kiro updates |
| `origin` | `https://github.com/SemonCat/sub2api-kiro-enhanced.git` | This downstream; use your own destination for a fork |

The initial public tree starts at official commit `ab99d56e9626e6cd731592dae8553c9758a0efa2` and imports a file delta from downstream snapshot `94d954374fee9e3fca624b690ab6fbec8eae82c9`, incorporating Kiro upstream `5d4ea03887ea219ea77ab447d59efc0c02a816ef`. The import retains only official Git ancestry and excludes earlier downstream history. Snapshot IDs record provenance and need not exist as commits in this repository. Public documentation replaces internal task artifacts. The Go module remains `github.com/Wei-Shaw/sub2api` for compatibility.

## Periodic upstream sync

Start from reviewed, committed work with an empty `git status --porcelain`. Run from the repository root, verify remote URLs, and use a unique integration branch (replace `YYYYMMDD`). Fetch and compare both sources even when Kiro is the primary update source:

```sh
git remote -v
git fetch upstream
git fetch kiro-upstream
git switch -c sync/kiro-YYYYMMDD
kiro_before=5d4ea03887ea219ea77ab447d59efc0c02a816ef
kiro_after=$(git rev-parse kiro-upstream/main)
git log --oneline "$kiro_before..$kiro_after"
git diff --stat "$kiro_before" "$kiro_after"
git log --oneline HEAD..upstream/main
git diff --stat upstream/main HEAD
```

On later cycles, replace `kiro_before` with the last successfully integrated Kiro revision. Also compare official changes from the accepted revision in the [sync ledger](docs/UPSTREAM_SYNC.md) to `upstream/main`. To preserve official-only ancestry, import the incremental Kiro file delta rather than merging Kiro history:

```sh
git merge-base --is-ancestor "$kiro_before" "$kiro_after"
```

Continue only after the ancestry check passes and you have reviewed the comparison.

```sh
kiro_patch=$(mktemp)
git diff --binary --full-index "$kiro_before" "$kiro_after" > "$kiro_patch"
git apply --3way "$kiro_patch"
```

Stop on any error or conflict. Resolve each file against both upstream intent and downstream behavior; do not blanket-select a side.

Review new files for private content, inspect staged and unstaged changes, and run focused Kiro/compatibility/gateway tests, frontend regressions, and the frontend build. Broaden checks for migrations, generated code, or shared routing changes.

Obtain review before committing the integration and updating accepted revisions. Pushing and releasing are separate actions. Official changes not yet carried by Kiro need a separate reviewed integration; the [sync guide](docs/UPSTREAM_SYNC.md) covers conflict handling and validation.

## Development and verification

From the repository root, after installing the prerequisites above:

```sh
pnpm --dir frontend install --frozen-lockfile --ignore-scripts
make build
make test-unit
make test
(cd backend && go test -tags=unit ./internal/pkg/kiro ./internal/pkg/apicompat ./internal/service ./internal/handler ./internal/handler/admin ./internal/handler/dto -run 'Kiro|AnthropicToResponses|ResponsesNamespace|EmptyCompleted' -count=1)
(cd frontend && pnpm test:run src/composables/__tests__/useKiroOAuth.spec.ts src/i18n/__tests__/forkLocalePreservation.spec.ts src/views/admin/__tests__/groupsKiroCacheMode.spec.ts)
git diff --check
git diff --cached --check
```

`make build` builds backend and frontend separately; use the quick-start build for a binary with an embedded UI.

`make test` runs backend tests and `golangci-lint`, then frontend lint, type checking, and the critical frontend test set. Install `golangci-lint` to run it.

For development, run `(cd backend && go run ./cmd/server)` and `(cd frontend && pnpm run dev)` in separate terminals. The [sync guide](docs/UPSTREAM_SYNC.md) lists additional checks.

## Security and privacy

Protect provider credentials, API keys, administrator access, database backups, and encryption keys. Use HTTPS and review URL allowlists and reverse-proxy settings before exposing the service; see [edge security guidance](deploy/EDGE_SECURITY.md). Review provider terms and applicable requirements before use.

Keep prompts, reasoning, tokens, cookies, personal data, internal hosts, and deployment details out of public issues, logs, screenshots, and commits. Other logs may contain sensitive data even though Kiro event traces record only types and sizes. Redact diagnostic material before sharing. Review diffs and scan for secrets before publication.

## License and attribution

Licensed under the [GNU Lesser General Public License v3.0 or later](LICENSE). Retained upstream attribution: Copyright (c) 2026 Wesley Liddick.

Thanks to [Wei-Shaw/sub2api](https://github.com/Wei-Shaw/sub2api) and [nianzs/sub2api](https://github.com/nianzs/sub2api) and their contributors. Refer to those projects for upstream documentation and acknowledgements; this downstream is maintained independently.
