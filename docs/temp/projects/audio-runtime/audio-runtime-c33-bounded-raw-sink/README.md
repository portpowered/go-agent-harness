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

The runner records exact source/binary revisions and hashes, Go version,
commands, stdout/stderr, exit codes, allocation exclusions, measurements and
clean child shutdown under this owned directory. Runtime replay is an optional
separate command against a same-source public yui executable:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c33-bounded-raw-sink/run.py --source-root <candidate-source> --runtime-regression --yui <exact-yui>
```
