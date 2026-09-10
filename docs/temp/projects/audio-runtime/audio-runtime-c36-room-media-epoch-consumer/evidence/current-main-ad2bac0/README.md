# C36 current-head evidence

These machine-readable summaries were generated from source revision
`ad2bac047eeeff068e9b2d33e3f72eeace1847a1` after integrating
`origin/main=926ded7bfa8f3c3e42115192d03aa1240c4806db`. The predecessor
checkpoint `32d31d541844228174b44d0d77cb4c2252679723` remains its parent.

The exact bounded probe was:

```sh
rtk proxy python3 verify.py --action all \
  --source /Users/abdifamily/.codex/worktrees/af44/go-agent-harness/.claude/worktrees/audio-runtime-c36-room-media-epoch-consumer \
  --evidence /Users/abdifamily/.codex/worktrees/af44/go-agent-harness/.claude/worktrees/audio-runtime-c36-room-media-epoch-consumer/docs/temp/projects/audio-runtime/audio-runtime-c36-room-media-epoch-consumer/evidence/current-main-ad2bac0 \
  --child-timeout 60 --aggregate-timeout 600
```

It returned `accepted` in 13.032649 seconds. The room-media executable SHA-256
is `e2eadc6f7e0f559bdebe4dd3b7b1595dd038691d052c0a0f186af05508bc8d54`; the
same-source yui executable SHA-256 is
`85f084aa019bf09020a791b85df46a8475aa39d86393245315c92f2d70d8499e`; the
input manifest SHA-256 is
`30dae80d7289a7f7b49717a3813282a1bc56aa4ff7b859ce2e11f6dcd8359c0f`; and
the source archive SHA-256 is
`5bd2bb08b38dfc376de9394919ce2fa7e8ca5396e14f05a2654ea4edc2d1e382`.

Commit `4c7c640a4457f6f79fa9ec301162373d95bcee3e` is an evidence-only
descendant of the tested source revision. Its diff from `ad2bac047eeeff068e9b2d33e3f72eeace1847a1`
is confined to this evidence directory, so the recorded executable-input
manifest and both binary hashes remain valid for the submitted head.

The boundary evidence includes a 1 MiB first JSON object followed by a second
object; the subprocess exits nonzero with `request exceeds maximum size of
1048576 bytes`. The active cancellation report records real media-pump
evidence (`source_frames=4`, `output_frames=1`, `media_pump_active=true`) and
both public waits joined. Parity records the generated interruption bundle
with only `audio-trace/timeline.jsonl` removed; strict directory replay exits
nonzero with the expected missing-timeline diagnostic, not an integrity
checksum failure. Normal/race/vet, mutation, lifecycle, hang cleanup, and
same-source audio/tool plus interruption replay controls all passed.

Native hardware/acoustic playback and Realtime sessions remain out of scope;
the playback proof is software-only and the evidence does not claim project
completion.
