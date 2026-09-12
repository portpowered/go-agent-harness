# C95 verification record

Focused behavioral checks passed:

```text
GOWORK=off go test ./services/roomaudiodiagnostics/... -count=3 -timeout=240s       PASS
GOWORK=off go test -race ./services/roomaudiodiagnostics/... -count=5 -timeout=360s PASS
GOWORK=off go test ./... -count=1 -timeout=180s  # external-consumer           PASS
GOWORK=off go test ./internal/services/internal/agentruntime -run 'RoomAudioIngress|ProviderInputRejection|ClosedTarget|Room.*Replay|Interruption|Audio.*Tool|Tool.*Audio' -count=1 -timeout=360s PASS
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
candidate `94d1fd1586fa4b533b221b5d725c907d6f3e5b63`. It built the
`nomicrophone` YUI artifact (SHA-256
`15e5e0a2c4aeec1b43ae85eb8de7394080ffd5f1ff8d7bc999266d84d3697a1b`), launched
the built public `room run --example` command, validated its two-participant
manifest, then ran all three focused children. All four bounded children exited
zero in 13.9 seconds with joined reader threads, reaped parents, no
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
