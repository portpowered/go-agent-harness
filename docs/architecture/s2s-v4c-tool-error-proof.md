# s2s vertical v4c — tool-call errors surface on the delta stream

Status: **proven in-repo** (2026-08-24) by a hermetic, CLI-driven integration
lane with no network and no credentials.

## What is proven

`TestFailingToolCallEmitsTypedDeltaErrorAndSessionSurvives`
(`agent-cli/test/integration/tool_error_test.go`) drives the real `agent-cli
ask` command over the record/replay HTTP transport with the committed fixture
`agent-cli/test/integration/testdata/tool_error_read_file.json`. The fixture
records an OpenAI-compatible SSE transcript in which the model issues a tool
call to `v4c_unknown_tool`, which is not in the tool registry. The test
asserts:

1. **Typed tool-error on the delta stream.** With `--stream --output-json`,
   each delta event is emitted as one NDJSON line. The observed stream must
   contain an `ERROR` event whose value has `type:"error"` and whose message
   identifies the failed tool call (`tool "v4c_unknown_tool" failed: ...`).
2. **Session survival.** The CLI process terminates normally with a zero exit
   code — no panic anywhere in stdout/stderr and no hang (a 60s deadguard
   bounds the run).
3. **Hermetic replay.** The only model traffic is served from the recorded
   fixture via `--replay`; no network dial occurs.

## Negative controls

The assertions discriminate rather than pass vacuously:

- **Panic control**
  (`TestNegativeControlUnhandledPanicDetectedByParent`): re-runs the test
  binary as a subprocess whose tool executor panics inside the tool path. The
  scenario must fail fast with an explicit `panic:` report identifying the
  panicking execution — not a timeout.
- **Suppressed-error control**
  (`TestNegativeControlSuppressedToolErrorFailsScenario`): runs the identical
  CLI surface with an executor that swallows the failure and reports success.
  The typed-event assertion must fail with the message
  `missing expected typed tool-error event ...`, so silent swallowing can
  never pass CI.

## Re-running offline

```
cd agent-cli
go test ./test/integration -run 'TestFailingToolCall|TestNegativeControl' -v
```

The committed audio corpus under `go-agent-loop/testdata/audio/` is not
exercised here: this vertical is text/tool-call scoped and carries no audio
content; no new audio fixtures were required.
