# C42 capture-energy software coverage

This evidence directory is the credential-free software gate for
`audio-runtime-c42-capture-energy-software-coverage`. It is bound to the
admitted project manifest and the isolated branch
`codex/audio-runtime-c42-capture-energy-software-coverage`.

The consumer is a separate Go module. Run its build from its own directory so
the module boundary is explicit and reproducible:

```sh
cd docs/temp/projects/audio-runtime/audio-runtime-c42-capture-energy-software-coverage/consumer
GOWORK=off go test -run '^$' -count=1 ./...
GOWORK=off go build -o /tmp/audio-runtime-c42-consumer ./...
```

From the repository root, run the positive software gate with explicit source,
consumer, and YUI inputs:

```sh
python3 docs/temp/projects/audio-runtime/audio-runtime-c42-capture-energy-software-coverage/run.py \
  --mode software \
  --source "$PWD" \
  --consumer /tmp/audio-runtime-c42-consumer \
  --yui /absolute/path/to/yui
```

The negative controls use the same explicit inputs:

```sh
python3 docs/temp/projects/audio-runtime/audio-runtime-c42-capture-energy-software-coverage/run.py \
  --mode negative-control \
  --source "$PWD" \
  --consumer /tmp/audio-runtime-c42-consumer \
  --yui /absolute/path/to/yui
```

`run.py` verifies all 40 C32 literal consumer oracles plus the C42 matrix,
requires unchanged input bytes and no panic, runs deterministic output-cap and
SIGKILL-reap controls, and writes bounded run details under the ignored
`runs/` directory. Software mode also replays the retained audio/tool and
interruption fixtures, checks the marker and exact rendered/provider PCM
hashes, checks clean trace termination, and verifies directory replay. The
YUI fixture provider is replay-only; no live provider, Realtime connection,
physical device, or acoustic result is claimed. Native Windows endpoints and
physical/acoustic testing are `OUT OF SCOPE` under the effective 2026-09-10
user amendment, not a prerequisite for software delivery.

The original C32 expected oracle is retained byte-for-byte as
`c32-original-expected.json` (SHA-256
`d209639bf6cf51c61fe2931eed9e50586fae1824334daa3239af8cc841a47f34`). The
original C32 vertical report is retained as
`c32-original-failed-report.json` (SHA-256
`0345a628a6038e18bf7c7a59016e7a0002ae89734c6ff59aa541642f34b3d960`). The
historical native WASAPI/acoustic result remains unchanged; under the effective
`factory/docs/operating-policy.md` user scope amendment, native endpoints and
physical/acoustic proof are `OUT OF SCOPE`, never PASS.
