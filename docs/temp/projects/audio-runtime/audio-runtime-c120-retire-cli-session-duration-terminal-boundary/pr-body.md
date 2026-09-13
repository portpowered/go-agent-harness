## Summary

- Move session-duration terminal admission, output projection, provider-terminal precedence, replay-artifact ordering, max-duration synthesis, and joined lifecycle/transport errors into the host-neutral `go-agent-runtime/services/sessionduration` contract and private implementation.
- Wire the service through a dedicated runtime graph and keep the CLI boundary as a 41-line `Deprecated` compatibility projection. Delete the immutable 131-line CLI implementation; `session_duration_loop.go` remains byte-for-byte unchanged.
- Add focused normal/race regressions, a separate `GOWORK=off` consumer, two fail-closed causal mutants, and an executable credential-free YUI replay probe for max-duration, provider-close, loop-close-negative, and healthy tool continuation.

## Gate evidence

- Factory admission: `verify-work --type task --name audio-runtime-c120-retire-cli-session-duration-terminal-boundary` returned `{"status":"admitted","project":"audio-runtime","name":"audio-runtime-c120-retire-cli-session-duration-terminal-boundary"}`.
- Branch: `codex/audio-runtime-c120-retire-cli-session-duration-terminal-boundary`; required PRD base `3963bc3566da24f8214634c17a9d0f79a6724171` and startup integration `8bdafc7f947a3a2c9856220abdc539437035bd21` are ancestors. Current `origin/main` `ea53be13c` is an ancestor after the preserved merge.
- `verify.py --mode final`: PASS on the current-main candidate. This includes runtime normal/race tests, all six named CLI regressions normal/race, the separate external consumer, legacy-file/adapter/path checks, and both causal mutants. The mutant tests fail as intended when provider precedence or artifact publication is removed.
- `make fmt`, `make vet`, pinned `make lint`, pinned `make staticcheck`, `make coverage`, `make coverage-registration`, and `make wire-check`: PASS on the implementation candidate. After integrating current main, `make architecture-size-check`, `make test-architecture-gate`, and `git diff --check`: PASS.
- Vertical probe: `latest-vertical-probe.json` records all four requested cases from source revision `b39f02f2ad16416d7883106d85e503eaa6cd69ea`, with artifact SHA-256 `0aba58187c1b2ce1f04b05e4ea3623b10757577c0af5728c5502f94aff0c2337`, no credential use, and no surviving child process. The max-duration child exits 1 by the CLI's expected error contract while publishing structured `max_duration`/`loop`/`partial` terminal metadata; provider-close and healthy continuation exit 0 with the pinned audio/output markers and hashes.

The only post-fetch architecture-policy change is the demonstrated generated-file registration for the new sessionduration Wire graph. Legacy shared registry/baseline paths, the duration loop, and predecessor-owned peer paths remain untouched.

## Handoff

This candidate is ready to submit to the script CI gate. CI is not claimed green here; inspect any exact gate rejection and repair/resubmit this same task branch. Independent review, guarded merge, and the downstream meta vertical probe remain required.
