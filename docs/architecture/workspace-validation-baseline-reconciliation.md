# Phase 1 authoritative workspace convergence reconciliation

## Starting point

- Active branch: `phase-1-authoritative-workspace-convergence`
- Active branch `HEAD`: `ef4787d6493c40691c6fe8546f1e48a0082bbe48`
- Expected stale local-main baseline from the PRD: `ef4787d`
- Landed Phase 1 comparison source: `origin/main`

This document records the initial comparison for the authoritative workspace
convergence PRD. The current worktree starts from the stale local-main commit
named in the PRD and does not yet contain the landed Phase 1 root workspace
baseline that already exists on `origin/main`.

## In-scope landed artifacts to reconcile

The PRD defines these landed artifacts and directly required supporting fixes as
the convergence scope:

- root `go.work`
- root `go.work.sum`
- root `Makefile`
- `.github/workflows/ci.yml`
- `docs/architecture/workspace.md`
- `docs/architecture/dependencies.md`
- `docs/architecture/contract-gap-audit.md`
- `docs/architecture/workspace-validation-baseline-reconciliation.md`
- root `README.md` and related landed documentation updates
- directly required supporting fixes needed to make the restored baseline work

Any final divergence from `origin/main` for those areas must be documented
explicitly with the competing inputs, the chosen outcome, and the reason.

## Comparison summary against `origin/main`

| Artifact or area | Current `HEAD` (`ef4787d`) | `origin/main` | Initial reconciliation status |
| --- | --- | --- | --- |
| `go.work` | missing | present | missing locally, must be restored |
| `go.work.sum` | missing | present | missing locally, must be restored |
| `Makefile` | missing | present | missing locally, must be restored |
| `.github/workflows/ci.yml` | missing | present | missing locally, must be restored |
| `docs/architecture/workspace.md` | missing | present | missing locally, must be restored |
| `docs/architecture/dependencies.md` | missing | present | missing locally, must be restored |
| `docs/architecture/contract-gap-audit.md` | missing | present | missing locally, must be restored |
| `docs/architecture/workspace-validation-baseline-reconciliation.md` | missing | present | created locally in this story to track the convergence work |
| `README.md` | present, older minimal root readme | present, expanded Phase 1 workspace guide | conflicting, merge intentionally |
| Supporting fixes in `agent-cli`, `go-agent-loop`, `go-llm-gateway` | current branch state differs from landed baseline | present on landed baseline | inspect surgically during later stories |

## Missing artifacts

These in-scope landed artifacts are absent from the stale authoritative
workspace and need to be ported or merged in later stories:

- `go.work`
- `go.work.sum`
- `Makefile`
- `.github/workflows/ci.yml`
- `docs/architecture/workspace.md`
- `docs/architecture/dependencies.md`
- `docs/architecture/contract-gap-audit.md`

## Conflicting artifacts

These scoped surfaces already exist locally but do not match the landed Phase 1
baseline and therefore must be merged intentionally rather than overwritten:

- `README.md`
- supporting code and module metadata differences under `agent-cli`,
  `go-agent-loop`, and `go-llm-gateway` that may be required to make the
  restored root workspace contract actually work

## Already-matching artifacts

There are no already-matching in-scope artifacts at this starting point. Every
root baseline file named in the PRD is either missing locally or materially
different from the landed Phase 1 state on `origin/main`.

## Unrelated local changes to preserve

The convergence work must not treat all differences from `origin/main` as
replaceable drift. The following local state is outside the narrow scope of this
story and should be preserved unless a later convergence step proves it is
directly required for mergeability:

- broad module-level code differences outside the explicit Phase 1 baseline
  artifact list
- factory workflow content unrelated to the root workspace baseline
- local workflow files used by this executor flow, including untracked
  `prd.md` and the local progress log

## Reconciliation rule

Intentional divergence from `origin/main` is allowed only when it is explicit.
For each final divergence that remains after convergence, the record must state:

- the competing inputs
- the selected result in the authoritative workspace
- the reason that result is safer or more correct than a blind copy from
  `origin/main`

## Validation snapshot for this story

- Compile-only command: `go test -run '^$' ./...` run separately in
  `agent-cli`, `go-agent-loop`, and `go-llm-gateway`
- Purpose: prove the stale authoritative workspace still typechecks before the
  file-restoration stories begin
- Result: passed after two minimal supporting fixes required by the stale
  branch itself:
  - `agent-cli/go.mod` and `agent-cli/go.sum` needed the tidy delta for the
    current test imports and replay/session transport dependency graph
  - `agent-cli/internal/tools/shell_darwin.go` was missing even though
    `tool_shell.go` already referenced the Darwin process helpers
- Additional observation for later stories: full `agent-cli` integration tests
  on this stale branch still expect deterministic config bootstrap that is not
  yet present, so they fail at runtime by requiring a live OpenRouter API key

## Story 002 workspace baseline restoration

The root workspace and CI baseline named in the PRD has now been restored into
the authoritative checkout from `origin/main`:

- restored root `go.work`
- restored root `go.work.sum`
- restored root `Makefile`
- restored `.github/workflows/ci.yml`

The restoration stayed inside the Phase 1 scope. No unrelated local files were
overwritten. The only supporting code changes made alongside the root baseline
were the minimum `agent-cli` test-wiring fixes required for the restored root
targets to work on this stale branch:

- added deterministic mock config/models bootstrap in
  `agent-cli/internal/wire/test_defaults.go`
- updated `agent-cli/internal/wire/{wire.go,wire_gen.go}` so mock CLI
  initializers opt into that deterministic config and relaxed model validation
- updated `agent-cli/internal/agent/executor.go` so injected-test executors can
  skip provider/model capability validation while production wiring remains
  strict

## Story 002 validation

The restored root entrypoints were exercised from the repository root:

- `make fmt`
- `make typecheck`
- `make test`
- `make test-integration`
- `make test-regressions`

Results:

- all five commands passed in the authoritative workspace on
  `2026-06-07T12:17:05Z`
- the restored root `Makefile` contract now drives deterministic compile and
  test validation across `agent-cli`, `go-agent-loop`, and `go-llm-gateway`

## Intentional divergence status after story 002

There is no final intentional divergence for the restored root workspace files
in this story. The authoritative checkout now carries the landed root baseline
artifacts from `origin/main`, while the supporting `agent-cli` test-wiring
fixes are branch-local compatibility changes needed to make that baseline pass
on the stale starting point.

## Story 003 documentation restoration

The landed Phase 1 documentation and README surface has now been restored into
the authoritative checkout:

- restored `docs/architecture/workspace.md`
- restored `docs/architecture/dependencies.md`
- restored `docs/architecture/contract-gap-audit.md`
- merged the landed root `README.md` workspace guide
- merged the landed module README refreshes for `agent-cli`,
  `go-agent-loop`, and `go-llm-gateway`
- restored `agent-cli/docs/interaction-replay.md` and updated
  `agent-cli/docs/README.md` so the CLI docs index matches the landed replay
  surface

## Story 003 documentation reconciliation result

This story did not require a broad rewrite of local documentation. The
authoritative checkout had only stale or missing versions of these files, so
the final merged outcome is the landed `origin/main` documentation surface for
the scoped architecture and README files.

Intentional divergence status after story 003:

- none for the restored documentation files in this story
- the only remaining branch-local divergence continues to be the `agent-cli`
  compatibility/test-wiring fixes already recorded under story 002

## Story 004 supporting fixes and conflict resolution

Story 004 reconciles the remaining merge-sensitive support work needed to keep
the restored Phase 1 baseline authoritative on this branch without discarding
current local code:

- removed the stale root `.gitignore` entries for `go.work` and `go.work.sum`
  so the committed workspace files restored in story 002 remain canonical and
  reviewable without force-add exceptions
- kept the branch-local `agent-cli` compatibility/test-wiring fixes because
  they are the minimum changes required to preserve deterministic root
  validation on top of the newer local CLI code
- left unrelated module-level differences outside the Phase 1 baseline scope
  untouched

## Story 004 conflict resolutions

| Area | Competing inputs | Chosen result | Reason |
| --- | --- | --- | --- |
| Root `.gitignore` | The stale local branch ignored `go.work` and `go.work.sum`, while the landed Phase 1 baseline treats those files as committed root artifacts. | Removed the workspace-file ignore entries but kept the local workflow ignores for `tasks/`, `prd.json`, and `progress.txt`. | This preserves the executor-local workflow files while making the root workspace baseline reviewable and maintainable without special staging steps. |
| Deterministic `agent-cli` validation wiring | `origin/main` supplies the landed root validation contract, but this branch also carries newer local CLI/test behavior that should not be overwritten wholesale. | Kept the current branch command structure and retained only the compatibility fixes already added in `agent-cli/internal/agent/executor.go`, `agent-cli/internal/wire/{test_defaults.go,wire.go,wire_gen.go}`, `agent-cli/internal/tools/shell_darwin.go`, and the tidy module metadata. | Replacing the newer local CLI surface with an older baseline snapshot would lose unrelated authoritative work; the selected merge keeps local behavior while preserving the deterministic Phase 1 validation contract. |
| Root validation entrypoints | The stale local branch previously lacked a canonical repository-wide validation surface, while the landed baseline defines the root `Makefile` and `.github/workflows/ci.yml` contract. | Standardized on the restored root `Makefile` and `.github/workflows/ci.yml` as the only repository-wide validation entrypoints. | This removes ambiguity between ad hoc per-module commands and the reviewer-facing root contract. |

## Duplicate-entrypoint check

The repository now exposes one canonical root validation/documentation surface
for the restored Phase 1 baseline:

- the root `Makefile` owns the repository-wide validation targets, including
  `make ci`
- `.github/workflows/ci.yml` delegates to `make ci` instead of duplicating
  module-specific validation logic in workflow YAML
- `docs/architecture/workspace.md` documents the root workspace contract and
  contributor expectations
- this reconciliation document records the explicit divergence decisions instead
  of leaving them implicit in branch-only code

This leaves no second root workflow file or competing repository-wide
validation/documentation entrypoint in scope for the same Phase 1 behavior.

## Story 005 deterministic convergence proof

Story 005 completes the final authoritative-workspace convergence proof by
running the restored root validation contract end to end and resolving the
small mergeability blockers that the older branch state exposed under that
contract.

Mergeability follow-up that was required on top of the restored Phase 1
baseline:

- workspace-wide lint/staticcheck cleanup in `agent-cli`, `go-agent-loop`, and
  `go-llm-gateway` where the restored root `make ci` contract surfaced
  pre-existing unchecked errors, dead helpers, and nil-constructor paths
- test expectation updates in `agent-cli/internal/input/validate_test.go` and
  `agent-cli/test/integration/session_command_test.go` so the assertions match
  the current lower-case, quoted runtime error strings already emitted by the
  branch-local CLI code
- replay/session test cleanup in
  `go-llm-gateway/pkg/providers/grok/session_test.go` and
  `go-llm-gateway/pkg/providers/openai/session_test.go` so all session-close
  and JSON decode paths satisfy the restored lint contract

## Story 005 validation evidence

- Command: `make ci`
- Workspace: repository root of the authoritative checkout
- Result: passed on `2026-06-07T12:57:28Z`
- Reproduction assumptions: no live provider credentials were required; the
  deterministic root test/integration/regression fixtures supplied all needed
  local state
- Covered root stages: `fmt`, `vet`, `lint`, `staticcheck`, `test`,
  `test-integration`, `test-regressions`, `build`, and `coverage`

## Final intentional divergence summary

The final authoritative workspace intentionally diverges from a plain file-for-file
copy of `origin/main` only in the narrow supporting fixes required to keep the
restored Phase 1 baseline working on top of the newer local code already
present on this branch:

- deterministic `agent-cli` test/bootstrap wiring and the Darwin shell helper
- small lint/staticcheck/test-alignment fixes needed so the canonical restored
  root `make ci` contract passes on the reviewed branch head

Those divergences were kept because replacing the newer local code wholesale
with an older baseline snapshot would have discarded unrelated authoritative
branch work, while the selected merge preserves current behavior and still
lands the Phase 1 root workspace contract completely.
