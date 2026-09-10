# C33 bounded raw PCM sink

This evidence lease uses only the public `audio.NewFileSink("-", writer)`
consumer. The consumer allocates and fills the two large sample inputs before
measurement, uses a writer that counts bytes/calls/max request and hashes input
without retaining output, and derives expected PCM hashes from a literal
little-endian oracle rather than `codec.EncodePCM16Into`.

The raw sink scratch ceiling is documented as 64 KiB (32,768 PCM16 samples).
The measured per-call allocation budget is 256 KiB, including fixed runtime and
interface overhead but excluding caller-owned sample input and the writer's
non-retaining hash state. Valid local PCM output has no remote codec payload cap.

Each public consumer run also receives a fresh, previously absent raw-file path.
It writes the literal three-sample tail `0080ff7fffff` through
`NewFileSink(path, nil)`, closes twice, reads the resulting file, and verifies
the exact byte count, full hash, and final tail. The same consumer checks that a
caller-owned `NewFileSink("-", writer)` writer is not closed and remains writable
after repeated sink close.

Characterize the immutable pre-fix source:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c33-bounded-raw-sink/run.py --source-root <isolated-before-source> --build --mode characterize
```

Verify the candidate and its independent negative oracle:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c33-bounded-raw-sink/run.py --source-root <candidate-source> --build --mode verify
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c33-bounded-raw-sink/run.py --source-root <candidate-source> --mode verify --negative-control
```

The old whole-buffer candidate is expected to fail the resource budget:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c33-bounded-raw-sink/run.py --source-root <isolated-before-source> --build --mode verify --expect-resource-failure
```

Every report records the exact tested source commit/tree and a hash of its
tracked build-input manifest. Public-consumer reports also record the consumer
source, runner, source-module, generated-module, and binary hashes; a reused
binary is rejected unless its input sidecar matches those hashes. This keeps an
evidence-only descendant distinguishable from the source that was actually
tested. The runner also executes a deterministic escaped-descendant timeout
control: TERM, KILL, bounded parent reap, pipe closure, and descendant exit
must all be observed before a report can pass.

Runtime replay is an optional separate command against a public yui executable
built from the same source revision. Pass both the executable and the source
root used to build it:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c33-bounded-raw-sink/run.py --source-root <candidate-source> --runtime-regression --yui <exact-yui> --yui-source-root <candidate-source>
```

When a report is committed after capture, compare its recorded
`tested_source.revision` with the final head and inspect the descendant diff;
the executable inputs must remain unchanged. The reports retain commands,
stdout/stderr, exit codes, allocation exclusions, measurements, and clean
shutdown evidence under this owned directory.
