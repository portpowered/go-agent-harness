# C59 audio-input retirement evidence

This task-local evidence directory contains the public-contract consumer and
independent checks for retiring the CLI audio-input implementation into the
shared `go-agent-runtime/services/audioinput` service. The consumer is a
separate `GOWORK=off` compile/test check; it is not a second project or an
acceptance waiver.

Build and run the shipped YUI binary through finite, text-only, and negative
paths:

```sh
rtk go build -trimpath -o /tmp/yui-c59 ./cmd/yui
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c59-retire-cli-audio-input-runtime/run.py \
  --case all --binary /tmp/yui-c59 --child-timeout 30 --aggregate-timeout 120
```

The runner strips ambient provider credentials, uses no live provider endpoint,
uses a committed finite replay fixture for the audio path, and kills the whole
child process group if a timeout occurs. It writes a structured result under
`evidence/`.

The independent verifier is invoked with `rtk proxy python3 verify.py` and
covers the baseline caller diff, public consumer, normal/race runtime tests,
five deliberate wrong-oracle controls, Wire registration, formatting, and the
final scope/provenance budget.

## Repaired CI rejection

PR #450 at `51f5c3942cd225f39270f57624bd7dfd39f9b805` was rejected by the
hermetic CI job because `TestRunCancellationReleasesBlockedDeltaForwarder`
returned `context canceled` instead of the provider's terminal
`*engine.StreamDeltaError`.

Commit `ac4d6022d68c0ff730a1f22229866048671bc5f0` preserves a non-context
engine error when cancellation races the completed hot loop and synchronizes
the regression test on provider close rather than a scheduler-dependent sleep.
The focused test passed 100 normal iterations and 20 race iterations; the full
`agentloop` package and accumulated session regression harness also passed.

## Repaired CI static rejection

The next script run, `34656026487`, rejected head
`0ea1fcf69480d40bfc137fb770345e21f03dc065` in job `103448507264` at the
architecture-size step. The exact findings were three C59-repair-induced
drifts in `go-agent-loop/pkg/agentloop/agent_loop.go`: file lines `629 > 622`,
`AgentLoop.Run` cognitive complexity `23 > 19`, and `AgentLoop.Run` function
lines `96 > 89`. No limit or baseline was raised.

Repair commit `0d39c5d2153db8aeb9e4db07d8ea2d0f534ce034` keeps the structured
provider error decision in the existing `execute_result.go` helper and leaves
`Run` at the established file/function counts. Architecture-size-check passes
at `189` packages, `1,907` files, and `28,165` functions; the full agentloop
normal/race package tests and the accumulated normal/coverage/race session
regression matrix pass. The subsequent script CI jobs were still running at
handoff, so no CI-green claim is made here.

## Current exact-source shipped refresh

After the repair, source revision `4fbf1c20be29518abd837b962097813a1f3b09d4`
was built with `go build -trimpath ./cmd/yui` and exercised by
`run.py --case all --child-timeout 60 --aggregate-timeout 600`. The rebuilt
binary is `/private/tmp/yui-c59-0d39c5d`, SHA-256
`ae787f5e5f703cced9cb7dd7f1e8c55e3c957acade4b31802c06cbf604734e2e`; the
structured evidence is SHA-256
`67a04f59c7b63bd00b185bd6c8f629d38a576c25c44800657c17a27ed7f0f4d5`. Text,
finite-audio, uncommitted-buffer, and unsupported-WebRTC controls pass with
bounded child shutdown and no survivors. The evidence is bound to execution
`origin/main d5d6f84363d8569d5dc1a59985f8d45cf50e1d06` by this checkpoint; it
does not claim CI, review, merge, or vertical acceptance.
