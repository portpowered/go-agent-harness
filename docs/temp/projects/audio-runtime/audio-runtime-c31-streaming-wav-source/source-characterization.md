# C31 source and evidence ledger

Task: `audio-runtime-c31-streaming-wav-source`  
Branch: `codex/audio-runtime-c31-streaming-wav-source`  
Isolated worktree: `/Users/abdifamily/.codex/worktrees/af44/go-agent-harness/.claude/worktrees/audio-runtime-c31-streaming-wav-source`

Admission used the sole `audio-runtime` project manifest and the exact
`~default` board task. The task had no C31 review or CI rejection feedback at
admission. The predecessor and baseline checkpoints were preserved:

| checkpoint | revision |
| --- | --- |
| startup integration revision | `8bdafc7f947a3a2c9856220abdc539437035bd21` |
| required baseline | `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad` |
| freshly fetched `origin/main` | `1f82284abee0bd31a6680310444cea2e4c16ef00` |
| pre-change isolated HEAD | `1f82284abee0bd31a6680310444cea2e4c16ef00` |

`git merge-base --is-ancestor` passed for the startup integration revision,
the required baseline, and fetched `origin/main`. No merge or reset was used;
the running host checkout was not touched.

## Frozen characterization

The evidence-local public consumer creates standard 44-byte-header, 16 kHz
mono PCM16 WAV fixtures with 4096 and 4194304 payload bytes, warms
`audio.NewFileSource`, then measures five `runtime.MemStats.TotalAlloc`
constructor/close deltas without payload reads. The frozen oracle is 65536
bytes maximum per constructor and 16384 bytes maximum median large-minus-small
growth.

`characterize-before.json` records the old source failing the oracle: small
fixture values were 74136 bytes and the large fixture median was 26624552 bytes
(maximum 26629840). `characterize-after.json` records the same consumer and
oracle after WAV reads delegate to the canonical streaming source; both fixture
maxima are below 65536 bytes and median growth is zero.

The public `NewWAVSource` counter records metadata-only open reads (44 bytes,
zero payload), exactly 14 payload bytes for `ReadSamples(7)`, and no more than
`FrameSize*2` payload bytes for `ReadFrame`. The caller-owned stream is closed
once.

## Behavioral controls

The positive consumer checks literal samples, one shared mixed-read cursor,
exact sample tails, zero-padded frame tails, empty input and repeated EOF,
pre-cancelled reads, malformed/truncated/unsupported error identity, in-place
payload mutation, post-open truncation, and caller-owned stdin ownership. The
negative control changes the expected `12345` sample to `12346`; it exits
nonzero with the causal mismatch.

The source now validates the RIFF layout and physical extent at open, retains
no decoded payload, and delegates both `ReadFrame` and `ReadSamples` to the
same `WAVSource` cursor. Post-open payload mutation is intentionally visible;
post-open physical truncation reports `*audio.TruncatedPCMError` on the first
affected read and terminal EOF thereafter. Snapshot isolation is not promised.

## Shipped workflow and regression

`--workflow` builds the same-source `agent-cli/cmd/yui` binary and launches the
public file-input route with a local integrity-sealed replay fixture derived
from the shipped credential-free vision-describe capture. The seven authored
samples are recorded as one exact 960-byte frame (short-frame zero padding),
the scripted response is emitted, the recording manifest is complete, and the
process exits cleanly with `fixture_complete`. `workflow.json` records the
literal input/output and manifest hashes.

`--regression` invokes the existing C21 verifier controls read-only with the
C31-built `yui` artifact. It asserts the exact audio-tool and interruption PCM
hashes, provider event bytes/order, tool marker/content, transcript/session-log
terminal results, manifest hashes, and healthy interruption tail. `regression.json`
records the reused control path and resulting artifacts. These are software
replay checks, not acoustic or physical-device proof.
