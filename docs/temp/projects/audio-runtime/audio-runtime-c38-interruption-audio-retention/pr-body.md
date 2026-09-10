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
