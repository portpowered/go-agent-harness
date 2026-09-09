# C22 causal repair evidence

- Work: `audio-runtime-c22-config-permission-causal-repair`
- Branch/worktree: `codex/audio-runtime-c22-config-permission-causal-repair`
- Fetched `origin/main`: `e4137eba6a6499142f50701609c1149afc71db84`
- Implementation checkpoint: `e927682a8e7da9912d3ea9fd2cfb2b01db50c653`
- Exact evidence HEAD: `6560f06e0794115cc454548344e41ef9e088ec5f`
- Required startup and baseline pins are ancestors of the candidate:
  `8bdafc7f947a3a2c9856220abdc539437035bd21` and
  `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`.

## Causal baseline

`baseline.json` runs the unchanged
`TestConfigStorageCommitPreservesPermissionsAndPublishesAtomically` in two
fresh POSIX child processes and a diagnostic copy of the same source. On
`e4137eba`, the exact test passes under umask `022` and fails under `077` with
`config mode = 600, want 640`. The diagnostic copy records raw requested `0640`
as `0640` under `022` but `0600` under `077`, while explicit `chmod 0640`,
explicit `0600`, and absent-file default `0600` controls remain stable.

## Candidate evidence

- `candidate.json`: exact test passes under both child masks in 17.627s; all
  eight diagnostic mask/scenario observations pass.
- `public-config.json`: rebuilt same-source `yui` passes six bounded cases under
  `022` and `077`: existing `0640`, absent private `0600`, and probe-time stale
  revision conflict for each mask. Bytes, modes, success/error output and lock/
  temp cleanup are checked.
- `public-ask.json`: the same binary records a deterministic local SSE answer,
  stops and joins the helper, then replays the capture after helper shutdown;
  both children pass the 60-second bound.
- `yui` SHA256: `1bb25bcadcb578c854f62af5616ed583b660bf044ea94ce4ade7dd346b347739`.

No live Realtime, credential, physical-device, or acoustic evidence is claimed.
The candidate is ready for executor handoff to script-owned current-head CI;
CI, independent review, guarded merge, and post-merge vertical acceptance are
not claimed by this evidence.
