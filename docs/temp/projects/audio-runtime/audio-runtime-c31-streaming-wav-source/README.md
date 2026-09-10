# C31 streaming WAV source

This evidence directory is owned by `audio-runtime-c31-streaming-wav-source`.
The production lease is limited to the three source files and the focused test
file named by `prd.json`; this directory owns the exported-API consumer and
bounded evidence runner.

The first characterization uses standard 44-byte-header, 16 kHz mono PCM16 WAV
fixtures with 4096 and 4194304 payload bytes. It warms `audio.NewFileSource`,
then records five constructor/close `runtime.MemStats.TotalAlloc` deltas for
each fixture without reading the payload. The frozen oracle is 65536 bytes for
every measured constructor and 16384 bytes for median large-minus-small growth.
The same consumer checks the public `audio.NewWAVSource` metadata/read bounds:
open reads at most 64 bytes and no payload, `ReadSamples(7)` reads exactly 14
payload bytes, and one `ReadFrame` reads at most `FrameSize*2` payload bytes.
Its JSON report preserves the exact header/data read ranges, metadata seek count,
per-operation seek counts, and one-close-per-source counts; the oracle asserts
those traces as well as their byte totals.
It also checks that `NewFileSource` keeps its historical path-level
`FormatError`/`wavio.UnsupportedError` identity for a valid but incompatible
44.1 kHz WAV, while direct `NewWAVSource` rate validation remains intact.

Run commands from the isolated worktree root:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c31-streaming-wav-source/run.py --build --source-root .
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c31-streaming-wav-source/run.py --characterize --label before
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c31-streaming-wav-source/run.py --positive
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c31-streaming-wav-source/run.py --negative-control
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c31-streaming-wav-source/run.py --workflow
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c31-streaming-wav-source/run.py --regression
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c31-streaming-wav-source/run.py --timeout-control
```

The pre-fix characterization is intentionally a recorded failing oracle; the
runner succeeds only when it observes that failure. After the source repair,
the identical oracle must pass. Generated binaries, process records, and JSON
reports record exact source identity and are retained as task evidence when
they are committed. `--timeout-control` uses a deterministic POSIX child that
detaches while holding inherited pipes; it records the signal, bounded reap,
pipe closure, and wall-time evidence without waiting for the detached holder.
The committed `timeout-control.json` is the exact-source summary; detailed
stdout/stderr and cleanup records remain under the ignored `runs/` evidence.

The JSON reports identify the clean merged implementation revision that was
tested as `tested_source_revision`
(`ce9a795192bc5433811de4f37824cc579fa0daa8`) and include a SHA256 over their
scoped build inputs.
The final evidence checkpoint may be a docs-only descendant of that revision;
the descendant is valid only when `git diff --name-status` shows evidence paths
and the recorded build-input hashes are unchanged. This separates executable
provenance from the later evidence ledger commit.

`characterize-before.json` records the unfixed pre-repair source
(`1f82284abee0bd31a6680310444cea2e4c16ef00`): the 4096-byte fixture measured
74136 bytes per constructor and the 4194304-byte fixture measured a median
26624552 bytes (maximum 26629840), so the frozen oracle failed. The repaired
run keeps both fixture maxima under 65536 bytes and median growth at zero; the
exact measurements and merged-source identity are in `characterize-after.json`.

The file-input workflow is launched with the same-source `yui` binary and a
derived, integrity-sealed copy of the shipped credential-free
`agent-cli/test/integration/testdata/s2s-e2e-vision-describe/s2s_e2e_vision_describe.session.json`
fixture. The literal input WAV is 16 kHz mono PCM16 with samples
`[-32768,-12345,-1,0,1,12345,32767]` (14 payload bytes); the public file-input
path records one 960-byte `FrameSize*2` frame with the remaining bytes zero
padded, emits the deterministic scripted response, and exits with
`fixture_complete`. `workflow.json` records the exact fixture, input-frame,
recording, output, and clean-shutdown hashes. The runner compares the emitted
WAV PCM payload byte-for-byte with the expected 480-sample response and records
both expected and observed payload hashes. This is software replay evidence,
not acoustic or physical-device proof.

The regression mode reuses the existing C21 verifier controls read-only. It
launches both shipped capture-to-recorded-bundle replay cases and asserts exact
PCM (`4800` bytes / SHA256
`0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502` and
`3840` bytes / SHA256
`6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22`),
provider event ordering, tool marker/content, terminal outcomes, manifest
hashes, and the 2400-byte healthy interruption tail. `regression.json` records
the exact read-only controls and outputs.

After opening, the WAV header and physical extent are fixed but payload bytes
are intentionally streamed from the owned file descriptor: an in-place rewrite
can be observed by a later read, while a post-open physical truncation reports
`*audio.TruncatedPCMError{Bytes: 1}` on the one-byte first affected read and
terminal EOF on later reads. Snapshot isolation is not promised; the constructor
still rejects an already-truncated file before any payload read.
