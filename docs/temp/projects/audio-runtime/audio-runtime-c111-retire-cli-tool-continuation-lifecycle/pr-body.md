## Summary

- Extracts unresolved tool-result and image/ordinary tool-continuation snapshot construction, normalization, metadata, and typed error joining into the public `go-agent-runtime/services/sessioncontinuation` contract with a private implementation and dedicated Wire package.
- Leaves the CLI lifecycle file as a 48-line explicit-Deprecated compatibility adapter; the immutable baseline was 147 lines, for 99 net physical production lines retired.
- Keeps compatibility identity at the public contract boundary and enriches an unresolved snapshot once, so one obligation produces one typed lifecycle error rather than a duplicate legacy join.
- Preserves the reusable live runtime's sentinel/type compatibility through canonical `errors.Is`/`errors.As` behavior and the historical CLI test facade, while keeping public continuation sentinels immutable.
- Adds a credential-free external consumer, bounded causal/replay evidence, and coverage registrations for the new packages.
- Integrates freshly fetched current `origin/main` (`bd6a1289218d1bef1a3af36e64e9d4496062416f`) with no reset in merge `44747db8fca0a1f5e11ce9722b709976316b943a`; the final repair checkpoint is `4921c3751f0027941f6f3991cf9c88f784b18265`.

## Evidence

- Admission is `admitted` for `audio-runtime` in `~default`; `prd.branchName` matches `codex/audio-runtime-c111-retire-cli-tool-continuation-lifecycle`, and startup, accepted-main, and current-main ancestry are preserved without resetting the host checkout.
- Final-head owned verifier: `verify.py --mode positive-and-negative-controls` accepted at `4921c375`; artifact `artifacts/verify-1789308278-90785.json` (SHA-256 `e4031d458b52f356a3d5d9e7c9d9aea4f6034ed7280315f17fbc3d8d88cb3311`). It verifies the 147-line immutable baseline/hash, 48-line adapter, owned scope, runtime contract/race checks, GOWORK=off external consumer, and all four accumulated regressions; every subprocess exited 0, was bounded, and was reaped.
- Final exact-head causal repair checks pass: `TestReadImageSpokenFailedContinuationIsActionable` passes under normal, `-tags=nomicrophone`, and coverpkg execution. This is the exact failure shared by prior CI integration, coverage, and hermetic jobs.
- Source-pinned credential-free replay at `4921c375`: accepted; artifact `artifacts/run-1789307734-75807.json` (SHA-256 `9a7b2b591537cb9a7f4db8b2737372311982487fb10571798b63690b5ddcdaae`). The YUI build exited 0 at 51,122,754 bytes (SHA-256 `3a29a36dda5da23a4ff1f856dadbcbc4f77b165a453a114995454c25efde44a4`); missing-tool and corrupt-audio controls passed, tool replay returned `PROBE_TOOL_MARKER_9182`, non-tool replay had no tool events, both audio outputs were produced, all process groups were reaped, and realtime sessions were 0.
- Final quality sweep passes: format, vet, pinned golangci-lint 2.9.0, pinned staticcheck 2026.1, Wire, coverage registration (188 packages across 6 modules), architecture-size-check (198 packages/1,928 files/28,650 functions), focused normal/race contract tests, internal lifecycle tests, and `git diff --check`.
- The complete prior CI rejection is retained as raw `ci-rejection-34759945739.json`: run `34759945739` at old head `3a88aef2` failed only integration, coverage, and hermetic on the shared image-continuation assertion; all other required jobs passed. The exact log error was a token-limit continuation error followed by the missing image continuation sentinel assertion.

## Gate status

Independent review findings at `work-review-22` and `work-review-29` are repaired: the CLI adapter no longer joins a second unresolved-result error, compatibility is decision-free at the public boundary, explicit `Deprecated:` markers remain lint-clean, and the verifier/evidence are current. The architecture gate's mutable-sentinel finding is also repaired in `4921c375`. No self-review was performed.

The candidate is ready for the script-owned CI gate on this same PR. Script CI is not claimed green; after push, `ACCEPTED` hands off the changed head without polling. If it rejects, inspect the exact failed checks/logs, repair this same task, and resubmit before requesting a fresh independent review and guarded merge. Project-wide SERVICE, REPLAY, FAILURES, QUALITY, and PARITY gates remain open.
