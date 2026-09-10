# C36 current-head evidence

These machine-readable summaries were generated from exact submitted source
revision `907e732b97e7abf1d2abe0bb90cd04dd547ce17c`, which includes current
`origin/main=926ded7bfa8f3c3e42115192d03aa1240c4806db`. The required baseline
`3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`, startup
`8bdafc7f947a3a2c9856220abdc539437035bd21`, and observed main
`d5012004c15c4df613fd5fc7d8c220f2c7252822` remain ancestors. Earlier
checkpoints are preserved in Git history.

The exact bounded probe was:

```sh
rtk proxy python3 verify.py --action all \
  --source /Users/abdifamily/.codex/worktrees/af44/go-agent-harness/.claude/worktrees/audio-runtime-c36-room-media-epoch-consumer \
  --evidence /Users/abdifamily/.codex/worktrees/af44/go-agent-harness/.claude/worktrees/audio-runtime-c36-room-media-epoch-consumer/docs/temp/projects/audio-runtime/audio-runtime-c36-room-media-epoch-consumer/evidence/current-main-ad2bac0 \
  --child-timeout 60 --aggregate-timeout 600
```

It returned `accepted` in 8.997228 seconds. The room-media executable SHA-256
is `6c49d7fba55deff469c501427f2ac12b6d92f035f9353b864747be3ffde942de`; the
same-source yui executable SHA-256 is
`85f084aa019bf09020a791b85df46a8475aa39d86393245315c92f2d70d8499e`; the
input manifest SHA-256 is
`596a1fbc2d1141e15a52be6ea8d17f8184a180f1437a11c3cf9eaabf0f05c9a1`; and
the source archive SHA-256 is
`b5ff9103be9e22878d7a9a268f232fcfe54d1cb0d5ca0409cfb638e6ca3159bf`.

The repair adds an observed public-fan-out barrier before advancing the
deterministic clock and requires a nonempty output frame before active
cancellation is declared ready. This removes the race-only empty/cadence
frame explosion in the mutation oracle while preserving the public
`rooms.Service.Run` lifecycle.

The boundary evidence includes a 1 MiB first JSON object followed by a second
object; the subprocess exits nonzero with `request exceeds maximum size of
1048576 bytes`. Active cancellation records `opened/started/waited/closed =
2/2/2/2`, `source_frames=4`, `output_frames=1`, `media_pump_active=true`,
`close_calls=6`, and joined public waits. All five mutation subprocesses and
six provenance-binding subprocesses reject as required; hang cleanup proves
the descendant shares the process group and is gone. Parity records the
generated interruption bundle with only `audio-trace/timeline.jsonl` removed;
strict directory replay rejects with the expected missing-timeline diagnostic,
not an integrity checksum failure. Audio-tool parity is 4800 bytes with hash
`0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502`, 18 wire
events and 1 tool call; interruption is 3840 bytes with hash
`6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22`, 15 wire
events and 0 tool calls, with the frozen healthy-tail oracle preserved.

Normal/race/vet, focused room lifecycle and mixer/audio normal/race, repeated
C36 race regressions, architecture-size (`181` packages, `1867` files,
`27583` functions), Wire, and diff checks pass. The latest script-CI rejection
is external C20 integration `test46/slow_device` (run `34488777819`, job
`102909761115`): remote playback timed out before its final marker while the
child was still running; no C36-owned path is implicated.

Native hardware/acoustic playback and Realtime sessions remain out of scope;
the playback proof is software-only and the evidence does not claim project
completion.
