# C115 current-head revalidation checkpoint

## Identity and preservation

- The sole admitted project remains `audio-runtime/audio-runtime-v1`. The
  required `project-control.py verify-work --type task --name
  audio-runtime-c115-centralize-filesystem-audio-codec` returned `admitted`.
- The isolated branch is
  `codex/audio-runtime-c115-centralize-filesystem-audio-codec`, matching
  `prd.json.branchName`. Freshly fetched `origin/main` is
  `ea53be13ce5e4ef14fd8c89c695c21744a1f7686`; startup
  `8bdafc7f947a3a2c9856220abdc539437035bd21`, accepted/planning main
  `3963bc3566da24f8214634c17a9d0f79a6724171`, and the fresh main are all
  ancestors of current head `9c7e7fb50296c023be62a9991eb21e273861651f`.
- The worktree was clean before this evidence checkpoint. No host checkout,
  peer worktree, predecessor checkpoint, second project, waiver, or C79-owned
  shared path was reset or edited.

## Focused and accumulated revalidation

- Focused codec tests pass: 26 normal tests across 3 packages and 105 race
  tests across 3 packages at `-count=3`.
- Focused filesystem tests pass: 58 normal tests and 57 race tests at
  `-count=3`.
- The separate `GOWORK=off` external consumer compiles and runs successfully;
  it proves public Wire construction, conversion, input immutability, output
  bounds and cancellation identity.
- `make test-regressions` passes the agent-cli and go-llm-gateway replay
  fixtures. Pinned Staticcheck 2026.1, pinned golangci-lint 2.9.0, `make vet`,
  `make fmt`, `make coverage-registration` (179 packages across 6 modules),
  `make wire-check`, `git diff --check`, and the fail-closed C115 verifier all
  pass. The verifier reports current candidate `9c7e7fb5` and retains the
  credential-free five-case artifact/replay evidence from source
  `459a5b74c5db68a1b5a884baa3aabd337c23e2d7`.

## Preserved review repairs and remaining gate

- The prior independent-review findings remain accounted for: composed tool,
  session and room runtime-service injection; temporary-file cleanup-error
  identity; one-shot decoder termination on stdout/stderr overflow with
  termination-error preservation; and bounded shipped positive/negative
  evidence.
- `make architecture-size-check` reproduces exactly two unchanged findings
  outside C115 ownership: `agent-cli/internal/transport/cli` (`148 > 147`) and
  `internal/transport/cli/room.go` (`535 > 527`). No baseline was raised or
  edited. C79 retains the shared architecture release lane, so this candidate
  is not handed to script CI until C79's reviewed guarded merge and explicit
  release make the demonstrated downward reconciliation legal.
- No script-CI result, independent review, guarded merge, immutable vertical
  probe, physical/acoustic proof, or project acceptance is claimed.

Next action: preserve PR `#501` and this same task, update its description with
current head `9c7e7fb5` and this evidence, then wait for C79's reviewed guarded
merge/release. After release, fetch and integrate the accepted current main,
apply only demonstrated C115 shared changes if still needed, rerun the bounded
scoped and accumulated gates, and submit the changed head to script CI without
polling.
