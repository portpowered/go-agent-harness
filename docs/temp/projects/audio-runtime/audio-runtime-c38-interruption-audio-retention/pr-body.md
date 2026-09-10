# C38 interruption audio retention — executor handoff

Project: `audio-runtime` (`audio-runtime-v1`)

This is the existing admitted task and existing PR #430. The isolated branch is
`codex/audio-runtime-c38-interruption-audio-retention`; `prd.json.branchName`
matches it. The candidate preserves startup, baseline, C30 planning, and current
`origin/main=fdf3b2d98914f50577865e825c733e73520b9ef3` ancestry. No second
project, acceptance waiver, host-checkout reset, or predecessor mutation was
used.

## Review and CI repair map

- Reviews 219, 229, and 249: provider-terminal barrier, bounded quiet drain,
  delayed-delta/canceled-connect controls, and deterministic interrupted-prefix
  retention in the leased session audio-output path.
- Review 245: queued-after-cancellation messages use bounded non-cancellable
  retention before `WriteSamples`, with a deterministic queued-delta regression.
- Review 258: malformed-delta and sink-write paths join and report provider
  close errors, with sentinel tests.
- Latest CI run 34539207051 / job 103077775076 (`CI (static)`) found the sole
  errcheck at `session_tool_audio_remote_oracle_test.go:32` for discarded
  `sink.Close()`. The deferred cleanup now reports close failure through the
  test. The repair is in checkpoint `83e31db1`.

## Evidence

- C38 session-audio normal and race focused suites: 20 tests each; vet,
  architecture-size (`184` packages / `1,888` files / `27,806` functions),
  size-check, Wire, pinned golangci-lint v2.9.0, staticcheck 2026.1, and
  diff-check pass.
- The newly authorized remote-marker causal control passes normally. Its race
  build was blocked before test execution because macOS `strip` could not write
  the helper with only about 493 MiB free; the required 2 GiB reserve is not
  available. This is not reported as a product pass or failure.
- Existing C38 exact artifact evidence remains historical and is not relabeled:
  the merged main and new integration test changed executable/build inputs.
  A fresh yui build, repaired replay, package provenance, and remote-control
  race run must be produced from the final clean head after storage recovery.

No script-CI success, independent review, guarded merge, post-delivery vertical
acceptance, physical/acoustic proof, or project completion is claimed here.
The next action is to restore stable free space above 2 GiB, run the remaining
bounded C38 causal/original/repaired/negative/cleanup/focused/package controls
from the clean head, then submit this same PR to the script-owned CI gate
without polling. Retain the task for any exact rejection or actionable repair.

## Fresh exact-head handoff — 2026-09-10T23:18Z

The storage prerequisite was available for this run: approximately 5.9 GiB was
free before the build and approximately 3.5 GiB remained afterward, above the
required 2 GiB reserve. The clean tested source is
`5d2e029a51e934fb8dcc78702ad8acb14c5e4402`.

- `repaired-20260910T231538Z-77788` returns `REPAIRED_ORACLE_PASS`. The newly
  built yui is 50,912,034 bytes,
  `8e5eb0d77a65e5467d4ccfb84146205aa9c91fa8c62d706c4df2683cb4618c42`.
  Tool PCM is 4,800/3,200 bytes with the frozen hashes; interruption PCM is
  3,840/3,360 bytes with the frozen hashes and 2,400-byte healthy tail; strict
  replays pass.
- `causal-20260910T231514Z-76987` passes the deterministic retention barrier.
  `focused-checks-20260910T231349Z-72475` passes normal/race, vet,
  architecture/size (`184` packages / `1,888` files / `27,806` functions),
  and Wire. The remote-marker control passes normal and race.
- `negative-controls-20260910T231525Z-77395` passes the 21-case C30 consumer
  plus exit-1 negative control, real same-length PCM/hash mutation rejection,
  and missing-timeline rejection. `cleanup-control-20260910T231528Z-77578`
  passes capped output, TERM/KILL, reaping, and no survivors.
- `original-20260910T231759Z-79359` returns
  `HISTORICAL_FAILURE_PRESERVED` against the immutable C30 artifacts and
  records the live legacy pass as scheduling variation only. `package` returns
  `PACKAGE_READY` for the exact source/artifact/build inputs; build-input SHA is
  `7689716a955acc5299b34c23aa3d600fa6c7829eed577c93232b2b0984716309` over
  `2,185` inputs and the architecture helper SHA is
  `debe7ca096d60db699974b7d9a37ba15d146ec3f26a860f45544f96a9e6d547f`.

The candidate is ready for the script-owned current-head CI gate. This does not
claim CI success, independent review, guarded merge, vertical acceptance, or
project completion. Submit the changed same-task head once without polling;
retain C38 ownership through `CONTINUE` for any exact rejection or actionable
repair.

## Pushed-head provenance confirmation — 2026-09-10T23:23Z

The exact pushed head is `8bce982045d81696348188b1e28194d005c25a8d`.
`repaired-20260910T232125Z-80573` returns `REPAIRED_ORACLE_PASS` and
`package` returns `PACKAGE_READY` for that exact source identity. The new yui
artifact remains 50,912,034 bytes with SHA-256
`8e5eb0d77a65e5467d4ccfb84146205aa9c91fa8c62d706c4df2683cb4618c42`; the
frozen tool/interruption PCM and strict replay results remain exact. The
build-input manifest is unchanged at SHA-256
`7689716a955acc5299b34c23aa3d600fa6c7829eed577c93232b2b0984716309` over
`2,185` inputs. The remote-marker control passes at this head in normal and
race modes.

This confirms executor readiness for the script-owned current-head CI gate; it
does not claim CI success, independent review, guarded merge, vertical
acceptance, or project completion.
