## Summary

- Move session-duration terminal admission, output projection, provider-terminal precedence, replay-artifact ordering, max-duration synthesis, and joined lifecycle/transport errors into the host-neutral `go-agent-runtime/services/sessionduration` contract and private implementation.
- Wire the service through a dedicated runtime graph and keep the CLI boundary as a 39-line `Deprecated` compatibility projection. Delete the immutable 131-line CLI implementation; the loop has only the explicitly authorized accessor/publication call-through repair required to remove duplicate adapter state.
- Add focused normal/race regressions, a separate `GOWORK=off` consumer, two fail-closed causal mutants, and an executable credential-free YUI replay probe for max-duration, provider-close, loop-close-negative, and healthy tool continuation.

## Gate evidence

- Factory admission: `verify-work --type task --name audio-runtime-c120-retire-cli-session-duration-terminal-boundary` returned `{"status":"admitted","project":"audio-runtime","name":"audio-runtime-c120-retire-cli-session-duration-terminal-boundary"}`.
- Branch: `codex/audio-runtime-c120-retire-cli-session-duration-terminal-boundary`; required PRD base `3963bc3566da24f8214634c17a9d0f79a6724171` and startup integration `8bdafc7f947a3a2c9856220abdc539437035bd21` are ancestors. Fresh `origin/main` `1a8467246c6607a06ffc7289075da2595724ce8b` is integrated by merge checkpoint `d41b34c34`.
- Independent review `work-review-30` rejected prior head `17491c1fa` because the adapter duplicated mutable `terminalWritten` state. Repair checkpoint `421f3dddc` removes that field, routes loop access through runtime-owned `State.Written`, and makes max-duration publication mark the same service-owned state. The repaired loop SHA-256 is `cd65dff97f8d58def4bac1dc0cf81b869a392ac366e6a19dee54359ff6e637ba`; the adapter is 39 lines.
- `verify.py --mode final`: PASS on the repaired current-main candidate. This includes runtime normal/race tests, all six named CLI regressions normal/race, the separate external consumer, legacy-file/adapter/path checks, and both causal mutants. The mutant tests fail as intended when provider precedence or artifact publication is removed.
- Fresh `make fmt`, `make vet`, pinned `make lint`, pinned `make staticcheck`, `make coverage-registration`, `make wire-check`, and `make architecture-size-check`: PASS on the repaired candidate. The architecture inventory is `192` packages, `1,912` files, and `28,239` functions; only the three demonstrated loop baseline values were lowered. Prior full coverage/architecture-test evidence remains preserved from the pre-review implementation head.
- Vertical probe: `latest-vertical-probe.json` records all four requested cases from source revision `421f3dddcdbd22bc078691e6210f2bbe1d54da91`, with artifact SHA-256 `ca65f6ad248c2b28c6314433b8dcb264e8a4955e6e872f115e43d14ab353e13b`, no credential use, and no surviving child process. The max-duration and loop-close-negative children exit 1 by the CLI's expected error contract while publishing structured `max_duration`/`loop`/`partial` terminal metadata; provider-close and healthy continuation exit 0 with the pinned audio/output markers and hashes. The later `5b86195fb` and `871ac970c` commits update only owned evidence, baseline, and verifier files; the tested executable inputs remain those at `421f3dddc`.

The post-fetch architecture-policy change is only the demonstrated generated-file registration for the new sessionduration Wire graph. The C120-owned duration-loop baseline records the three measured downward values from the authorized access repair; legacy shared registry paths and predecessor-owned peer paths remain untouched.

## Handoff

This candidate is ready to submit to the script CI gate. CI is not claimed green here; inspect any exact gate rejection and repair/resubmit this same task branch. Independent review, guarded merge, and the downstream meta vertical probe remain required.
