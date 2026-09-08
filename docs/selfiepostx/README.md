# Selfie video to X: implementation and acceptance log

## Verified outcome (2026-09-08 UTC)

Two user-authorized AI-disclosed selfie videos were published through the harness
X WebMCP router as @portpowered and verified on fresh permalink tabs with native
video playback (readyState 4, 480x864, currentTime advancing, no media error):

| Cut | Published post | Local uploaded SHA-256 |
| --- | --- | --- |
| XFINAL01-a2/post-v3.mp4 | https://x.com/portpowered/status/2097157540169822591 | b517b07e7bd6535bfc2b918e35fe4ed5dba5bb0b10194c38d887343a79a53fed |
| XFINAL02-a2/post-v2.mp4 | https://x.com/portpowered/status/2097158624548368755 | 4e82b5fe3a30936c0e119f196954f7dfc59eeb54a97d483c131024a93b692e41 |

Local exports were 358441 bytes/3.25s and 445548 bytes/4.184s. X's player reports
3.328s and 4.256s after processing. Fresh permalink playback confirms persisted
posts, not just local composer previews. Exactly two publish calls were made for
the selected cuts; no rejected sample was posted.

### Likeness iteration and review

Twelve built-in ChatGPT image-generation variants (four headshots, six wardrobe
plates, two targeted repairs) and exact prompts are retained in tv-girl
`input/jessie/likeness-20260907`. Independent subagents chose headshot02 as the
best still translation. Nevertheless two initial videos from it narrowed/lengthened
the lower face, so they were held back. The successful pair used the original
photo directly and Comfy's `ref_image_size=max`, preserving more reference pixels.
Clothing, room and camera angle stayed close to the source; no canonical old
character assets were overwritten. Unknown body proportions remain provisional.

The tv-girl renderer gained optional validated `reference_image_size=match|max`;
legacy plans default to match. Three mocked tests verify modes, rejection and alias.
Four new five-second renders were reviewed; the selected exports trim opening/tail
material, remove embedded prompt metadata, and preserve each complete short question.
Final local ASR matches the scripts without extra words. Peaks are -8.5/-6.4 dBFS;
ffmpeg detected no freezes at -60dB for 0.5s. Main inspected sampled frames and the
second export's exact settled last frame. Independent visual reviewer accepted both
under the user's approximate-likeness judgment.

AGY streamed a review of each cut and each selected final export. Final conversations:
`59d530c4-8739-4113-9683-91c464e98392` and
`a9645ab8-7e32-4446-b9a2-050dfd4d2adb`. Both final reviews reported no obvious
sampled-frame likeness drift or extra transcribed words. **These tools supplied
sampled frames/transcription, not continuous audiovisual perception.** The original
strict cold-watch contract is not marked passed. The user explicitly delegated
judgment; this run used an honestly limited alternative release decision. Unsupported
AGY inferences (e.g. freeze inferred from a settled expression) were not accepted as
facts. Each cut has a review.md and retained evidence.

### Live upload recovery and limits

The first new transfer hit a page-generation change before any bytes were accepted.
Inspection found an empty unattached transfer; it was canceled by exact token.
A multiline caption then stopped at exact-text validation because X inserted an
extra newline. That owned text was cleared after account/text/no-media checks,
and a single-line caption used. Neither failure clicked Post. The final successful
receipts bind account, hash, byte count and one-use token, and publish uses identical
caption with confirm=true. Do not silently normalize a changed caption or automatically
retry an uncertain publish. Multiline caption fidelity remains a known limitation.

Independent code review fixed two additional races: failed focus acquisition cleanup
must not disable a newer active lease; text-only preparation/publishing must reject
pending files even before previews exist. Regression tests pass. The feature binary
used for publication has SHA-256
`f6224d6f23dafeb18f9eb96baa33113d929bc7b41916c5b8b342dc6f47655755`.

### Evidence locations and boundaries

In tv-girl, each selected attempt directory contains plan, six-section prompt,
original render, final export, transcript/probes, review, X prepare/publish receipts
and `x-permalink-playback.json`. Stream logs are `.tmp/agy-XFINAL*-*.jsonl`.
The chronological local account is `docs/selfiepostx/overnight-run-20260908.md`.
Private source images, media bytes and authentication data are not committed to the
harness repository. Captions explicitly say AI-generated video and voice, scripted
synthetic performance, not a recording of the reference person.

No daily automation was enabled. Generation stopped after these two verified posts.
The earlier sections below are historical snapshots, superseded by this outcome.

### Integration and CI follow-up

Feature commit 099a1145 was integrated with upstream 00c14758 and pushed as PR399.
The initial PR run passed unit, race, real-Chrome, macOS audio and Windows checks,
but identified new lint/size debt. The command was split into bounded preparation,
transfer and polling methods; tests were split by concern and consolidated into
existing files to respect package file-count limits. File-close/type-assertion
errors are handled; cleanup uses WithoutCancel to retain context lineage while
remaining independent of operation cancellation. No baseline or policy was relaxed.

Post-refactor local checks pass: the three affected packages' architecture/size
lane (249 files, 4086 functions), all focused X/focus/CLI tests, actual final MP4
decode, core WebMCP/adapter tests, build and vet. The scoped gate was run with
the checked-in manifest/baseline but without historical comparison. Historical
comparison separately encounters the pre-existing contradictory source-commit
check: upstream run 34179765022 already failed because baseline source ddebb8f4
does not equal merge base 00c14758; changing that source would also violate its
unchanged-source rule. This unrelated gate is not bypassed or modified here.
PR: https://github.com/portpowered/go-agent-harness/pull/399. Merge is conditional
on an acceptable CI result; do not infer that pushing the feature merged main.


## Latest update: live video preparation fixed

The resumed upload investigation identified Chrome's hidden-page media deferral.
The existing X tab reported `document.visibilityState === "hidden"` even with a
normal window and after `Page.bringToFront`. Both detached and attached MP4
players stalled at readyState 0. `Emulation.setFocusEmulationEnabled(true)` made
both load metadata immediately (480x864, 5.167s). Disabling it restored `hidden`.
No codec changes, credentials, browser-profile changes, or security-policy
bypasses were needed.

The harness now acquires a bounded focus lease on the exact selected target
around `x-prepare-video`, forwarding through the production session wrapper.
Cleanup uses an independent two-second context on success or failure; overlapping
leases in the same target session are reference-counted. The focus override is
not applied to unrelated WebMCP invocations or left intentionally enabled.

**Live X preparation succeeded** through the feature binary: 554426-byte
metadata-stripped `post.mp4`, SHA-256
`5c85ae1e17847c24af4258f182d18a6f5447d5af321724257bf923df903200fe`.
X exposed a video preview and enabled Post button; the adapter returned a one-use
draft token with `published:false`. An independent technical playback check
confirmed readyState 4, 480x864, duration 5.167s, and currentTime advancing to
0.860371 seconds after starting playback. Focus returned to `hidden` afterward.
This proves live attachment/preparation and local preview playback, **not a
published X post or final X-side transcoding**. Preview source was a blob URL.

Test caption: “Video upload validation only. AI-generated test clip; not for
publication.” No call to `x_publish_post` was made. The user rejected the
sample's likeness, so this clip must not be published. Merge/push remains gated
on the corrected likeness, valid audiovisual review, and an authorized publish
acceptance test. The historical upload timeout below has been resolved for this
sample; the review and publication gates remain open.

One early focus run encountered a generation change after beginning staging.
Read-only inspection found a zero-byte, unattached transfer; it was canceled by
its exact token before retrying. The CLI now preserves page-adapter refusal
codes/messages in its classified error, instead of hiding `upload_in_progress`
behind a generic invocation failure.

Regression checks passed: stock-Chrome X guard journey, actual MP4 decode journey,
focus release after cancellation, overlapping leases, production-wrapper target
binding, MP4 snapshot/hash and adapter-response tests. Core WebMCP and adapter
packages were rerun. Prior broad Windows baseline failures are unchanged scope.
Local evidence: tv-girl `.tmp/selfiepostx-media-probe.json`,
`.tmp/selfiepostx-media-foreground.json`, `.tmp/selfiepostx-media-focus.json`,
`.tmp/selfiepostx-prepare-focus.json`, `.tmp/selfiepostx-transfer-focus.json`,
and `.tmp/selfiepostx-live-playback.json`.

The remaining sections preserve the original investigation chronology.

## Status: not approved for merge or automated publishing

Run date: 2026-09-07, America/Los_Angeles (2026-09-08 UTC).
Repository: `portpowered/go-agent-harness`; baseline `ddebb8f4`.
Local branch: `feat/x-webmcp-video`, isolated worktree at
`C:/Users/andre/work/experiments/tv-girl/.tmp/harness-selfiepostx`.

Implemented an experimental native-video preparation path. Build, focused unit
tests, and the credential-free real-Chrome fixture pass. **Live acceptance did
not pass. No video post was submitted; no merge or push was performed.**

Two independent gates remain unresolved:

1. X stays at “Preparing media...” for the generated sample. The five-minute
   preparation deadline returns `invocation_timed_out`, with no publish token.
   A separate browser metadata-decode probe also timed out at readyState 0 for
   both the original MP4 and a losslessly remuxed faststart copy. This suggests
   a browser/media-path issue, but does not establish its cause. No X server
   acceptance or permalink was observed.
2. AGY returned `pass`, but its explicit audit admitted that it used sampled
   keyframes, transcription, and numerical frame/audio analysis rather than
   perceiving full continuous motion and audio. That is **insufficient for the
   existing cold-watch contract**. The report is retained as evidence, not an
   accepted audiovisual review.

## Requested workflow and boundaries

The user authorized implementing video support, generating a basic sample,
AGY validation, one live test post, and merging/pushing only if successful.
This run did not enable a daily schedule or authorize unrelated account actions.
The signed-in account was verified as `@portpowered` using `x_get_context`.
Authentication remains in the selected Chrome profile; no X credentials or
private HTTP upload endpoints are used by the implementation.

## Discovery and setup

- Harness library: `C:/Users/andre/work/harness/go-agent-harness`.
- Working installed CLI: `C:/Users/andre/go/bin/yui.exe`; an older `agent.exe`
  did not expose WebMCP. The feature binary was built with
  `go build -o <path>/yui-selfiepostx.exe ./agent-cli/cmd/yui`.
- Used the explicit CDP endpoint `http://127.0.0.1:9222`. Automatic discovery
  did not find the running browser. Selected the existing X home tab and
  persisted selection in tv-girl `.tmp/daily-webmcp`.
- Chrome version: `152.0.7977.76`. The original stock-Chrome test helper
  incorrectly skipped Windows because it used POSIX executable bits and
  `chrome.exe --version`. The opt-in helper now reads Windows file-version
  metadata; production browser discovery was not expanded in this change.
- Comfy Desktop moved after an upgrade. The application runs from
  `AppData/Local/Programs/ComfyUI/Comfy Desktop/Comfy Desktop.exe`, but starting
  its UI did not start the rendering backend. Used its recorded installation:
  `AppData/Local/Comfy-Desktop/ComfyUI-Installs/ComfyUI/ComfyUI/main.py`, with
  the existing `Documents/ComfyUI/.venv/Scripts/python.exe`, model-path YAML,
  input/output/user directories, and custom nodes. A temporary extra-path YAML
  points to the existing `Documents/ComfyUI/custom_nodes`.
- Set `PYTHONUTF8=1`: the initial backend startup failed when a custom node's
  checkmark log message could not be encoded in the Windows console encoding.
  No model, package, or Comfy installation upgrades were performed.
- The `you` factory service at localhost:7447 was not running. This run invoked
  the existing factory render/QC executors and AGY rubric directly, **not a
  completed streamed factory run**.

## Sample generation and reference-aware checks

All sample artifacts live in tv-girl:
`production/selfie-jessie/attempts/XTEST20260907-a1/`.

- `plan.json`: five seconds, seed 202609071, Jessie black-minimal outfit.
- `prompt.md`: follows the required six-section MiniMax reference structure.
- Image: `input/jessie/body-shots/jessie-bodyshot.png`.
- Voice: `input/jessie/voice/jessie-voice-ref-a.wav`.
- Spoken line: “One small idea, every day. Let's see what we notice.”
- Workflow: `workflows/minimax-r2v-audio-image`, portrait, 0.4 megapixels,
  27 steps, fixed seed. This is a basic pipeline sample, not evidence of
  audience demand or a tested daily editorial format.

```powershell
python factory-selfie/scripts/render_from_plan.py --character jessie --concept XTEST20260907 --attempt 1
python factory-selfie/scripts/qc_attempt.py --character jessie --concept XTEST20260907 --attempt 1
```

Render completed in 5.9 minutes. `clip.mp4`: 560444 bytes, 5.167 seconds,
480x864, 24 fps, H.264 High level 3.0, yuv420p, AAC LC stereo 32 kHz.
SHA-256: `094cfeced7712d41a332b1e9828665265216fb47b1cfb8c0c937417ff9f908dc`.
QC: audio present, peak -6.8 dBFS, ASR word match 1.0, no hard-fail reasons.
Reference-aware still review compared start/middle/end with the supplied image:
consistent identity, hair, wardrobe, setting and framing. This does not certify
motion or sound. See `qc.json`, `render-log.md`, and `review.md`.

For browser diagnostics, `post.mp4` was created with:

```powershell
ffmpeg -i clip.mp4 -map_metadata -1 -c copy -movflags +faststart post.mp4
```

This removes embedded Comfy prompt metadata without re-encoding. Future public
exports should strip private generation metadata. The final exported artifact
still needs its own accepted review/hash binding; it was not published.

## AGY validation and audit

Invoked `agy --model gemini-3.6-flash-high --print <cold-watch rubric>
--print-timeout 12m --output-format stream-json` with the existing watch-clip
instructions. AGY initially searched outside the project because its command
execution directory differed from the CLI-reported cwd. Supplied the exact
absolute clip/report paths in a follow-up, without providing the plan or QC.

Conversation: `a2620771-fd35-47db-8411-ba2621791b71`.
AGY wrote `cold-watch.md` and answered `pass`. A follow-up evidence audit asked
whether it actually perceived full motion and audio. It explicitly said no:
`view_file` yielded sampled keyframes and transcription; subsequent tools
extracted frames/audio and performed numerical analysis. Therefore the cold-watch
gate remains unmet. Do not treat `Inspected: yes` or a one-word verdict alone as
proof; require the capability and inspection evidence as well.

Local diagnostic logs (not checked in because they may contain personal paths):
tv-girl `.tmp/selfiepostx-agy-watch.jsonl`,
`.tmp/selfiepostx-agy-clarification.jsonl`, and `.tmp/selfiepostx-agy-audit.txt`.

## WebMCP implementation

See [X adapter usage](../webmcp/sites/x.md). The new CLI command
`webmcp x-prepare-video` snapshots a regular MP4 up to 64 MiB, hashes it, and
transfers ordered 32-KiB chunks through normal selected-broker invocations.
It never publishes. The page validates account, order, size and hash, reconstructs
a File, and dispatches a normal file-input change in the scoped X composer.
Transfer state expires after ten minutes. The adapter does not read local paths.

Preparation returns a token only after the preview and Post control are ready.
Publish checks the exact composer, caption, account, and observed media; file
changes/removal invalidate the media guard. Tokens are consumed before clicking.
Uncertain publication must never be retried automatically. Existing unrelated
text/media is preserved. Video-draft clearing is explicitly manual.

Live inspection found that the caption character counter is also a progressbar;
it must be excluded from processing detection. The fixture includes this case.
The live attempt still had a genuine “Preparing media...” indicator, so correcting
the counter selector alone does not establish a successful live upload.

## Tests and evidence

Passed:

```powershell
go build -o <output>/yui-selfiepostx.exe ./agent-cli/cmd/yui
go test ./agent-cli/internal/transport/cli -run 'Test(ReadXVideo|DecodeXVideoReply|WebMCPDirectCommandTreeIsFrozen|WebMCPDirectFlagsUseOneUnprefixedSpelling)$' -count=1
$env:WEBMCP_X_ADAPTER_INTEGRATION='1'
go test ./agent-cli/internal/webmcp/chrome -run '^TestXAdapterStockChromeJourney$' -count=1 -v
```

The fixture covers text publishing, missing confirmation, mismatched text,
duplicate tokens, video transfer, incomplete upload, wrong account, reordered
chunks, wrong hash, changed media/account, cancellation, and conservative video
clear behavior. It drives stock Chrome and actual File objects, but its synthetic
video payload and local composer **do not prove real media decoding or X upload**.

Ran the full CLI/siteadapter/chrome package suites on both the feature and
unmodified main. Both fail on existing Windows issues (POSIX permissions,
symlink privilege, escaped path assertions, subprocess/audio fixtures, config
tests). The feature failure-name set is a subset of baseline, which also showed
one intermittent managed-browser-close failure. WebMCP package recursion likewise
passes core/discovery/siteadapter/testkit/tools and fails those existing Chrome
platform cases. Logs: `.tmp/selfiepostx-tests.log`,
`.tmp/selfiepostx-baseline-tests.log`, `.tmp/selfiepostx-webmcp-tests.log` in tv-girl.
This is not a claim that the repository's full multi-module CI is green.

## Live attempt and remaining acceptance

Caption prepared (never posted):
“First end-to-end WebMCP video test: Jessie, generated with ComfyUI.
AI-generated video and voice. One small idea, every day.”

The 560444-byte sample transferred and the exact caption appeared in the X
composer. X remained at “Preparing media...”; no publish token was issued.
The CLI returned a non-retryable `invocation_timed_out`. There is no post URL.

Before merge/push:

1. Resolve actual browser decoding/processing, rerun live preparation with the
   final metadata-stripped artifact, and verify a playable preview.
2. Obtain a genuine full audiovisual review under the AGY contract, or an explicit
   user decision to use a different review gate. Do not silently substitute ASR.
3. Publish once through `x_publish_post`, with exact token/text and confirm=true.
4. Verify the resulting account permalink contains the caption and playable video.
5. Rerun relevant tests and CI, review the diff, then merge/push as authorized.

## Daily-content follow-up

The earlier content research is in tv-girl
`reference-docs/projects/daily-creator-webmcp-content-plan.md`. Start with useful
observations and short explainers, not weather. Treat proposed hooks and subject
choices as hypotheses until actual retention, completion, saves, and replies are
measured. Keep daily research, factual checks, reference assets, render hashes,
per-cut review evidence, explicit publishing decisions, and post receipts in a
single run manifest. No daily automation was enabled by this implementation run.

## External references

- [X: sharing and watching videos](https://help.x.com/en/using-x/x-videos), checked
  2026-09-07: web-upload workflow and account-dependent limits. Our 64-MiB cap is
  deliberately smaller and does not promise every MP4 will be accepted.
- [X API media best practices](https://docs.x.com/x-api/media/quickstart/best-practices):
  codec/export reference only; this implementation uses the browser UI, not API credentials.
