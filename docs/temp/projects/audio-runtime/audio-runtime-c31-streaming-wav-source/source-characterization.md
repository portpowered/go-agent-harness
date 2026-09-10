# C31 source and evidence ledger

Task: `audio-runtime-c31-streaming-wav-source`
Branch: `codex/audio-runtime-c31-streaming-wav-source`
Isolated worktree: `/Users/abdifamily/.codex/worktrees/af44/go-agent-harness/.claude/worktrees/audio-runtime-c31-streaming-wav-source`

Admission used the sole `audio-runtime` project manifest and the exact
`~default` board task. The original admission had no C31 review or CI rejection
feedback; the current board feedback and its repairs are accounted for below.
The predecessor and baseline checkpoints were preserved:

| checkpoint | revision |
| --- | --- |
| startup integration revision | `8bdafc7f947a3a2c9856220abdc539437035bd21` |
| required baseline | `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad` |
| freshly fetched `origin/main` | `431fc96c14f0e0045629d9c36f98ee61ff06e840` |
| pre-change isolated HEAD | `1f82284abee0bd31a6680310444cea2e4c16ef00` |
| current-main integration merge | `a85ebbd374d8264f33a79d551b9cea8aff9b36ab` |
| initial candidate implementation revision | `6f82c046dd19a52ce57a8be895f0df43921db8da` |
| cleanup-error repair revision | `9efd435177b58db8a9508b92b8b96202021b6e89` |
| final candidate revision | `e0ee33f0c161f8031fd074397a46ad0316c4b4fb` |
| current review-repair implementation | `ee971c0f8a8a62ac17d85181f013a88dfc16502` |
| review-150 repair checkpoint | `f88eda244ae2c8d8bc7cebc85aa5c2f99a9df57c` |

`git merge-base --is-ancestor` passed for the startup integration revision,
the required baseline, and fetched `origin/main`. The current-main integration
was a no-ff merge at `a85ebbd374d8264f33a79d551b9cea8aff9b36ab`; no reset was
used and the running host checkout was not touched.

## CI rejection accounting

The first submitted candidate was PR `#423` at head
`b30ed0d55df320bf11f901304264c1fb90255727`. Full run
`34431822827`/static job `102728747026` reported five pinned golangci-lint
`errcheck` findings: the unsupported-rate `wav.Close` path and four streaming
test cleanup paths. The same run's hermetic job `102728747047` also reported
`TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio/test46/provider_burst`
missing its final PCM marker at the remote device boundary. That integration
test path is unchanged by C31 and does not call `NewFileSource`; the exact
`CGO_ENABLED=0 -tags=nomicrophone` case passed locally in 15.750s, so no
C20/C21-owned fixture or runtime path was changed here. The five C31 findings
were repaired in `9efd435` with checked cleanup/error joins; the resulting
403-line source-file budget regression was repaired in `e0ee33f` by moving the
WAV-specific rate-error constructor into the owned streaming WAV source.

The current candidate evidence below is generated from clean merged source
revision `a85ebbd374d8264f33a79d551b9cea8aff9b36ab`, whose first parent is the
review-150 repair checkpoint `f88eda244ae2c8d8bc7cebc85aa5c2f99a9df57c` and
whose second parent is the freshly fetched `origin/main`. Broad CI has not been
rerun or claimed green, and the script gate retains ownership of the current-head
check.

## Review-140 repair accounting

The concluded review-140 feedback identified four C31 defects. `NewFileSource`
now uses an internal metadata-only WAV constructor so valid 44.1 kHz input is
reported as the historical path-aware `FormatError` wrapping
`wavio.UnsupportedError`, while public `NewWAVSource` still rejects that rate
directly. `WAVSource.ReadSamples` now uses the byte count returned by
`io.ReadFull`; the owned regression and consumer assert `Bytes == 1` after a
one-byte post-open truncation. The workflow runner validates the emitted WAV
header/data length and compares actual PCM bytes and SHA256 with the scripted
response. All reports now include the clean tested revision and scoped
consumer/yui build-input hashes, distinguishing them from a later docs-only
evidence descendant.

## Review-150 repair accounting

The concluded review-150 feedback identified three remaining evidence defects.
The public consumer's IO report now serializes exact header/data read ranges,
metadata and per-operation seek counts for both counted opens, plus individual
close counts, and its pass oracle asserts those values instead of only aggregate
bytes. The bounded runner never calls `communicate()` after timeout termination;
it kills the process group, performs a bounded parent reap, closes inherited
pipe readers, and records timeout/cleanup state. `--timeout-control` provides a
deterministic detached-pipe-holder regression: a 2-second holder completed the
bounded cleanup in 105 ms against the 850 ms cap, with SIGKILL, reaped parent and
closed pipes recorded in `timeout-control.json`. The trailing whitespace in this
ledger was removed, so `git diff --check` is clean.

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
oracle after WAV reads delegate to the canonical streaming source; the final
source run measured small- and large-fixture allocations of
`[488,488,488,488,488]` bytes. Both maxima and medians are 488 bytes, and
median growth is zero.

The public `NewWAVSource` counter records metadata-only open reads (44 bytes,
zero payload), exactly 14 payload bytes for `ReadSamples(7)`, and no more than
`FrameSize*2` payload bytes for `ReadFrame`. The caller-owned stream is closed
once. The JSON IO report now preserves the exact metadata/read ranges and seek
counts for both counted opens, plus the per-source close counts; the consumer
asserts the canonical ranges rather than only their aggregate byte totals.

## Latest CI rejection reconciliation

The exact current-head rejection is preserved in
`ci-rejection-34433566917.json`. The canonical board returned `work-task-123`
for PR `#423` at source head
`6717dd5add3d613b0adf24516d679dd63293495` with required `CI (hermetic)`
failure. The full hermetic job log (`102733925196`) reported
`TestFamilyAIterativeBuildUpThroughShippedProcess` with three streamed output
markers/12 bytes instead of four/16. The same run's integration job
(`102733924992`) separately reported the C20-owned remote
`test46/slow_device` final-marker deadline at
`agent-cli/test/integration/session_tool_audio_remote_e2e_test.go:183`.

The Family A check is outside this task's owned paths and exercises raw stdin
`FileSource.ReadFrame`; its raw implementation is byte-for-byte behaviorally
unchanged by C31 (the helper methods were moved from the deleted duplicate
`source_samples.go`). The exact hermetic-tagged test passes 20/20 on this head,
and the C20 remote fixture/runtime path is unchanged and remains separately
owned. No C31 source repair is justified by either failure; both are retained
as non-waived external CI evidence. The focused C31 normal/race, consumer,
workflow, and read-only regression controls remain green after this
reconciliation.

The subsequent concluded review-146 finding returned PR `#423` at head
`494e1323bcc7a8208b8bcc99eaaa69a3d36a585f` for stale ancestry against
`origin/main` `b0acab1238d1aa6bf6bce5ca074451310c7eb039`. This checkpoint
integrates that exact fetched main in merge `ce9a795192bc5433811de4f37824cc579fa0daa8`
and regenerates the executable reports from the merged source; no old green
checks or artifacts are reused.

The latest concluded review-157 finding identified the next stale-base boundary:
PR `#423` was still based on `b0acab1238d1aa6bf6bce5ca074451310c7eb039` while
the freshly fetched `origin/main` was `431fc96c14f0e0045629d9c36f98ee61ff06e840`.
The current checkpoint merges that main as `a85ebbd374d8264f33a79d551b9cea8aff9b36ab`
and regenerates the consumer, yui, workflow, timeout, regression, and source
characterization reports from that exact revision. The old Family-A and C20
remote failures remain preserved as external, non-waived findings.

## Behavioral controls

The positive consumer checks literal samples, one shared mixed-read cursor,
exact sample tails, zero-padded frame tails, empty input and repeated EOF,
pre-cancelled reads, malformed/truncated/unsupported error identity (including
the 44.1 kHz `FormatError`), in-place payload mutation, post-open truncation
with the actual one-byte count, and caller-owned stdin ownership. The negative
control changes the expected `12345` sample to `12346`; it exits nonzero with
the causal mismatch.

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
literal input/output and manifest hashes, and its runner compares the observed
output PCM payload byte-for-byte against the expected scripted response.

`--regression` invokes the existing C21 verifier controls read-only with the
C31-built `yui` artifact. It asserts the exact audio-tool and interruption PCM
hashes, provider event bytes/order, tool marker/content, transcript/session-log
terminal results, manifest hashes, and healthy interruption tail. `regression.json`
records the reused control path and resulting artifacts. These are software
replay checks, not acoustic or physical-device proof.

## Exact-source provenance

`artifact-manifest.json` records tested source revision
`a85ebbd374d8264f33a79d551b9cea8aff9b36ab`, consumer build-input SHA256
`a28b1c178a2021d2fcb1ca7b8586f4972ebdf4617c2f2641c1ad0b749106ec1b`, and yui
build-input SHA256
`9d1612bd877f70699d35641c134ceefa4083727df048c221debb3f1bb551835c`. The
current consumer and yui artifact hashes are respectively
`a8fd583252450bfbebef79e3ffd7745a4a4032a9148e79ab74c0c3eb06b4aa85` and
`dfa157096bd1110e71eda70d1c19055899c8f7da452a4c375fc5d254a84e5252`.
Any later evidence-only descendant must preserve these input hashes and change
only the owned evidence directory; no executable is relabeled as current
without a source/build-input match.
