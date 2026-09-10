# C26 source characterization

The admitted candidate started from freshly fetched `origin/main` at
`98ce636dd67349ba64f22cd7916dd370cf4ba484` on branch
`codex/audio-runtime-c26-shared-pcm-mixing`. Required ancestry checks passed for
startup integration `8bdafc7f947a3a2c9856220abdc539437035bd21`, migration baseline
`3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`, and the fetched main revision.

Before extraction, the exact source hashes were:

| source at 98ce636 | SHA-256 |
| --- | --- |
| `go-audio/pkg/mixer/mixer.go` | `2d2d55babf90aeaf830f0df450cb90a3885b8a49cc61c3e041f86df62eb24f0d` |
| `agent-cli/internal/room/mixer.go` | `21a1ade23f4f2ef9b2121be902ad73b1ae78f7e078a8769043c6c23ee6b515ce` |

The pre-extraction commands were:

```text
git fetch origin main
git rev-parse origin/main
git show 98ce636dd67349ba64f22cd7916dd370cf4ba484:go-audio/pkg/mixer/mixer.go
git show 98ce636dd67349ba64f22cd7916dd370cf4ba484:agent-cli/internal/room/mixer.go
go test ./go-audio/pkg/mixer -count=1 -timeout 60s       # 15 passed
go test ./agent-cli/internal/room -run 'TestPCM16Mixer|TestPCMMix' -count=1 -timeout 60s  # 17 passed
```

Both production callers independently accumulated `int32` sample totals and
clipped after the source loop. The canonical mixer selected the maximum
contributing sample length, retained short terminal tails at that length, and
emitted a full frame only for silence; the legacy room mixer always encoded a
full cadence frame, zero-padding short input. Both sorted IDs before collecting
source attribution. Empty inputs contributed silence, and a non-empty all-zero
input remained attributed in the legacy source list.

The deterministic boundary vectors preserved by the candidate are:

| vector | expected final output | intermediate-clip control |
| --- | --- | --- |
| `32767 + 32767 - 32768` | `32766` | `-1` |
| `-32768 - 32768 + 32767` | `-32768` | `32767` |
| sources `{1,2,3}` and `{4}` with four-sample legacy output | `{5,2,3,0}` | omitted tail is incorrect |
| boundary-only canonical input | empty samples plus `EndOfResponse` | invented silence is incorrect |

The old bounded `int32` arithmetic had these source-count edges for one-sample
inputs. The shared operation now uses `int64` and clips only once, so every
listed count is supported exactly before clipping; `MaxPCM16MixSources` is the
explicit first rejected bound at `1,048,577` sources.

| sources | old int32 total behavior | shared operation |
| ---: | --- | --- |
| `65,536 × -32768` | fits at `-2147483648` | exact, clips to `-32768` |
| `65,537 × -32768` | wraps positive before clipping | exact, clips to `-32768` |
| `65,538 × 32767` | fits at `2147483646` | exact, clips to `32767` |
| `65,539 × 32767` | wraps negative before clipping | exact, clips to `32767` |

The operation bounds its requested output to `MaxPCM16MixSamples` (`1,048,576`)
and rejects a source longer than the requested output instead of silently
dropping its tail. It allocates only the returned frame and a temporary
`int64` accumulator, retains no input, and does not know about codecs, devices,
clocks, metadata, or lifecycle. The caller retains responsibility for source
order/attribution and chooses the output length that defines short-tail versus
full-cadence behavior.
