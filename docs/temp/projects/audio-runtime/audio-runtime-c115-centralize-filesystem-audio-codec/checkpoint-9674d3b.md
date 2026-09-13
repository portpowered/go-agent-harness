# C115 integrated-baseline checkpoint

The admitted task is `audio-runtime-c115-centralize-filesystem-audio-codec` in
project `audio-runtime`, on branch
`codex/audio-runtime-c115-centralize-filesystem-audio-codec`. Admission was
verified with `project-control.py verify-work --type task`. The isolated
worktree now contains merge commit `9674d3b3407a16990ce3d0e1305a0a5db27b671d`,
which integrates the fetched C107 release at `origin/main`
`ea53be13ce5e4ef14fd8c89c695c21744a1f7686` while preserving the C115 repair
commits and predecessor checkpoints.

The focused causal suite passed again after integration: 1,902 tests across
six packages. Wire generation completed cleanly. The bounded C115 workflow
then rebuilt the shipped artifact and accepted all five requested cases in
`runs/20260913T050702Z-35571/report.json`:

- shipped valid-WAV/text tool use;
- malformed/truncated-WAV rejection;
- cleanup-error identity and stdout/stderr overflow termination;
- C21 GOWORK=off consumer/runtime/replay regressions; and
- C50 public help/tool/replay regressions.

The artifact is 51,013,874 bytes with SHA-256
`10eaff438b75efdfb2d5d6c6d83ae690e41c5f78725520794bf4d736809664fe`.
The replay produced the pinned 4,800-byte PCM receipt with SHA-256
`0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502`.
`verify.py --mode public-and-accumulated-regressions` passed with the current
candidate and current `origin/main`. The workflow is credential-free and
does not claim live-provider, physical-device, acoustic, CI, review, merge,
or post-merge vertical evidence. The next action is to commit and push this
changed head, update PR #501, and submit it to script CI without polling.
