# S2S v5c — Toolset None contract status

Status: **blocked on the implementation base; not proven by this lane** (2026-08-25).

The v5c proof requires one hermetic behavior matrix through the shipped CLI:
the same replayed model tool call must be refused with `--tools none` and
executed exactly once with the default toolset. The refusal must identify the
call ID and tool name, while replay must validate the advertised tool
definitions and any outbound tool result.

## Base-contract gap

The public realtime session command currently exposes `--record` and `--replay`
but no `--tools` selection or documented session configuration equivalent.
`SessionRunOptions` has no session tool executor/definition inputs, and the
session runtime constructs the loop without a tool executor or a public
disabled-mode selection. The internal
`go-agent-loop.WithToolExecutionDisabled()` option therefore cannot be reached
through the customer-facing session command.

The alternative public `agent probe run` command is not a substitute on this
base: its replay executor observes raw fixture frame count, outbound ticks, and
terminal state only. It has no tool executor, tool-definition assertion,
correlated refusal, or tool-result observation.

The missing upstream contract is session-runtime tool wiring plus a public
selection for active versus disabled tools and structured refusal evidence.
Those production paths are outside this lane's changed-path lease
(`agent-cli/test/integration/**`, `docs/architecture/**`, and additive audio
fixtures), so this lane cannot implement them without bypassing the public CLI
or changing an out-of-lease API.

## Follow-up proof contract

Once the upstream session contract is available, the focused offline proof
should:

1. run the real CLI router with the same replay capture and
   `go-agent-loop/testdata/audio/tool_request_16k.wav` (or the 24 kHz sibling)
   in both rows;
2. assert zero advertised definitions, one correlated refusal, zero executor
   calls, and no tool result in the disabled row;
3. assert the harmless tool definition, one executor call, and the known
   correlated result in the default-active row; and
4. apply the disabled assertion to that default-active observation as the
   negative control, expecting it to fail because execution and a result are
   present.

The current baseline replay checks can be rerun offline with:

```text
go test ./agent-cli/test/integration -run 'TestSessionCommand_HelpDocumentsRecordReplayAndHistorySubcommands|TestProbeRunAllPassExitZero' -count=1
```

That command validates the existing CLI/replay seams only; it is not v5c proof
and makes no live-network claim. No new audio fixture is justified until the
missing session tool path can consume the committed tool-request fixture.
