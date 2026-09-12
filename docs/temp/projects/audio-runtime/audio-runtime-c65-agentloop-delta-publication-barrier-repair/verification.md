# C65 verification checkpoint

## Scope

Candidate source/test checkpoint: `6e1561370b3ad7b1bf53c6cf49bd06299da7f41f`.

Only the admitted task-owned paths changed:

- `go-agent-loop/pkg/agentloop/agent_loop.go`
- `go-agent-loop/pkg/agentloop/run_delta_barrier_test.go`
- this task-owned evidence directory

The production adjustment is confined to `AgentLoop.Run`: after a natural
engine return, the finish path no longer cancels the already-quiesced internal
loop context before joining `forwardDone`; the independent forwarding context
remains available to drain already-published kernel deltas, and caller
cancellation still releases a full public buffer.

## Causal proof

`TestRunJoinsPublishedDeltasBeforeReturningOnEngineError` now:

1. queues provider `SESSION.OPEN`, starts `Run`, and reads that exact event from the public buffer;
2. queues a first text delta and reads its exact content from `AgentLoop.Deltas()` before any terminal error;
3. fills the public delta buffer, queues a second text delta, and waits on the exact `KernelRunner: sending text delta` signal emitted after reader-channel publication;
4. queues the structured terminal error only after that second kernel publication;
5. proves `Run` has not returned, releases the bounded backlog, and verifies the second exact text delta remains readable after `Run` returns;
6. verifies the original `*messages.ErrorValue` identity plus `errors.Is`/`errors.As` compatibility.

`TestRunCancellationReleasesBlockedDeltaForwarder` covers caller cancellation with the same full-buffer pressure. Capacity tests remain unchanged and passed.

## Gate results

All commands below passed during final candidate verification from the isolated worktree:

```text
go test ./go-agent-loop/pkg/agentloop -run '^TestRun(JoinsPublishedDeltasBeforeReturningOnEngineError|CancellationReleasesBlockedDeltaForwarder)$' -count=100
go test -race ./go-agent-loop/pkg/agentloop -run '^TestRunJoinsPublishedDeltasBeforeReturningOnEngineError$' -count=20
CGO_ENABLED=0 GOMAXPROCS=64 go test ./go-agent-loop/pkg/agentloop -run '^TestRunJoinsPublishedDeltasBeforeReturningOnEngineError$' -count=2000
go test ./go-agent-loop/pkg/agentloop -run '^TestNew_(DefaultBufferCapacity|GlobalBufferCapacityOverride|PerParticipantBufferCapacity|PerParticipantOverridesGlobal|UserRunnerOutboxCapacity|KernelRunnerDeltaInboxCapacity)$' -count=20
go test ./go-agent-loop/pkg/agentloop
go test -race ./go-agent-loop/pkg/agentloop
go test ./agent-cli/test/integration -run '^Test(SessionCommand_DefaultRegistryExecRoundTripInStrictOpenAIReplay|AgentBinaryToolContinuationPreservesRemoteDeviceAudio)$' -count=1
make fmt
make verify-architecture
make coverage-registration
make vet
make lint
make staticcheck
COVERAGE_BASE=d5d6f84363d8569d5dc1a59985f8d45cf50e1d06 make coverage-changed
```

The coverage gate reported `176 registered packages checked across 7 profiles`; the agent-cli target-wide run completed in `5m20.244s` within its configured `8m0s` budget.

## Negative control

Temporarily removing only `<-forwardDone` from `AgentLoop.Run` failed the causal test immediately:

```text
run_delta_barrier_test.go:79: Run returned before joining the published delta: provider failed after publishing text
```

The join was restored before the passing gates and checkpoint.

## Post-rejection repair checkpoint

The unconditional internal-loop cancellation was removed after script CI
rejected PR 455 for the separately leased high-rate audio continuation loss.
The repaired source/evidence checkpoint is the commit containing this record;
the final branch head is reported in the handoff.
The causal barrier remains the `forwardDone` join; only the redundant natural
error cancellation was removed. A one-line explanatory comment preserves the
architecture-size baseline without changing runtime behavior.

After the repair, these gates passed:

```text
go test ./go-agent-loop/pkg/agentloop -run '^TestRun(JoinsPublishedDeltasBeforeReturningOnEngineError|CancellationReleasesBlockedDeltaForwarder)$' -count=100
go test -race ./go-agent-loop/pkg/agentloop -run '^TestRunJoinsPublishedDeltasBeforeReturningOnEngineError$' -count=20
go test ./go-agent-loop/pkg/agentloop
go test -race ./go-agent-loop/pkg/agentloop
go test ./agent-cli/test/integration -run '^Test(SessionCommand_DefaultRegistryExecRoundTripInStrictOpenAIReplay|AgentBinaryToolContinuationPreservesRemoteDeviceAudio)$' -count=1 -timeout 180s
make fmt
make verify-architecture
COVERAGE_BASE=d5d6f84363d8569d5dc1a59985f8d45cf50e1d06 make coverage-changed
make vet
make lint
make staticcheck
```

Changed coverage completed in `5m15.762s` for the agent-cli target-wide
suite and passed the full gate with `176 registered packages checked across 7
profiles`.

The shipped 20-trial high-rate replay reproduced only the C64-owned boundary
(trial 06 lost 6,400 samples; trial 11 hit `buffer_full`; 18 trials passed).
That failure is recorded in `ci-34663256624-integration-rejection.md`; no
C64-owned source or test path was modified.
