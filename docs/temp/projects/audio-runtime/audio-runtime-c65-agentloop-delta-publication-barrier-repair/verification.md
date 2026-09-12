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

## Handoff recheck

At `2026-09-12T02:04:55Z`, admission was reverified with
`project-control.py verify-work --type task --name audio-runtime-c65-agentloop-delta-publication-barrier-repair`.
The isolated branch remains
`codex/audio-runtime-c65-agentloop-delta-publication-barrier-repair`, the
candidate is clean at `1d4fb218a904b508377278da37779743d6b39e67`, and freshly
fetched `origin/main` remains the accepted `d5d6f84363d8569d5dc1a59985f8d45cf50e1d06`
ancestor. The existing PR #455 is open at that exact head with no independent
review findings.

The post-checkpoint bounded recheck passed:

```text
go test ./go-agent-loop/pkg/agentloop -run '^TestRun(JoinsPublishedDeltasBeforeReturningOnEngineError|CancellationReleasesBlockedDeltaForwarder)$' -count=100 -timeout=120s  # pass, 0.194s
go test -race ./go-agent-loop/pkg/agentloop -run '^TestRunJoinsPublishedDeltasBeforeReturningOnEngineError$' -count=20 -timeout=120s  # pass, 1.393s
go test ./go-agent-loop/pkg/agentloop -count=1 -timeout=180s  # pass
go test ./agent-cli/test/integration -run '^Test(SessionCommand_DefaultRegistryExecRoundTripInStrictOpenAIReplay|AgentBinaryToolContinuationPreservesRemoteDeviceAudio)$' -count=1 -timeout=180s  # pass, 91.576s
```

C64 remains an unavailable delivery prerequisite: its separate PR #459 is
still open at `996f69fcb2b1ddb002ff449d810f984b6ac46643` and has not landed on
accepted main. Do not resubmit this unchanged C65 head to script CI while the
known C64-owned 6,400-sample integration failure remains in the accepted-main
line. Next action is to fetch the reviewed/guarded C64 merge once it lands,
rerun the focused C65 checks and required shipped regression against that exact
main, then update and submit PR #455 to the script CI gate without polling.

## Current merged-main recheck

The preceding handoff section is historical. C64 is now reviewed and merged as
59af6325614d80173447fe2018a0471e27b4e7b1, which is the freshly fetched
origin/main. The isolated branch was merged with that accepted main without
touching the running host checkout, producing local integration commit
7e435abf57bcef3006a0a697b71c707bb140fddb. Required ancestry checks pass for
startup integration 8bdafc7f947a3a2c9856220abdc539437035bd21, planning main
d5d6f84363d8569d5dc1a59985f8d45cf50e1d06, C64 merge 59af6325, and
origin/main. Relative to origin/main, the candidate changes only the C65
owned AgentLoop/test/evidence paths; buffer_capacity_test.go remains
unchanged.

The latest prior hosted rejection is preserved in
ci-34666727240-integration-rejection.md. It failed only the C64-owned
slow-device high-rate terminal-drain test. On the merged-main candidate, the
required shipped high-rate control passed all 20 trials in each normal,
coverage, and race mode in scripts/test-session-ci-regressions.sh with
COUNT=1.

Post-merge focused and accumulated evidence:

    go test ./go-agent-loop/pkg/agentloop -run '^(TestRunJoinsPublishedDeltasBeforeReturningOnEngineError|TestRunCancellationReleasesBlockedDeltaForwarder)$' -count=100: 200 passed
    go test -race ./go-agent-loop/pkg/agentloop -run '^TestRunJoinsPublishedDeltasBeforeReturningOnEngineError$' -count=20: 20 passed
    CGO_ENABLED=0 GOMAXPROCS=64 go test ./go-agent-loop/pkg/agentloop -run '^TestRunJoinsPublishedDeltasBeforeReturningOnEngineError$' -count=2000: 2000 passed
    capacity controls -count=20: 120 passed
    go test ./go-agent-loop/pkg/agentloop -count=1: 59 passed
    go test -race ./go-agent-loop/pkg/agentloop -count=1: 59 passed
    go test ./agent-cli/test/integration -run '^(TestSessionCommand_DefaultRegistryExecRoundTripInStrictOpenAIReplay|TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio)$' -count=1: 14 passed
    scripts/test-session-ci-regressions.sh all with COUNT=1: normal, coverage, and race exited 0

The causal test still asserts literal delta content, delivery before the
terminal error, original *messages.ErrorValue identity, and
errors.Is/errors.As; the full-buffer cancellation control remains green.
The local quality gates all pass on this merged source: make fmt,
make verify-architecture (186 packages, 1896 files, 28041 functions),
make coverage-registration (176 packages across six modules), make vet,
pinned golangci-lint 2.9.0 with zero issues, pinned staticcheck 2026.1,
and COVERAGE_BASE=d5d6f84363d8569d5dc1a59985f8d45cf50e1d06 make
coverage-changed (176 registered packages across seven profiles; the
agent-cli target-wide run completed in 5m19.465s with 2m40.535s headroom).
Wire regeneration left generated files unchanged and git diff --check is
clean.

PR #455 remains open and has no independent review. The current local merge
candidate is implementation evidence only: it has not yet been pushed, sent
to the new script-CI run, reviewed, merged, or vertically probed. Next action
is to commit this evidence update, push the changed same branch, update PR
#455, and return ACCEPTED to SCRIPT CI without polling; retain C65 ownership
for any exact current-head rejection.
