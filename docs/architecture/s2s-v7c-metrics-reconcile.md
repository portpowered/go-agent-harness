# S2S v7c — exact session metrics reconciliation

Status: **proven in-repo** (2026-08-26).

The v7c claim is gated on both reconciliation tests passing together. The
positive test proves the complete CLI observation; the expected-failing test
proves that removing one observed delta makes the same oracle reject the run.
Neither test claims live-provider behavior, sibling-vertical coverage, or
approximate reconciliation.

## Reproducible proof surface

The focused proof is
`agent-cli/test/integration/session_metrics_reconcile_test.go`:

- `TestSessionCommandMetricsReconcileMatchesIndependentFoldOverFullSession`
- `TestSessionCommandMetricsReconcileMissingOutputTextDeltaFails`

The tests invoke the shipped Cobra route with normal argv, equivalent to:

```text
agent session --replay <temporary>/metrics_reconcile.session.json \
  --provider grok --model grok-synthetic \
  --audio-out <temporary>/assistant-reply.wav --max-duration 2s
```

The route is the production CLI composition initialized by
`wire.InitializeMockAgentCLI`; the positive-path system under test is the
command, not an internal session runner, probe executor, agent loop, token
counter, or metrics helper. Replay supplies the transport, so the test needs
no credentials and makes no live network request. The test also wraps command
execution in a 30-second context deadguard.

The replay capture is assembled in the test's temporary directory as
`metrics_reconcile.session.json`. It reuses the committed
`go-agent-loop/testdata/audio/utt_short_16k.wav` corpus file, splitting its
decoded PCM into two output-audio deltas. No new audio fixture or raw audio is
embedded in the capture. The normalized stream contains a non-empty text turn,
a two-delta non-empty audio turn, and the final `SESSION.CLOSE` boundary with
reason `fixture_complete`. Successful command output includes
`[session closed: fixture_complete]`; `--max-duration 2s` is only the bounded
guard and is not the expected terminal reason.

## Independent ledger and token oracle

The test retains ordered `StreamMessage` records in two separate ledgers:

1. The fixture ledger is decoded directly from the replay capture.
2. The command ledger is decoded from the CLI-owned normalized duration JSONL
   artifact written beside the temporary capture.

The two ledgers are folded independently before comparison. The fold recognizes
text/transcript deltas, audio deltas, and usage-bearing `MESSAGE.END` records;
it does not derive token values from payload byte counts.

`MESSAGE.END` usage is incremental per completed assistant turn. The token
oracle appends each usage closure that belongs to a turn with an observed
non-empty output payload, then sums these fields exactly:

- `PromptTokens` → token input
- `CompletionTokens` → token output
- `TotalTokens` → token total
- `ReasoningTokens` → token reasoning detail

Each closure must satisfy `PromptTokens + CompletionTokens = TotalTokens`, and
the folded input plus folded output must equal the folded total. The positive
fixture has two usage closures: `(11, 7, 18)` for text and `(3, 5, 8)` for
audio, so the folded totals are input `14`, output `12`, and total `26`.
Closure count and every available token field are compared exactly. There is
no tolerance, rounding, approximation, or nonzero-only shortcut.

## Exact metric oracle

For every key in the cross-product of `metrics.SupportedDirections()` and
`metrics.SupportedModalities()`, the fold records event count and byte total.
Text and transcript deltas are `output/text`; audio deltas are
`output/audio`. The supported but unobserved series are initialized and must
remain exact zero:

| Series | Expected observation in this proof |
| --- | --- |
| `input/audio` | `event_count=0`, `total_bytes=0` |
| `input/text` | `event_count=0`, `total_bytes=0` |
| `input/image` | `event_count=0`, `total_bytes=0` |
| `output/audio` | `event_count=2`, `total_bytes=265500` (the complete decoded PCM from `utt_short_16k.wav`) |
| `output/text` | `event_count=1`, `total_bytes=16` |
| `output/image` | `event_count=0`, `total_bytes=0` |

The command-owned duration artifact is compared with the independently folded
fixture for every row. The test additionally verifies that both the duration
WAV and the explicit `--audio-out` WAV contain exactly the fixture PCM, and
that output text is present. Any difference reports the stable series or token
field plus exact `expected` and `actual` values.

## Missing-delta negative control

The control first runs and reconciles the same CLI observation successfully.
It then changes exactly one condition: **remove one non-empty output-text delta
from the independent observed ledger while leaving captured counters
unchanged**. The command ledger and its captured accounting are not mutated.

The mutated ledger remains structurally valid, but its first text turn no
longer has an attributable output payload, so the shared exact-equality verdict
must reject it with these exact differences:

```text
token total: expected 8, actual 26
output/text event_count: expected 0, actual 1
output/text total_bytes: expected 0, actual 16
```

The test asserts this rejection as an expected failure. A replay divergence,
missing fixture, network access, panic, or timeout is a setup failure—not a
passing negative-control result.

## Offline rerun

From the repository root, run:

```bash
(cd agent-cli && go test ./test/integration -run 'TestSessionCommandMetricsReconcile' -count=1)
```

This command runs both the positive proof and the expected-failing control
without provider credentials or network access. The contract does not extend
to live providers, other S2S verticals, or approximate/threshold-based
accounting.
