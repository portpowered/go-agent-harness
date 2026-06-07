# Phase 1 workspace-validation baseline reconciliation

## Sources consulted

This comparison used the three Phase 1 baseline sources named in the PRD:

1. Git branch `phase-1-workspace-validation-entrypoints`
2. Saved worktree `.claude/worktrees/phase-1-workspace-validation-entrypoints`
3. Remote branch `origin/main`

The branch, saved worktree, and `origin/main` agree on the root baseline artifacts in scope for this PRD.

## Baseline scope and expected behavior

Phase 1 baseline scope for the authoritative workspace:

- committed root `go.work` and `go.work.sum`
- root `Makefile` validation targets, including `ci`
- `.github/workflows/ci.yml` delegating to root `make` targets
- `docs/architecture/workspace.md`
- deterministic validation wiring that keeps default root validation free of live provider credentials
- directly required supporting fixes needed for the root validation surface to typecheck and run

The reconciliation must preserve unrelated current work while restoring that root validation surface.

## Comparison summary

| Artifact or behavior | `phase-1-workspace-validation-entrypoints` | saved worktree | `origin/main` | active branch |
| --- | --- | --- | --- | --- |
| Root `go.work` / `go.work.sum` | present | present | present | missing |
| Root `Makefile` with `ci` pipeline | present | present | present | missing |
| `.github/workflows/ci.yml` delegating to `make ci` | present | present | present | missing |
| `docs/architecture/workspace.md` | present | present | present | missing |
| Deterministic validation tiers (`test`, `test-integration`, `test-regressions`, opt-in customer sessions) | documented and wired | documented and wired | documented and wired | missing at root |
| Darwin shell process helpers for `agent-cli` tool execution | present | present | present | missing before this iteration |
| `agent-cli` module metadata needed for local `go test ./...` | stabilized | stabilized | stabilized | required `go mod tidy` before this iteration |

### Missing artifacts

The active branch is missing every in-scope root artifact from the landed Phase 1 baseline:

- `go.work`
- `go.work.sum`
- `Makefile`
- `.github/workflows/ci.yml`
- `docs/architecture/workspace.md`

### Conflicting or merge-sensitive areas

These areas should be merged intentionally instead of copied wholesale:

- `agent-cli`, `go-agent-loop`, and `go-llm-gateway` contain current-workspace changes that are outside the root-artifact story and should remain authoritative unless a specific baseline fix is required.
- `factory/factory.json` and `.gitignore` differ from `phase-1-workspace-validation-entrypoints`; those differences are outside the narrow root validation baseline unless a later mergeability fix proves otherwise.
- Supporting fixes must be ported surgically. On this host, `agent-cli` needed the Darwin shell helper and tidy module metadata before `go build ./...` could typecheck cleanly.

### Already-matching artifacts

None of the in-scope root baseline artifacts already match in the active branch because each one is absent. The three consulted baseline sources do match each other for those root artifacts, which means the landing source of truth is stable.

## Landing plan

1. Restore the committed root workspace artifacts from the agreed baseline source: `go.work`, `go.work.sum`, root `Makefile`, `.github/workflows/ci.yml`, and `docs/architecture/workspace.md`.
2. Port the directly required supporting fixes that make the root validation surface deterministic on the current workspace, without broad cleanup.
3. Resolve any in-scope conflicts by favoring the authoritative current branch for unrelated work and documenting each intentional deviation from the landed baseline.
4. Prove convergence by running root `make ci` from this active workspace and recording the command and result.

## Preservation rule

Unrelated local main changes must be preserved during reconciliation. The existence of non-baseline differences in module code and factory configuration is not a license to overwrite those trees from the saved worktree or `origin/main`.

## Validation snapshot for this story

- `go build ./...` passed in `agent-cli`
- `go build ./...` passed in `go-agent-loop`
- `go build ./...` passed in `go-llm-gateway`
- `go test ./...` already passes in `go-agent-loop` and `go-llm-gateway`
- `agent-cli` broad integration tests still fail without the later deterministic-validation wiring already present in the saved baseline; that remains story-003 scope rather than a blocker on this story's typecheck acceptance
