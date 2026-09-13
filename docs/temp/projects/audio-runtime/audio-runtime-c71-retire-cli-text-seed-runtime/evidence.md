# C71 implementation evidence

Date: 2026-09-11

## Admission and ancestry

- `project-control.py verify-work --type task --name audio-runtime-c71-retire-cli-text-seed-runtime` returned `{"status": "admitted", "project": "audio-runtime", "name": "audio-runtime-c71-retire-cli-text-seed-runtime"}`.
- The admitted branch is `codex/audio-runtime-c71-retire-cli-text-seed-runtime`, matching `prd.json.branchName` and the isolated worktree.
- `origin/main` was freshly fetched before implementation. The isolated `HEAD` and `origin/main` are both `d5d6f84363d8569d5dc1a59985f8d45cf50e1d06`.
- The accepted baseline file is `agent-cli/internal/services/internal/agentruntime/session_text.go` at 227 physical lines in that revision.

## Implementation

- Added the public `go-agent-runtime/services/textseed` contract, private allocator/session/output implementation, and generated `go-agent-runtime/services/textseed/wire` graph.
- The allocator is service-owned, atomically sequenced, explicitly injectable, and uses a constructor-local namespace containing the live allocator identity on both entropy and fallback paths; no CLI package-global sequence or hidden initialization remains. Repeated live service and Wire construction controls reject a duplicate value.
- The runtime wrapper preserves exact first `TEXT.DELTA` substitution, input immutability, bounded receive capacity 256, response and complete-message capabilities, terminal error identity, cancellation close, and retained first writer failures. It now honors an inner session's explicit complete-message capability flags instead of treating method presence alone as support.
- The CLI compatibility file is 126 lines, retaining option shaping, runtime planning, recording/writer adaptation, and deprecated peer-facing adapters. The five peer caller files remain unchanged.
- Added the external `GOWORK=off` consumer under this owned evidence path; it imports only the public textseed contract, the generated Wire constructor, and the shared messages contract. It covers explicit-empty and nonempty replacement, response/complete-message forwarding, retained writer failure, and bounded close completion.

## Focused gates

- `go test ./go-agent-runtime/services/textseed/...`: passed after the capability/allocator repair.
- `go test -race ./go-agent-runtime/services/textseed/...`: passed after the capability/allocator repair.
- `GOWORK=off go test ./...` in `external-consumer`: passed after the nonempty/capability/shutdown expansion.
- Focused normal and race agentruntime prompt/wire tests: 12 tests passed in each mode.
- Focused `go vet` for the service and agentruntime packages: no issues.
- Pinned `make staticcheck` (staticcheck 2026.1) and `make lint` (golangci-lint 2.9.0) pass across all workspace modules with no issues; `make vet` also passes.
- Direct package coverage: public contract 100.0%, private service 87.0% (floor 80.00%), and Wire 100.0% (floor 95.00%).
- `make coverage-registration` passes 179 registered workspace packages across 6 modules; `make coverage-changed COVERAGE_BASE=d5d6f84363d8569d5dc1a59985f8d45cf50e1d06` passes 179 packages across 7 profiles.
- `git diff --check`: clean.
- Repair checkpoint `1ed5a60e` is committed and pushed to the task branch; the worktree is clean.

## Pending shared integration

The current canonical board and handoff still show the C57 Wire-registry lease and C61 architecture-baseline lease active. The existing shared `scripts/wire-packages.txt` does not yet register `go-agent-runtime/services/textseed/wire`, and the immutable architecture baseline still contains the retired C71 `sessionTextWireSequence` violation. The C71 task does not modify either shared artifact while those leases are active. After both canonical releases, the next action is to integrate accepted main, reconcile only the released registry/baseline entries, rerun the focused architecture and Wire gates, then push the same task candidate for independent review and script CI.

The current `factory/docs/implementation-handoff.md` and operating policy were read before action. No acceptance waiver was used. The inherited root `progress.txt` contains no C71/textseed task record; canonical board state and task/review rows are the authoritative inbox.

## Executor verification checkpoint — 2026-09-12T03:20:54Z

- Reverified task admission with `project-control.py verify-work --type task --name audio-runtime-c71-retire-cli-text-seed-runtime`; the result remained `admitted`. The exact branch/worktree and `prd.json.branchName` remain `codex/audio-runtime-c71-retire-cli-text-seed-runtime`. The freshly fetched `origin/main` is `d5d6f84363d8569d5dc1a59985f8d45cf50e1d06`, and the required startup, planning, baseline and current-main ancestry checks pass.
- The focused service suite passed 8 tests across 3 packages; its race suite passed 21 tests across 3 packages with `-count=3`. The agentruntime text/compatibility suite passed 212 tests in both normal and race modes. The independent external consumer passed with `GOWORK=off`.
- `COUNT=3 bash scripts/test-session-ci-regressions.sh all` exited 0 in normal, coverage and race modes, including the expected divergent-PCM, transcript and replay-mismatch negative controls. `make coverage-registration`, `make coverage-changed COVERAGE_BASE=d5d6f84363d8569d5dc1a59985f8d45cf50e1d06`, `make vet`, pinned `make staticcheck`, pinned `make lint`, and `git diff --check` all exited 0.
- The unchanged shared gates fail closed on exactly two findings: `make wire-check` reports only unregistered `go-agent-runtime/services/textseed/wire/wire_gen.go`; `make architecture-size-check` reports only that generated-file registration plus the stale `sessionTextWireSequence` baseline entry. C71 did not edit `scripts/wire-packages.txt` or `docs/architecture/architecture-size-baseline.json`.
- This checkpoint is executor evidence only. No CI was polled or claimed green; no independent review, guarded merge, immutable executable probe or project acceptance is claimed. The next action is owner-mediated release of the C57 Wire-registry and C61 architecture-ledger leases, then integrate accepted main and make only the two demonstrated shared downward/registration changes before resubmitting the same task.

## Current-head CI rejection and dependency checkpoint — 2026-09-12T03:33Z

- Automatic SCRIPT CI run `34670152080` evaluated exact head `a6e0ff987bdd6e07c47c0ad162875ec88aa03345`. Unit, coverage, race, hermetic, WebMCP Chrome, macOS audio release and Windows audio portable passed. No independent review row or review finding exists for C71.
- Static job `103489876003` failed only on the already-characterized shared findings: `make wire-check` reports unregistered `go-agent-runtime/services/textseed/wire/wire_gen.go`; `make architecture-size-check` reports the stale `sessionTextWireSequence` baseline entry and the same generated-file registration. These require the exact C57 `scripts/wire-packages.txt` and C61 `docs/architecture/architecture-size-baseline.json` leases; C71 did not steal either writer.
- Integration job `103489875954` failed only in `TestAgentBinaryTest45HighRateToolAudioRegression/trial_09`, rendering `171191/177591` samples and losing exactly `6400`, with zero dropped samples, overflow events, discarded samples or discard events. This is the preserved C64/C47 provider/device-drain failure outside C71's owned paths.
- Post-rejection C71 revalidation remains green: textseed normal `13` tests, textseed race `39` tests across three repetitions, external `GOWORK=off` consumer, and `COUNT=1 bash scripts/test-session-ci-regressions.sh all` in normal, coverage and race modes all pass. No C71-specific repair is demonstrated, so the rejected head is not resubmitted unchanged.
- Exact next action is to retain this same task until the canonical C57 Wire-registry and C61 architecture-ledger leases release and the C64-owned high-rate integration defect is resolved on accepted main; then fetch/merge accepted main, apply only the demonstrated textseed Wire registration and stale baseline deletion, rerun the focused gates, commit/push the changed head, update PR `#461`, and submit the same task to SCRIPT CI without polling.
