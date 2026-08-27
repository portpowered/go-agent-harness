# S2S v7c — exact session metrics reconciliation

Status: **proven by the bounded hermetic CLI evidence below** (2026-08-26).

The proof compares an independent fold of the raw replay ledger with the
production-owned terminal `SessionRuntimeObservation.FinalAccounting` emitted
by the actual `agent session` command. The terminal value contains the
session-cumulative token totals and the complete production `metrics.Snapshot`,
including supported zero series; neither side is reconstructed from a
rendered duration artifact or from the test's stream observer.

## Evidence delivered in this lease

The focused evidence is
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
`wire.InitializeMockAgentCLIWithPorts`, with the runtime observer installed
through the composed `PortSessionRuntimeObserver` seam. Replay supplies the
transport, so the test needs no credentials and makes no live network request.
The command is also wrapped in a 30-second context deadguard and must reach the
recorded `SESSION.CLOSE` boundary with reason `fixture_complete`.

The temporary capture reuses the committed
`go-agent-loop/testdata/audio/utt_short_16k.wav` corpus file, splitting its
decoded PCM into two output-audio deltas. The normalized stream carries a
non-empty text turn, a two-delta audio turn, usage-bearing `MESSAGE.END`
boundaries, and the final `SESSION.CLOSE` reason `fixture_complete`.

The duration JSONL and WAV files are validated as command-owned output
artifacts, and the explicit `--audio-out` WAV is checked against the reused
corpus PCM. The independent expected side is folded from the replay capture,
including all supported direction/modality zero series and exact usage fields;
the actual side is the captured terminal production snapshot.

## Exact oracle and negative control

The oracle requires the production value to declare incremental usage
semantics. It treats each non-negative `MESSAGE.END` usage value as the
incremental contribution for its observed output-bearing turn and sums prompt,
completion, total, and reasoning fields. A close with no observed output is
retained as a valid boundary but contributes no independently attributable
usage, which makes the missing-output control change the token fold. Each
direction/modality series is initialized before folding and is compared with
exact event count, byte total, histogram bounds, buckets, overflow, sample
count, and byte sum; supported but unobserved series must remain exact zeros.
The oracle requires prompt plus completion to equal total and reports stable
field names with exact expected/actual values, with no tolerance or
non-zero-only shortcut.

The control first verifies a successful CLI replay and a successful comparison,
then changes exactly one condition: **remove one non-empty output-text delta
from the independent observed ledger while leaving the captured production
token counter and metrics snapshot unchanged**. The same exact comparator must
reject the mutation with these values:

```text
token total: expected 8, actual 26
output/text event_count: expected 0, actual 1
output/text total_bytes: expected 0, actual 16
```

The mismatch is caused by the oracle-only missing delta, not replay
divergence, malformed capture data, network access, a panic, or a timeout.

## Offline rerun

From the repository root, run:

```bash
(cd agent-cli && go test ./test/integration -run 'TestSessionCommandMetricsReconcile' -count=1)
```

This runs the bounded public CLI evidence and its expected-failing control
without provider credentials or network access. It does not claim live
provider behavior, sibling-vertical coverage, or approximate reconciliation.
