# C109 surviving process-group cleanup repair — 2026-09-13

This checkpoint changes only the admitted C109 evidence directory. The
preserved C61/C83 refs and worktrees, production and test source, C79 shared
registries, running host checkout, and unrelated owner paths remain unchanged.

## Cause and repair

Review reproduced an exiting leader with a silent descendant that closed its
inherited stdout. `bounded()` observed pipe EOF and returned without cleanup
because its final cleanup branch only ran when `process.poll() is None`. That
left the descendant's process group alive with `term_sent=false` and
`kill_sent=false`.

Commit `c3680d7f362e6301bca5142534f0bed701c714cf` repairs the owned runner to
clean up whenever `process_group_gone(process.pid)` is false, regardless of
the leader's exit status. It adds a bounded group-disappearance wait after
TERM/KILL and records `group_gone_after_cleanup`. The new regression uses a
same-group Python descendant that closes stdout/stderr and sleeps; it requires
the bounded result to time out, send TERM or KILL, and report a gone process
group.

## Before and after control

The in-memory pre-repair comparison loaded the runner from `8d8cfa68` and
returned:

```text
status=failed timed_out=false process_group_gone=false term_sent=false kill_sent=false
```

The intentionally retained control group was then killed by the comparison
harness and verified gone. The repaired runner at `c3680d7` returned:

```text
status=timeout timed_out=true process_group_gone=true
term_sent=true kill_sent=true group_gone_after_cleanup=true
```

No descendant remained after either control's explicit cleanup.

## Revalidated evidence

- Current main was fetched as `071b0abfd67501db61e3c1929971c6dd6e77eb62` and
  integrated in merge commit `a8409ebc566a0f5ec6c2c287e6665cf14c162307`.
- Accepted main remains
  `d4766c3dbbf2c198142047ead4449d58dd47d485`; startup integration remains
  `8bdafc7f947a3a2c9856220abdc539437035bd21`; C61 and C83 remain pinned at
  `8e8177c031a7b3b9322d712af19970e13fa7a1bc` and
  `22cc6769aaf06d1e2c1275b064cc7ec29de3e371`.
- `test_analyze.py` passes 4/4. Python compilation and `git diff --check`
  pass.
- The positive credential-free browser/audio/tool workflow passes 18/18 in
  `254.711s` under child/aggregate bounds `90/300`; output includes
  `PROBE_TOOL_MARKER_9182` and `strict replay continuation`.
- The malformed/canceled control passes 3/3 in `31.847s` under bounds
  `60/180`. Both public reports have clean process groups, unchanged
  synthetic trees, credential scrubbing and bounded temporary-tree cleanup.
- `verify.py --mode all` passes provenance, both merge orders, behavior and
  attribution, handoff sequence, determinism, caller-tree mutation control,
  public checks, and all 19 negative fixtures.
- Source bindings are runner
  `705bc3167678b83158fc925e132fe39d1fdd4315c845360a8686eee8db9b6efe`, test
  `d07a668ae174dcc2e1e0791f4b570b0c41f22bd9f47989fb2b2f2ee469d40395`,
  verification
  `eb352a6de2507562858895fef4d63315b7839a7e706bbf6a274cb37b86e9d3b8`,
  positive report
  `5b2954bc390e4817a1470539d96f5e421a82b1bf95e0da8341657e4bec85125c`, and
  malformed/canceled report
  `9ae5451df7356d8129008bafba703b92c7cb03a4676810d8b19493ff5454363e`.

This is executor evidence only. It makes no CI-green, independent-review,
guarded-merge, C61/C83 acceptance, vertical, hardware/acoustic or project
completion claim. The C79 provider-audio terminal-drain failure remains
preserved and unwaived, and AUDIO, DEVICE, EMBED, SERVICE, TRACE, REPLAY,
FAILURES, QUALITY and PARITY remain open.
