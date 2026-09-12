# C105 browser cancellation coverage characterization

Classification: **INCONCLUSIVE**

This is an evidence-only characterization. It does not modify production or test source, C93/C79 paths, active owner paths, pull requests, or merge state; it does not claim C93 acceptance or any broad project gate.

## Pinned comparison and gate evidence

- Admitted project/contract: `audio-runtime` / `audio-runtime-v1`; task `audio-runtime-c105-characterize-browser-cancel-coverage-regression`; branch `codex/audio-runtime-c105-characterize-browser-cancel-coverage-regression`.
- Accepted main archive: `d4766c3dbbf2c198142047ead4449d58dd47d485`; preserved C93 archive: `35b4752e578dff49744159012982fccfacfb9794`.
- Startup integration ancestor: `8bdafc7f947a3a2c9856220abdc539437035bd21` in both archives; accepted main is an ancestor of C93.
- Matrix bounds: ten fixed trials per revision/mode, 90-second per-command ceiling, 600-second aggregate ceiling. Each revision/mode has an independent stable Go cache root and stable offline module-cache symlink view warmed at the exact Go-visible paths; every trial uses its immutable archive source tree and has unique temporary, GOPATH, HOME, coverage, and cache-snapshot paths plus a unique module-cache snapshot marker. Go-visible cache roots receive pre/post manifests after the ten sequential trials; any deterministic Go build-cache writes are recorded rather than treated as source mutation.
- Toolchain/environment and archive/tree hashes are in `provenance.json`; the exact diff inventory is in `source-diff-inventory.json`.

Evidence threshold: The fixed paired observations do not demonstrate an accepted-main recurrence, a necessary environment condition, or a changed browser causal edge sufficient for one of the other classes.

## Fixed matrix

| Revision | Mode | Trials | Outcome counts | Terminal conclusions |
|---|---|---:|---|---|
| main | normal | 10 | PASS=10 | TERMINAL_CANCELED_ASSERTED=10 |
| main | nomicrophone-coverpkg | 10 | PASS=10 | TERMINAL_CANCELED_ASSERTED=10 |
| c93 | normal | 10 | PASS=10 | TERMINAL_CANCELED_ASSERTED=10 |
| c93 | nomicrophone-coverpkg | 10 | PASS=10 | TERMINAL_CANCELED_ASSERTED=10 |

A passing unchanged test is recorded only as `TERMINAL_CANCELED_ASSERTED`: the test itself checks finalized/mechanical success, canceled terminal state, lifecycle/detached-tab preservation, late-event suppression, and audio. A product failure is retained with its raw JSON log and literal mismatch; it is never retried away. TIMEOUT, INFRA, zero-match, compile, or unrelated assertion outcomes are not product reproduction.

## Preserved CI negative control

The historical coverage failure is the immutable GitHub Actions job reference in `negative-control/ci-reference.json`, with SHA256 `244bd0ebbd3219ef85f11e83883290a803ff6aa6aeb193dc07955fdff4441af0`. It failed `TestRunBrowserConversationInterruptsInFlightWorkAndPreservesDetachedTab` at the unchanged `session_browser_scenario_runner_test.go:220` assertion. The captured signature is `Finalized:true`, `Mechanical.Passed:false`, empty `Cancellation.FinalState`, and the cancel observation `State:canceled Terminal:false`. `negative-control/assertion-map.json` contains the exact assertion text at both archived revisions and maps the bad state to the finalized/mechanical and canceled-terminal checks.

## Causal path and ownership

The observed event edge is: `browserConversationInterruptionController.observeInFlight` records the interruption at `session_browser_scenario_interrupt.go:56-101`; the tracker invokes the explicit cancel at `session_browser_scenario_tracker.go:349-385`; `browserConversationBroker.Cancel` records `State:canceled` but `Terminal:false` and records requested cancellation without `FinalState` at `session_browser_scenario_runner.go:914-935`; `browserConversationBroker.Invoke` can later copy a terminal `WaitInvocation` result at `session_browser_scenario_runner.go:875-900`; `BrowserConversationRun.RecordCancellation` only fills `FinalState` from a non-empty later observation at `session_browser_scenario_run.go:640-697`; mechanical evaluation rejects a missing canceled terminal state at `session_browser_scenario_evaluation.go:33-53`; and `Finalize` publishes the immutable result at `session_browser_scenario_run.go:779-792`.

The exact current canonical owner is the preserved C83 browser-scenario runner ownership (`work-task-125`, failed/inactive, branch `codex/audio-runtime-c83-retire-cli-browser-scenario-runner`). Its owned runner, run, tracker, interruption-controller, fixture-option, and browserrunner paths are recorded in `ownership.json`. C61 owns the separate browser-scenario contract/evaluation policy paths and is a consumer/dependency, not the event-edge owner. C105 transfers no lease.

## One bounded later recommendation

Under the preserved C83 owner, add bounded instrumentation at `agent-cli/internal/services/internal/agentruntime/session_browser_scenario_runner.go` (`browserConversationBroker.Cancel`/`Invoke`) and `session_browser_scenario_run.go` (`RecordCancellation`) to retain the ordered `(InvocationID, State, Terminal)` cancel and terminal-publication observations and make the missing handoff explicit. Preserve the unchanged assertions in `session_browser_scenario_runner_test.go:219-228` and `session_browser_scenario_evaluation.go:33-53`; run only the named test in normal and `nomicrophone`+coverpkg modes. Any implementation must wait for the C83/C61 ownership/dependency disposition, then use the later executor's focused regressions, SCRIPT-owned current-head CI, independent review, guarded merge, and fresh immutable vertical probe. C105 implements none of this recommendation.

## Falsifiers and downstream status

- A reproducible candidate-only failure with a changed browser cancellation edge would resolve this as C93_REGRESSION.
- The same failure recurring on accepted main in a matched mode would resolve this as ACCEPTED_MAIN_BROWSER_FLAKE.
- The same failure only under a recorded coverpkg/nomicrophone condition on both revisions would resolve this as ENVIRONMENT_ONLY_TRIGGER.

All nine immutable project criteria (`AUDIO`, `DEVICE`, `EMBED`, `SERVICE`, `TRACE`, `REPLAY`, `FAILURES`, `QUALITY`, `PARITY`) remain `OPEN`; this package is not a project acceptance waiver. The machine-readable classification and verifier output are authoritative for evidence completeness, not a green CI claim.
