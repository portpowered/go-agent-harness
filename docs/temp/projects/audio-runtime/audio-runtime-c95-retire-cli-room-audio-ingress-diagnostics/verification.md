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

# C95 current-head continuation checkpoint 2026-09-12T16:13:12Z

- Admission remains valid for the sole `audio-runtime` project: task
  `work-task-175` is canonical `init`, `project-control.py verify-work
  --type task --name audio-runtime-c95-retire-cli-room-audio-ingress-diagnostics`
  returns `admitted`, and the isolated branch exactly matches
  `prd.json.branchName`. Fetched `origin/main=d4766c3dbbf2c198142047ead4449d58dd47d485`;
  startup `8bdafc7f`, planning origin `3d3e7278`, and current-main ancestry all
  pass. The board has no C95 review row or C95 rejection feedback.
- Current HEAD is `6de65b5f06736ec36f0c99ced72346b535974ef0`. The executable
  implementation source remains `ee1a44b2`; the intervening HEAD delta is
  documentation-only in the owned C95 evidence files. A fresh `run.py` build
  from current HEAD launched `room run --example`, ran all three bounded room
  and non-room cases, exited zero in `24.242s`, reaped every child, and kept
  every process group dead. YUI is `50,954,386` bytes with SHA-256
  `92553b9edce8c5a17f5d8c87c9b1bc998d59c34f26bc364d80f7f330ba668171`.
- Focused runtime normal/race (`17`/`51` tests), external `GOWORK=off`
  consumer, positive plus three compiling mutation oracles, CLI room/replay/
  audio-tool normal/race (`240`/`65` tests), and the retirement/scope audit all
  pass. `make fmt`, `make vet`, pinned `make lint` (0 issues in 15 modules),
  pinned `make staticcheck`, `make coverage-registration` (`179` packages),
  `make architecture-size-check`, `make coverage-changed
  COVERAGE_BASE=3d3e7278`, and `git diff --check` pass. The accumulated
  `COUNT=1` normal, coverage, and race session-regression matrix passes,
  including the 20-trial high-rate audio controls and expected negative
  diagnostics.
- `make wire-check` remains the only demonstrated gate failure and reports
  exactly `unregistered=['go-agent-runtime/services/roomaudiodiagnostics/wire/wire_gen.go']`.
  C79 `work-task-114` still owns `scripts/wire-packages.txt` and
  `docs/architecture/architecture-size-baseline.json`; C95 has not edited
  either shared path. This is an unavailable ownership prerequisite, not a
  C95 implementation failure. The next action is to retain this same task,
  wait for C79's reviewed guarded merge and explicit lease release, integrate
  the accepted main, register only the C95 generated Wire path and remove only
  demonstrated C95 stale baseline entries, then rerun bounded Wire/architecture
  gates before submitting the changed same PR to SCRIPT CI.
