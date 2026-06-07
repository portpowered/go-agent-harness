# Phase 1 authoritative checkout reconciliation

## Comparison inputs

This reconciliation record starts from the three Phase 1 reference surfaces
named in the PRD:

- local `main` at `ef4787d`
- `origin/main` at `38dd71b`
- `origin/phase-1-authoritative-workspace-convergence` at `42ccd5f`

The current worktree branch is
`phase-1-authoritative-checkout-reconciliation`, and its `HEAD` currently
matches the stale local-`main` baseline at `ef4787d`.

## In-scope artifacts

The PRD limits Phase 1 reconciliation to the landed root baseline and the
directly required supporting fixes needed to keep that baseline working
together:

- root `go.work`
- root `go.work.sum`
- root `Makefile`
- `.github/workflows/ci.yml`
- root `README.md` refreshes
- `docs/architecture/workspace.md`
- related architecture and audit docs required by that baseline
- directly required supporting fixes only

Any final divergence from `origin/main` or
`origin/phase-1-authoritative-workspace-convergence` must be documented with
the competing inputs, the chosen result, and the reason for that choice.

## Comparison summary

| Artifact or area | local `main` / current `HEAD` (`ef4787d`) | `origin/main` (`38dd71b`) | `origin/phase-1-authoritative-workspace-convergence` (`42ccd5f`) | Initial status |
| --- | --- | --- | --- | --- |
| `go.work` | missing | present | present | missing locally, restore later |
| `go.work.sum` | missing | present | present | missing locally, restore later |
| `Makefile` | missing | present | present | missing locally, restore later |
| `.github/workflows/ci.yml` | missing | present | present | missing locally, restore later |
| `docs/architecture/workspace.md` | missing | present | present | missing locally, restore later |
| related architecture/audit docs | missing | present | present | missing locally, restore later |
| `docs/architecture/workspace-validation-baseline-reconciliation.md` | missing | present | present | restored in this story as the reviewer-facing comparison record |
| root `README.md` | present, minimal pre-Phase-1 readme | present, expanded Phase 1 root guide | present, same in-scope surface as `origin/main` | conflicting, merge intentionally |
| broader module code under `agent-cli`, `go-agent-loop`, `go-llm-gateway` | branch-specific local state | differs from stale local baseline | differs from stale local baseline | preserve unless directly required |

For the in-scope artifact list above, `origin/main` and
`origin/phase-1-authoritative-workspace-convergence` currently expose the same
baseline surface. The active discrepancy is between that landed Phase 1
surface and the stale local checkout at `ef4787d`.

## Missing artifacts

These in-scope artifacts are absent from the stale local checkout and must be
restored or merged in later stories:

- `go.work`
- `go.work.sum`
- `Makefile`
- `.github/workflows/ci.yml`
- `docs/architecture/workspace.md`
- related architecture and audit docs required by the baseline

## Conflicting artifacts

These in-scope surfaces exist locally but do not match the Phase 1 baseline
that reviewers can inspect on repository surfaces:

- root `README.md`
- any directly required supporting code or module metadata changes needed to
  make the restored root baseline work on top of the stale local checkout

## Already-matching artifacts

There are no already-matching in-scope root baseline artifacts at this
starting point. Each scoped artifact is either missing locally or materially
older than the Phase 1 baseline exposed on the repository remotes.

## Unrelated local work to preserve

This reconciliation must preserve local changes outside the narrow Phase 1
scope unless a later story proves they are directly required for mergeability:

- non-baseline module code differences outside the root workspace/CI/docs
  artifact list
- factory workflow files not required by the restored root baseline
- local executor-workflow files such as untracked `prd.md` and local
  `progress.txt`

## Validation snapshot

Because this stale checkout does not yet contain a root `go.work` file, root
`go test ./...` is not a valid typecheck surface for this story. Compile
validation was run per module instead:

- `cd agent-cli && go test -run '^$' ./...`
- `cd go-agent-loop && go test -run '^$' ./...`
- `cd go-llm-gateway && go test -run '^$' ./...`

Initial result:

- `go-agent-loop`: passed
- `go-llm-gateway`: passed
- `agent-cli`: requires a minimal `go mod tidy` metadata sync before the
  compile-only test pass can succeed on this stale branch

Final result for story 001:

- `agent-cli`: passed after syncing `go.mod`/`go.sum` and restoring the missing
  Darwin shell termination helper used by `internal/tools/tool_shell.go`
- compile-only validation now passes across `agent-cli`, `go-agent-loop`, and
  `go-llm-gateway`
