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

## Pending shared integration

The current canonical board still shows the shared C61 architecture-baseline lease active. The existing shared `scripts/wire-packages.txt` does not yet register `go-agent-runtime/services/textseed/wire`, and the immutable architecture baseline still contains the retired C71 `sessionTextWireSequence` violation. The C71 task does not modify either shared artifact while that lease is active. After the canonical C61 release, the next action is to reconcile the released registry/baseline entries, rerun the focused architecture and Wire gates, then push the same task candidate for independent review and script CI.

The current `factory/docs/implementation-handoff.md` and operating policy were read before action. No acceptance waiver was used. The inherited root `progress.txt` contains no C71/textseed task record; canonical board state and task/review rows are the authoritative inbox.
