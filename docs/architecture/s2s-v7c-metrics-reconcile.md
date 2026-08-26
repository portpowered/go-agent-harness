# S2S v7c — exact session metrics reconciliation

Status: **in-lease evidence delivered; final proof blocked by a production
snapshot exposure outside this lane's changed-path lease** (2026-08-26).

The lane has delivered the bounded CLI replay and the expected-failing
independent-ledger control, but it does not claim v7c proven. The public
`agent session` route exposes a stream observer and user-facing duration
artifacts, while the production final `metrics.Snapshot` and
`session_metrics` token fields are not exposed through that route. The public
`agent probe run` result also serializes expectation outcomes rather than its
internal metric series. Comparing another fold of a rendered stream would
therefore be self-referential and is intentionally not described as the final
proof.

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
`wire.InitializeMockAgentCLI`; replay supplies the transport, so the test
needs no credentials and makes no live network request. The command is also
wrapped in a 30-second context deadguard.

The temporary capture reuses the committed
`go-agent-loop/testdata/audio/utt_short_16k.wav` corpus file, splitting its
decoded PCM into two output-audio deltas. The normalized stream carries a
non-empty text turn, a two-delta audio turn, usage-bearing `MESSAGE.END`
boundaries, and the final `SESSION.CLOSE` reason `fixture_complete`.

The accounting ledger used by the test is captured directly from the
command's supported `AgentCLI.SetSessionStreamObserver` seam. The duration
JSONL and WAV files are validated as command-owned output artifacts and the
explicit `--audio-out` WAV is checked against the reused corpus PCM. The
independent side is still folded from the replay capture, including all
supported direction/modality zero series and exact usage fields.

## Exact oracle and negative control

The in-lease oracle treats each `MESSAGE.END` usage value as incremental per
completed turn and sums prompt, completion, total, and reasoning fields. It
requires prompt plus completion to equal total. For metrics it groups every
supported direction/modality key and compares event count and byte total with
no tolerance or non-zero-only shortcut.

The control first verifies a successful CLI replay, then changes exactly one
condition: **remove one non-empty output-text delta from the independent
observed ledger while leaving the command-captured ledger unchanged**. The
same exact comparator must reject the mutation with these values:

```text
token total: expected 8, actual 26
output/text event_count: expected 0, actual 1
output/text total_bytes: expected 0, actual 16
```

This is an oracle-mutation control over the CLI-observed stream. It does not
claim that the unchanged ledger is the production final token counter or
metrics snapshot; that distinction is the unresolved contract below.

## Required out-of-lease production contract

To complete v7c, an owner of the production CLI surface must expose one
supported final observation for a real `agent session` or `agent probe run`
execution containing:

1. the production token/accounting totals, including cumulative versus
   incremental usage semantics; and
2. the production `metrics.Snapshot` for every direction/modality series,
   including exact zero series.

The current `SessionRunOptions.MetricsRecorder` and
`SessionRunOptions.Diagnostics` injection points live below the public CLI;
the current probe path consumes them internally and does not emit the
snapshot/token fields in its JSON result. Adding or threading that public
exposure through the session/probe production files is outside this lane's
allowed paths (`agent-cli/test/integration/**`, `docs/architecture/**`, and
additive-only `go-agent-loop/testdata/audio/**`). The operator should file
that production contract as a separate work item, then update this test to
compare the independent raw replay fold with the captured final snapshot.

## Offline rerun

From the repository root, run:

```bash
(cd agent-cli && go test ./test/integration -run 'TestSessionCommandMetricsReconcile' -count=1)
```

This runs the bounded public CLI evidence and its expected-failing control
without provider credentials or network access. It does not claim live
provider behavior, sibling-vertical coverage, or approximate reconciliation.
