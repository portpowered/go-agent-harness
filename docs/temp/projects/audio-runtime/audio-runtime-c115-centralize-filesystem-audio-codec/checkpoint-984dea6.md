# C115 repair and evidence checkpoint

The admitted task is `audio-runtime-c115-centralize-filesystem-audio-codec` in
project `audio-runtime`, on branch
`codex/audio-runtime-c115-centralize-filesystem-audio-codec`. Admission was
verified with `project-control.py verify-work --type task`; the candidate
remains a descendant of `origin/main` at
`b7d25ca6f0e9b94c62b193059160dfbf446ef1d6`.

Independent review rejected PR #501 because the shipped CLI constructed an
uncomposed runtime tool service, session/room capability construction did not
receive the composed service, codec cleanup errors were dropped, decoder
output overflow waited without terminating the process, and C115 shipped
evidence was absent. The repair is checkpointed in `e4d1814`:

- `NewToolCommandWithRuntimeService` and
  `NewRoomRunCommandWithToolService` receive the composed Wire service;
  generated Wire was regenerated and the session capability transport has no
  uncomposed runtime fallback.
- Temporary-file removal errors are joined as typed input-file failures.
- Bounded stdout/stderr overflow cancels and terminates the decoder once,
  with deterministic cleanup and termination assertions.
- Focused compile/test validation passed 1,902 tests across six packages.

The shipped evidence workflow is checkpointed in `984dea6`. The exact run is
`runs/20260913T045935Z-19850/report.json` and the accepted verifier output is
`verification-summary.json`. All five requested cases passed in 4.840 seconds:
shipped valid-WAV/text tool use, truncated-WAV rejection, cleanup/overflow
causal tests, C21 runtime/consumer/replay regressions, and C50 public
help/tool/replay regressions. The built `artifacts/yui` is 51,013,874 bytes
with SHA-256
`10eaff438b75efdfb2d5d6c6d83ae690e41c5f78725520794bf4d736809664fe` and is
intentionally retained as an ephemeral build artifact; its manifest and all
bounded logs/receipts are committed.

The C21 replay produced the pinned 4,800-byte PCM receipt with SHA-256
`0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502`.
`GOWORK=off` external-consumer build/run passed. No live provider, credential,
physical device, or acoustic proof was used or claimed. Script CI, independent
review, guarded merge, and post-merge vertical validation remain unclaimed;
the next action is to push this changed head and submit it to the script CI
gate without polling it.
