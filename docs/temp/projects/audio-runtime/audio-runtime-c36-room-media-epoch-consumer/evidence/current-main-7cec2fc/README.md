# C36 current-head evidence

These machine-readable summaries were generated from clean committed source
revision `7cec2fce25e0003952ca080ffcd299269940ee16` after integrating
`origin/main=d6efc88d`. The preceding `dc04dc4a` repair commit is the causal
clock implementation checkpoint; this source revision adds the direct
synchronous-deadline regression assertion.

The fresh build probe was run from the isolated worktree with:

```sh
python3 verify.py --action all --source <isolated-worktree> \
  --evidence /private/tmp/audio-runtime-c36-current-7cec2fc \
  --child-timeout 60 --aggregate-timeout 600
```

It returned `accepted` in `13.110226` seconds. The complete source archive is
bound by SHA-256
`f40301638ff5ea3e7d015f5109edbbf008234062a6e83476fd01a289dae8c6f4`, the
input manifest by
`56aa8708644b6fe1628a4045067e7c934fefe83f4f501411640cd181bc88350e`, the
room binary by
`f0f42fe666f2d91e43f120a1901c9b8c34e3cfe7f6cd90aa54c0a0d7a41ef3fd`, and
the yui binary by
`79dca64f4413b3e926071b18769660f57d7d04a06359955f02bd25cf08abec33`.

The verifier accepted boundary, provenance, peer routing/epoch, all five
subprocess mutation oracles, partial-recording rejection, software playback
consumption, lifecycle cancellation/join, TERM/KILL descendant cleanup, and
same-source C21 audio-tool/interruption parity. The raw positive report retains
`alice=[211,212,213,214]`, `bob=[111,112,113,114]`, stale pending samples `4`,
and end-of-response before terminal. Physical playback remains explicitly
unavailable; the consumption result is software-only.

`no-build-parity-verdict.json` records an independent `--no-build` parity run
in a fresh scratch directory using the exact binary hashes above. It returned
`accepted` in `0.768263` seconds. `parity.json` preserves the missing
`audio-trace/timeline.jsonl` negative control and its truthful rejection.

The source archive, generated binaries, and process logs remain in the
generating host's private scratch paths; these tracked summaries retain their
hash bindings without relabeling those paths as portable artifacts.
