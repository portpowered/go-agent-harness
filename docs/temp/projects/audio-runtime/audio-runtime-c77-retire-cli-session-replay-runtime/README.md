# C77 replay-runtime handoff evidence

This directory is reserved for the C77 task's focused evidence. The external
consumer is a separate Go module and imports only the public
`services/replay` and `services/replay/wire` packages. Its tests use legacy
event-array fixtures written in-process, so they exercise public admission
without importing CLI, internal packages, credentials, or a workspace.

The consumer verifies text and multi-turn audio plans, exact declared rates,
stable malformed-rate identity, deterministic recorded-duration calculation,
provider-close discovery, and cancellation before replay draining.

## Executor evidence

`verify.py` is the C77-owned causal harness. It pins the C21 raw-capture
fixture hashes and event order, runs the public external consumer with
`GOWORK=off`, exercises the outbound/audio-boundary/synthetic-terminal
negative controls, and checks that the legacy adapter is 193 lines from the
planning baseline's 670 lines with no retired parser, loader, pacing, or
credential ownership.

The shipped executable probe is `run.py`. Each case builds a fresh
`nomicrophone` `yui` binary from the current checkout and runs it in a fresh
process group with a child and aggregate deadline:

```text
rtk proxy python3 verify.py --mode frozen-capture-matrix
rtk proxy python3 verify.py --mode mutation-disable-outbound-validation --expect-failure
rtk proxy python3 verify.py --mode mutation-reorder-tool-audio --expect-failure
rtk proxy python3 verify.py --mode mutation-synthetic-terminal --expect-failure
rtk proxy python3 verify.py --mode retirement-and-scope
rtk proxy python3 run.py --case shipped-raw-capture --child-timeout 60 --aggregate-timeout 300
rtk proxy python3 run.py --case shipped-finalized-bundle --child-timeout 60 --aggregate-timeout 300
rtk proxy python3 run.py --case strict-mutations --child-timeout 60 --aggregate-timeout 300
rtk proxy python3 run.py --case non-replay-regression --child-timeout 60 --aggregate-timeout 300
```

Generated logs and recordings stay under the ignored `runs/` directory. The
raw and finalized probes use the admitted C21 audio-tool and interruption
fixtures and assert exact provider bytes, session-log order, terminal state,
PCM digests, interruption healthy-tail bytes, tool output, and clean rejecting
mutations. The architecture-size check remains pending the active C61 shared
baseline lease; C77 does not edit that shared file while the lease is held.

The prior script-CI rejection at head `0054acad` is preserved in
`ci-rejection-34676193333.json`, including failed-job metadata, the exact
architecture diagnostics, the 6,400-sample remote-device loss, Factory
feedback, and the current non-reproduction evidence for the stress tests.
