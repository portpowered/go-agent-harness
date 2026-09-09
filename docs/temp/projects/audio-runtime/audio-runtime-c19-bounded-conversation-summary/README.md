# C19 bounded conversation summary

This evidence lease repairs one retained-memory cause in the public recording
service: the in-memory conversation convenience projection. The standalone
consumer in `consumer/main.go` uses only `recording/wire.NewService` and the
public `session.LiveRecorder` interface. It does not use CLI, recording
internals, Realtime, credentials, devices, or C18 replay/embedding paths.

## Explicit residual limits

The projection has two independent limits:

- `2 MiB` of conservative retained-summary accounting (`directorySummaryMaxBytes`).
- `4096` retained projection items (`directorySummaryMaxItems`).

The byte unit is an upper-bound accounting unit, not a promise about Go heap
size: each retained string is charged at `6 * len(input bytes) + 32` to cover
the maximum `encoding/json` escape expansion plus a string header; fixed charges
cover turn/event records, map entries, slice entries, field names and a
per-turn JSON encoding allowance. Charges are reserved before retaining input,
and transcript snapshot replacement releases the old charge before accepting
the replacement. `int64` overflow-safe checks reject a reservation that would
cross either limit.

The admission queue is a separate `16 MiB` byte budget and its bytes are
released after drain. Raw transcript JSONL, PCM files, provider capture and
destination/spool disk are authoritative artifacts and are not charged to the
summary budget; this change does not claim to bound disk growth. The consumer
reports queue events/payload bytes separately from forced-GC retained-heap
samples.

When the projection cannot reserve another item, recording latches one stable
`io.ErrShortBuffer`-compatible budget error, publishes the accepted summary
prefix with partial status, and continues recording raw transcript/PCM and the
terminal event. A partial summary is not a full replay certificate. Finalize is
idempotent and returns the same error on repeated calls.

## Reproduction and verification

The repository `go.work` intentionally lists source modules, not `docs/temp`.
Build the consumer from the evidence directory with the source-file command:

```text
rtk go build -o docs/temp/projects/audio-runtime/audio-runtime-c19-bounded-conversation-summary/summary-consumer docs/temp/projects/audio-runtime/audio-runtime-c19-bounded-conversation-summary/consumer/main.go
```

Then run the bounded cases (each consumer watchdog is 50 seconds):

```text
rtk proxy docs/temp/projects/audio-runtime/audio-runtime-c19-bounded-conversation-summary/summary-consumer --case normal --output docs/temp/projects/audio-runtime/audio-runtime-c19-bounded-conversation-summary/normal.json
rtk proxy docs/temp/projects/audio-runtime/audio-runtime-c19-bounded-conversation-summary/summary-consumer --case overflow --output docs/temp/projects/audio-runtime/audio-runtime-c19-bounded-conversation-summary/overflow.json
rtk proxy docs/temp/projects/audio-runtime/audio-runtime-c19-bounded-conversation-summary/summary-consumer --case matrix --output docs/temp/projects/audio-runtime/audio-runtime-c19-bounded-conversation-summary/matrix.json
rtk proxy docs/temp/projects/audio-runtime/audio-runtime-c19-bounded-conversation-summary/summary-consumer --case characterize --output docs/temp/projects/audio-runtime/audio-runtime-c19-bounded-conversation-summary/after.json
```

The public verifier runs one of those same binaries, captures stdout/stderr,
checks normal parity or observable overflow/raw-tail behavior, and checks the
characterization checkpoints plus no-recording controls:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c19-bounded-conversation-summary/verify-public.py --case normal --consumer docs/temp/projects/audio-runtime/audio-runtime-c19-bounded-conversation-summary/summary-consumer
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c19-bounded-conversation-summary/verify-public.py --case overflow --consumer docs/temp/projects/audio-runtime/audio-runtime-c19-bounded-conversation-summary/summary-consumer
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c19-bounded-conversation-summary/verify-public.py --case characterize --consumer docs/temp/projects/audio-runtime/audio-runtime-c19-bounded-conversation-summary/summary-consumer
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c19-bounded-conversation-summary/verify-public.py --case matrix --consumer docs/temp/projects/audio-runtime/audio-runtime-c19-bounded-conversation-summary/summary-consumer
```

The matrix case runs the normal and overflow scenarios with independent recorder
lifecycle/finalization state, so each contract is checked against its own result.

The pre-fix and candidate runs use the same public source, finite checkpoints
of 16/64/128/256/512 completed turns, deterministic clock and fixture, drained
queue, forced-GC live samples, shutdown allocation and shutdown elapsed time.
The pre-fix report is `before.json`; the candidate report is `after.json`.
The source revision, executable SHA-256 and fixture hashes are recorded in the
adjacent evidence manifest once the candidate checkpoint is committed.

## Focused checks

```text
rtk go test ./go-agent-runtime/services/recording/... -count=1 -timeout=60s
rtk go test -race ./go-agent-runtime/services/recording/... -count=1 -timeout=60s
rtk go vet ./go-agent-runtime/services/recording/...
rtk git diff --check
```

The broader current-head CI gate and independent review remain script-owned;
this evidence does not claim CI green or close the project-wide AUDIO, DEVICE,
EMBED, SERVICE, TRACE, REPLAY, FAILURES, QUALITY, or PARITY gates.
