# C95 verification record

Latest executable-source checkpoint: `ee1a44b2073f1de4b818bb8e9ad1db2963829293`.
The prior script-CI rejection at PR head `0284e379` identified two owned
adapter `errcheck` findings; the adapter now handles `Admit` and `Record`
errors explicitly (only the documented `ErrFinished` teardown race is benign,
other errors fail closed), and the concurrent service test checks its Record
error. The final adapter is 168 physical lines.

Focused behavioral checks passed:

```text
GOWORK=off go test ./services/roomaudiodiagnostics/... -count=3 -timeout=240s       PASS
GOWORK=off go test -race ./services/roomaudiodiagnostics/... -count=5 -timeout=360s PASS
GOWORK=off go test ./... -count=1 -timeout=180s  # external-consumer           PASS
GOWORK=off go test ./internal/services/internal/agentruntime -run 'RoomAudioIngress|ProviderInputRejection|ClosedTarget|Room.*Replay|Interruption|Audio.*Tool|Tool.*Audio' -count=1 -timeout=360s PASS
go test -race ./internal/services/internal/agentruntime -run 'RoomAudioIngress|ProviderInputRejection|ClosedTarget|Room.*Replay|Interruption|Audio.*Tool|Tool.*Audio' -count=3 -timeout=360s PASS
make lint                                                                       PASS (all 15 modules, 0 issues)
make coverage-registration                                                         PASS (179 packages / 6 modules)
make vet                                                                         PASS
make staticcheck                                                                 PASS (pinned 2026.1)
```

`verify.py --mode positive-and-three-mutations` passed. The positive runtime
and external-consumer controls passed, and each mutation compiled before its
intended oracle failed:

```text
pending-loss                         compiled_and_failed_oracle
source-collapse                      compiled_and_failed_oracle
rejected-counted-accepted            compiled_and_failed_oracle
```

`run.py --case room-ingress-replay --case room-ingress-rejection
--case non-room-audio-tool --child-timeout 60 --aggregate-timeout 300` passed at
candidate `ee1a44b2073f1de4b818bb8e9ad1db2963829293`. It built the
`nomicrophone` YUI artifact (SHA-256
`92553b9edce8c5a17f5d8c87c9b1bc998d59c34f26bc364d80f7f330ba668171`), launched
the built public `room run --example` command, validated its two-participant
manifest, then ran all three focused children. All four bounded children exited
zero in 25.6 seconds with joined reader threads, reaped parents, no
process-group survivors, and output under the 64 KiB cap.

The accumulated historical regression matrix also passed with
`COUNT=1 bash scripts/test-session-ci-regressions.sh all`: normal, coverage,
and race modes across CLI, integration, simulated devices, and composed
provider tests.

The following gates are intentionally not green until the C79-owned lease is
released:

```text
make wire-check
wire-check: Wire registry mismatch: unregistered=['go-agent-runtime/services/roomaudiodiagnostics/wire/wire_gen.go'], outside modules=[]

make architecture-size-check
3 issues: two baseline-stale entries for the retired predecessor and one
generated-file-spoof for the unregistered roomaudiodiagnostics Wire graph
```

No CI was polled. The candidate is ready for the script CI gate after the
primary releases the shared Wire/baseline ownership and assigns those exact
repairs; `ACCEPTED` will mean submission to that gate, not green CI.
