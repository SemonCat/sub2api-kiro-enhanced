# Upstream sync

Follow the self-contained workflow in [README](../README.md#periodic-upstream-sync). Run commands from the repository root. Do not change remotes as part of synchronization.

## Accepted source revisions

Update this ledger and the README example only after integration, tests, and review succeed.

| Source | Initial accepted revision |
| --- | --- |
| Official `upstream/main` | `ab99d56e9626e6cd731592dae8553c9758a0efa2` |
| Kiro `kiro-upstream/main` | `5d4ea03887ea219ea77ab447d59efc0c02a816ef` |
| Imported downstream snapshot (provenance only) | `94d954374fee9e3fca624b690ab6fbec8eae82c9` |

Use a unique integration branch for each cycle. The README's three-way patch imports only the changes since the last accepted Kiro revision; replaying the entire Kiro tree would overwrite downstream work. Fetching objects does not make Kiro history an ancestor of the downstream branch. If the Kiro source rewrites history, stop when the ancestry check fails and determine a reviewed replacement range.

A three-way application can leave staged changes and conflict markers. Inspect `git status`, resolve files individually, and stage resolved paths explicitly. Inspect both `git diff` and `git diff --cached`, including newly added files. If abandoning an integration, leave its branch/worktree intact for inspection; do not discard work with a hard reset or force-update a branch.

Compare official changes from the ledger revision to `upstream/main` as well as `HEAD..upstream/main`. Assess whether Kiro already incorporates them. If a direct official update is needed, finish the Kiro integration first, then use a separate clean branch and `git merge --no-ff --no-commit upstream/main`. Review conflicts, run checks, and commit only after approval through the project's normal review process. This adds official ancestry only. Record the actual accepted official revision, not merely the latest fetched one.

## Validation

Use the Go version required by `backend/go.mod` and pnpm 9 as used by CI. A focused starting point is:

```sh
(cd backend && go test -tags=unit ./internal/pkg/kiro ./internal/pkg/apicompat ./internal/service ./internal/handler ./internal/handler/admin ./internal/handler/dto -run 'Kiro|AnthropicToResponses|ResponsesNamespace|EmptyCompleted' -count=1)
(cd frontend && pnpm install --frozen-lockfile --ignore-scripts)
(cd frontend && pnpm test:run src/composables/__tests__/useKiroOAuth.spec.ts src/i18n/__tests__/forkLocalePreservation.spec.ts src/views/admin/__tests__/groupsKiroCacheMode.spec.ts)
(cd frontend && pnpm build)
git diff --check
git diff --cached --check
```

Broaden to backend normal/unit suites and frontend tests when shared code changes. Regenerate Ent/Wire when their sources change and review generated output. Migrations require database-backed checks; routing changes require failover, cancellation, and streaming coverage. Review cache scoping/TTL and cold/warm behavior across Anthropic, Chat, and Responses. Verify that non-auth 403s retain their meaning and failed attempts never teach the estimator. Inspect public files and binary assets for private data and run a secret scanner before the integration commit. Record exact blockers rather than claiming unrun checks passed.

Commit source changes, intentional public documentation, and the revision ledger together after review. Publication and release are separate actions.
