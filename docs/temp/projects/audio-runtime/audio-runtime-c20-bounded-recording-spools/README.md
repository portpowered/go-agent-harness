# C20 bounded recording spools

This evidence lease uses only the public recording Wire, `LiveRecorder`, and
`ProviderCaptureSink` interfaces. The consumer intentionally does not import
`internal/evidence` or the CLI. Every subprocess is bounded to 60 seconds and
the verifier kills the owned process group on timeout.

The service defaults are finite and protected even when callers pass no limits:

| Resource | Default byte budget | Default item budget |
| --- | ---: | ---: |
| paired semantic transcript records | 64 MiB | 1,048,576 |
| PCM segments | 64 MiB | 1,048,576 |
| explicit semantic sidecar | 256 KiB | 64 |
| session metadata/log | 4 MiB | 4,096 |
| terminal transcript reserve | 128 KiB | 16 |
| provider capture spool | 64 MiB | 1,048,576 |

Transcript bytes are charged as the sum of both encoded JSONL peers for one
logical observation. PCM bytes are charged after PCM16 encoding. Provider bytes
include the exact `encoding/json` event bytes plus their newline and the
versioned envelope metadata/footer needed for final publication. Queue
admission is separate: draining releases queue backlog but never refunds
cumulative evidence capacity. A provider append reserves its encoded bytes
until commit/discard; discarded pending data is not published, while settlement
controls retain a reserved queue capacity after data overflow. Provider
publication claims its destination and uses no-replace publication, preserving
pre-existing bytes.

Manifest metadata is bounded per field before finalization, and the serialized
manifest plus session log are charged to the metadata budget. If that budget is
exhausted, the recorder drops the optional session log and publishes a minimal
partial manifest when it fits; it never emits an unbounded metadata envelope.
Transcript, audio, and sidecar writes verify complete records and roll back a
short write before a partial JSONL line can be published.

For one normal directory capture, a conservative service-owned temporary bound
is the sum of the live semantic budgets plus the bounded conversation projection
and provider spool. Publication can briefly hold one private staging copy and
one final destination copy; the verifier reports those disk bytes separately
from queue accounting and retained summary heap. `semantic_usage` reports the
current queue (`queue_*`), queue peaks, admission/worker counts, cumulative
transcript/PCM/sidecar/metadata/terminal counters, and the retained summary
charge (`summary_*` and `peak_summary_*`). `provider_usage` reports provider
append reservations separately from committed provider event bytes and items;
the top-level `provider_bytes` is the finalized envelope on disk. `disk_usage`
reports final bytes plus sampled peaks for staging, temporary spools, and their
sum; `finalization` reports elapsed milliseconds and Go allocation deltas. A
zero queue value after finalization therefore does not erase cumulative
evidence or retained-summary measurements. Filesystem allocator slack, external
trace writers, and unrelated long-conversation causes are not claimed by this
slice.

Run the focused public controls with:

```text
python3 verify-public.py --case baseline --revision e4137eba6a6499142f50701609c1149afc71db84
python3 verify-public.py --case normal
python3 verify-public.py --case many-small
python3 verify-public.py --case large-record
python3 verify-public.py --case provider-overflow
python3 verify-public.py --case default-overflow
python3 verify-public.py --case semantic-boundaries
python3 verify-public.py --case encoded-expansion
python3 verify-public.py --case overflow-healthy-terminal
python3 verify-public.py --case summary-only-overflow
python3 verify-public.py --case provider-boundaries
python3 verify-public.py --case provider-settlement
python3 verify-public.py --case provider-overflow-controls
python3 verify-public.py --case cleanup-failures
python3 verify-public.py --case publication-resources
python3 verify-public.py --case composition
python3 verify-public.py --case audio-tool
python3 verify-public.py --case interruption
python3 verify-public.py --case ask
```

`default-overflow` passes no limit flags; its large paced records exhaust the
service's finite zero-request transcript default and must publish bounded
partial evidence with a causal budget error.

`semantic-boundaries` runs both an exact 500-byte paired-record boundary and an
exact one-item boundary. `encoded-expansion` includes quotes, backslashes and
newlines, so its limit is charged against the escaped paired JSONL bytes.
`summary-only-overflow` deliberately exhausts only the C19 retained projection;
its raw transcript, PCM and provider artifacts remain available below their
independent limits. Provider boundary/settlement cases publish only committed
sequences, while provider overflow controls settle the admitted prefix after
the append budget latches. `cleanup-failures` repeats both finalizers and
requires stable idempotent results. The remaining named cases are explicit
public composition, publication, audio/tool, interruption, and ask controls;
each has a distinct event shape and assertion in the verifier rather than
silently falling back to the normal case.
