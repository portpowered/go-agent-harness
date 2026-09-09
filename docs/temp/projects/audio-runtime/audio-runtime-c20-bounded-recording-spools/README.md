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
are the exact `encoding/json` event bytes plus their newline. Queue admission is
separate: draining releases queue backlog but never refunds cumulative evidence
capacity. A provider append reserves its encoded bytes until commit/discard;
discarded pending data is not published, while settlement controls retain a
reserved queue capacity after data overflow.

For one normal directory capture, a conservative service-owned temporary bound
is the sum of the live semantic budgets plus the bounded conversation projection
and provider spool. Publication can briefly hold one private staging copy and
one final destination copy; the verifier reports those disk bytes separately
from queue accounting and retained summary heap. Filesystem allocator slack,
external trace writers, and unrelated long-conversation causes are not claimed
by this slice.

Run the focused public controls with:

```text
python3 verify-public.py --case baseline --revision e4137eba6a6499142f50701609c1149afc71db84
python3 verify-public.py --case normal
python3 verify-public.py --case many-small
python3 verify-public.py --case large-record
python3 verify-public.py --case provider-overflow
python3 verify-public.py --case default-overflow
```

`default-overflow` passes no limit flags; its large paced records exhaust the
service's finite zero-request transcript default and must publish bounded
partial evidence with a causal budget error.
