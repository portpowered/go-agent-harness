# S2S v7a metrics-modality proof

Status: implemented on this branch. The v7a lane proves exact audio, text, and
tool per-modality reconciliation for one credential-free, hermetic CLI session,
with a negative control that demonstrably fails on an injected mismatch.

## What the lane proves

One replayed session (text in, audio out, one streamed tool call) emits
per-direction/per-modality metric series whose totals reconcile exactly with
the summed observed delta stream:

| Series | Observed deltas | Source |
| --- | --- | --- |
| `input/text` | 29 (`conversation.item.create` input_text prompt bytes) | send_text step seeded as the session prompt |
| `output/audio` | 6 (base64-decoded `response.audio.delta` PCM) | fixture wire stream |
| `output/text` | 21 (`response.audio_transcript.delta`) | fixture wire stream |
| `output/tool` | 16 (two `response.function_call_arguments.delta` fragments; the terminal `arguments.done` is not double-counted) | fixture wire stream |

The two sides are derived by genuinely independent code paths:

- **Emitted side** — the production observation seam. The real session runner
  replays the committed fixture with a `metrics.Recorder` injected;
  `sessionProgressObserver.account` forwards every counted byte exactly once
  and the recorder's terminal snapshot is the emitted metric matrix.
- **Observed side** — a wire-level sum over the same fixture's raw records
  (`agent-cli/internal/cli/probe_metrics.go`), which mirrors the observer's
  accounting rules without sharing its code.

Reconciliation runs inside probe expectation evaluation as the measurable kind
`metrics-reconcile` (`go-agent-loop/pkg/probe/expect.go`). Any missing,
duplicated, misattributed, or stale non-zero series fails with the series name
and both values.

## Negative control

Registered scenario `s2s-v7a-metrics-modality-overcount` declares the same
expectations but its execution seam injects a precise fault: the emitted
`output/tool` total is reported as the observed delta sum plus one. Running it
through the public CLI must exit non-zero with a failed outcome of kind
`metrics-reconcile` whose expected-vs-actual detail names `output/tool`
(observed sum 16 vs reported total 17) while the untouched transcript
expectation still passes. Restoring exact equality — i.e. running the positive
case `s2s-v7a-metrics-modality` — passes.

## Committed evidence and commands

Fixture: `go-llm-gateway/pkg/testing/testdata/session-fixtures/s2s-v7a-metrics-modality.session.json`
(provenance `synthetic_failure`: the runtime has no tool executor, so the
provider tool call stays unexecutable; this provenance exempts it from the
healthy-session tool-call/result pairing rule).

```text
# Positive case: exits 0, JSONL result line all-pass.
agent probe run --replay go-llm-gateway/pkg/testing/testdata/session-fixtures/s2s-v7a-metrics-modality.session.json \
  --json --scenario s2s-v7a-metrics-modality

# Negative control: exits 1 naming metrics-reconcile with expected vs actual.
agent probe run --replay go-llm-gateway/pkg/testing/testdata/session-fixtures/s2s-v7a-metrics-modality.session.json \
  --json --scenario s2s-v7a-metrics-modality-overcount

# Focused integration tests covering both cases end to end.
go test ./agent-cli/internal/cli -run 'TestProbeRunS2SV7A'
```

## Production surfaces touched

- `go-agent-loop/pkg/metrics` — `ModalityTool` joins audio/text/image; the
  conformance suite covers the extended set through `orderedSeriesKeys`.
- `agent-cli/internal/services/session_diagnostics.go` — tool-call argument
  deltas counted at the single accounting seam, per-turn record gains
  `output_tool_bytes`, provider `MESSAGE.END` usage accumulated, and a terminal
  `session_metrics` matrix record emitted once per run after the last delta.
- `docs/architecture/s2s-session-diagnostic-contract.md` — contract rows for
  the new event, field, and series.

No claim is made here about v1 text-in/audio-out, v2a basic audio-input, v6a
authentication-error, or v7c cross-session/provider reconciliation lanes.
