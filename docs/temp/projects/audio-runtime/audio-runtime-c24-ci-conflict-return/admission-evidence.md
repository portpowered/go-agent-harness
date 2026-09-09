# C24 admission and ownership checkpoint

- `project-control.py verify-work --type task --name audio-runtime-c24-ci-conflict-return` returned `{"status": "admitted", "project": "audio-runtime", "name": "audio-runtime-c24-ci-conflict-return"}`.
- The canonical `~default` board row is saved in `admission-board.json`: `work-task-69`, exact isolated worktree and branch `codex/audio-runtime-c24-ci-conflict-return`; no C24 review row or rejection feedback exists at admission.
- `prd.json.branchName` matches the checked-out branch. The worktree started clean at `5d5afcb14d7b269378020809f5a2418c499ac94d`, and `git fetch origin main` kept `origin/main` at that same revision. Startup integration `8bdafc7f947a3a2c9856220abdc539437035bd21`, observed main `5d5afcb14d7b269378020809f5a2418c499ac94d`, and baseline `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad` are ancestors.
- The governing C20 decision identifies PR #412's explicit `OPEN`, current-head `mergeable=CONFLICTING`, `mergeStateStatus=DIRTY` and absent checks as the deterministic failure. C20's recording/runtime paths remain outside this task's owned scope.
- The only implementation changes are the admitted factory waiter/adapter and their tests; the public-control runner/report are under the admitted C24 evidence folder. `factory.json`, runtime, graph, policy, locks and other owners are unchanged.
