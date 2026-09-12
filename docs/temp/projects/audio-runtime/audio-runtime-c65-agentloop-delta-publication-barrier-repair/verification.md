# C65 verification checkpoint

## Scope

Candidate source/test checkpoint: `86aa7f1c4120ee3921fc556378f9a03277700b2b`.

Only the admitted task-owned paths changed:

- `go-agent-loop/pkg/agentloop/agent_loop.go`
- `go-agent-loop/pkg/agentloop/run_delta_barrier_test.go`
- this task-owned evidence directory

The production adjustment is confined to `AgentLoop.Run`: the finish path cancels the internal loop context before joining `forwardDone`; the independent forwarding context remains available to drain already-published kernel deltas and caller cancellation still releases a full public buffer.

## Causal proof

`TestRunJoinsPublishedDeltasBeforeReturningOnEngineError` now:

1. queues provider `SESSION.OPEN` and then a text delta;
2. waits on the exact `KernelRunner: sending text delta` signal, emitted after the reader-channel publication;
3. queues the structured terminal error only after that signal;
4. fills the public delta buffer so the forwarder is blocked when the engine reports the error;
5. proves `Run` has not returned, releases the bounded backlog, and verifies the exact text delta remains readable after `Run` returns;
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
