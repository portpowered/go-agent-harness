# C71 implementation evidence

Date: 2026-09-11

## Admission and ancestry

- `project-control.py verify-work --type task --name audio-runtime-c71-retire-cli-text-seed-runtime` returned `{"status": "admitted", "project": "audio-runtime", "name": "audio-runtime-c71-retire-cli-text-seed-runtime"}`.
- The admitted branch is `codex/audio-runtime-c71-retire-cli-text-seed-runtime`, matching `prd.json.branchName` and the isolated worktree.
- `origin/main` was freshly fetched before implementation. The isolated `HEAD` and `origin/main` are both `d5d6f84363d8569d5dc1a59985f8d45cf50e1d06`.
- The accepted baseline file is `agent-cli/internal/services/internal/agentruntime/session_text.go` at 227 physical lines in that revision.

## Implementation

- Added the public `go-agent-runtime/services/textseed` contract, private allocator/session/output implementation, and generated `go-agent-runtime/services/textseed/wire` graph.
- The allocator is service-owned, atomically sequenced, explicitly injectable, and uses a constructor-local namespace for default services; no CLI package-global sequence or hidden initialization remains.
- The runtime wrapper preserves exact first `TEXT.DELTA` substitution, input immutability, bounded receive capacity 256, response and complete-message capabilities, terminal error identity, cancellation close, and retained first writer failures.
- The CLI compatibility file is 126 lines, retaining option shaping, runtime planning, recording/writer adaptation, and deprecated peer-facing adapters. The five peer caller files remain unchanged.
- Added the external `GOWORK=off` consumer under this owned evidence path; it imports only the public textseed contract, the generated Wire constructor, and the shared messages contract.

## Focused gates

- `go test ./go-agent-runtime/services/textseed/...`: 11 tests passed.
- `go test -race ./go-agent-runtime/services/textseed/...`: 11 tests passed.
- `GOWORK=off go test ./...` in `external-consumer`: passed.
- Focused normal and race agentruntime prompt/wire tests: 12 tests passed in each mode.
- Focused `go vet` for the service and agentruntime packages: no issues.
- Focused `staticcheck` and `golangci-lint` for the service: no issues; focused `golangci-lint` for agentruntime: no issues.
- `git diff --check`: clean.

## Pending shared integration

The current canonical board still shows the shared C61 architecture-baseline lease active. The existing shared `scripts/wire-packages.txt` does not yet register `go-agent-runtime/services/textseed/wire`, and the immutable architecture baseline still contains the retired C71 `sessionTextWireSequence` violation. The C71 task does not modify either shared artifact while that lease is active. After the canonical C61 release, the next action is to reconcile the released registry/baseline entries, rerun the focused architecture and Wire gates, then push the same task candidate for independent review and script CI.

The requested `factory/docs/implementation-handoff.md` is absent from the admitted checkout and from the available factory tree; the current `factory/docs/handoff-plan.md`, `meta-planner-handoff.md`, operating policy, and workstation process instructions were used as the available handoff guidance. No acceptance waiver was used.
