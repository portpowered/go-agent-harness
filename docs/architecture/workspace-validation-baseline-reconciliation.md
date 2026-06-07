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

| Artifact or behavior | `phase-1-workspace-validation-entrypoints` | saved worktree | `origin/main` | reconciled active branch |
| --- | --- | --- | --- | --- |
| Root `go.work` / `go.work.sum` | present | present | present | restored from baseline |
| Root `Makefile` with `ci` pipeline | present | present | present | restored from baseline |
| `.github/workflows/ci.yml` delegating to `make ci` | present | present | present | restored from baseline |
| `docs/architecture/workspace.md` | present | present | present | restored from baseline |
| Deterministic validation tiers (`test`, `test-integration`, `test-regressions`, opt-in customer sessions) | documented and wired | documented and wired | documented and wired | restored and passing |
| Darwin shell process helpers for `agent-cli` tool execution | present | present | present | restored from baseline |
| `agent-cli` module metadata needed for local `go test ./...` | stabilized | stabilized | stabilized | aligned for deterministic root validation |

### Restored baseline artifacts

The reconciled active branch now contains every in-scope root artifact from the landed Phase 1 baseline:

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

The three consulted baseline sources matched each other for the root artifact set, so the landing source of truth was stable before any merge work began. After reconciliation, the active branch matches that baseline for the root workspace files and CI entrypoints.

## Conflict resolutions

| Area | Competing inputs | Chosen result | Reason |
| --- | --- | --- | --- |
| Root workspace and CI entrypoints | `phase-1-workspace-validation-entrypoints`, saved worktree, and `origin/main` all contained the same `go.work`, `go.work.sum`, root `Makefile`, `.github/workflows/ci.yml`, and `docs/architecture/workspace.md`; the active branch had none of them. | Restored the baseline versions directly. | There was no disagreement across the baseline sources, so copying the agreed root artifacts was the narrowest safe merge. |
| Root `.gitignore` | The landed baseline no longer ignored `go.work` and `go.work.sum`, while the active branch still needed current-workspace ignores for `tasks/`, `prd.json`, and `progress.txt`. | Kept the active branch's local-workflow ignore entries and removed the workspace-file ignore lines. | This preserved current branch workflow files while making the committed workspace artifacts reviewable and canonical in Git history. |
| Deterministic `agent-cli` validation wiring | The landed baseline expected provider-free root validation, but the active branch had newer CLI wiring and test expectations than the saved baseline snapshot. | Kept the current branch command structure and merged only the supporting fixes required for deterministic validation: the Darwin shell helper, inferencer-aware config validation, and deterministic mock config bootstrap. | Reapplying the older branch wholesale would have discarded newer CLI behavior; the selected merge preserves current code while landing the baseline validation contract. |
| Validation entrypoints | The active branch previously relied on per-module commands and had no authoritative root CI surface, while the landed baseline defined a single root `make ci` pipeline and thin GitHub Actions wrapper. | Standardized on the root `Makefile` and `.github/workflows/ci.yml` as the only canonical repository-wide validation entrypoints. | This removes contradictory root-vs-module orchestration paths and keeps contributor and CI behavior aligned. |

## Preservation rule

Unrelated local main changes must be preserved during reconciliation. The existence of non-baseline differences in module code and factory configuration is not a license to overwrite those trees from the saved worktree or `origin/main`.

## Duplicate-entrypoint check

The repository now has one canonical root validation surface:

- the root `Makefile` owns the repository-wide validation targets, including `ci`
- `.github/workflows/ci.yml` delegates to `make ci` instead of duplicating module commands in YAML
- `docs/architecture/workspace.md` documents that root contract and explicitly states that root automation must use the workspace-aware Make targets rather than ad hoc root `./...` patterns

This leaves no second root workflow file or competing repository-wide validation command in scope for the same behavior.

## Validation snapshot for this story

- Mergeability follow-up was required after the root baseline was restored: `make ci` initially exposed repo-wide `errcheck`, `staticcheck`, `ineffassign`, and unused-code failures across `agent-cli`, `go-agent-loop`, and `go-llm-gateway`, plus test expectations that no longer matched the now-canonical lowercase error strings.
- The reviewed branch fixed those blockers in place on the current PR head instead of treating them as inherited debt, because they were the concrete reason the restored root validation contract could not yet merge cleanly.
- Final convergence evidence:
  - Command: `make ci`
  - When: `2026-06-07T09:18:33Z` UTC
  - Result: success from the repository root
  - Included root stages: `fmt`, `vet`, `lint`, `staticcheck`, `test`, `test-integration`, `test-regressions`, `build`, and `coverage`
  - Documented skips: none inside `make ci`; the opt-in `test-customer-sessions` target remains outside the deterministic CI contract
