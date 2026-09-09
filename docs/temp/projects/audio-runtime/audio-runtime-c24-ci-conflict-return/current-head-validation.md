# C24 current-head validation checkpoint

Validation ran in the isolated worktree on source revision
`1dc010d29bd5752f83e4d77cb778f1b0941144f4` after the canonical coverage
rejection was reconciled. `origin/main` was freshly fetched at
`5d5afcb14d7b269378020809f5a2418c499ac94d`; startup integration
`8bdafc7f947a3a2c9856220abdc539437035bd21`, observed main, the fetched main,
and baseline `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad` are ancestors of the
candidate. Admission remains verified for the single admitted project and the
branch matches `prd.json.branchName`.

Focused controls:

- `rtk proxy python3 -m unittest discover -s factory/scripts/tests -p test_ci_wait.py` — 23/23 passed.
- `rtk proxy python3 -m unittest discover -s factory/scripts/tests -p test_ci_gate.py` — 16/16 passed, including the accumulated sanitization regressions.
- `rtk proxy python3 -m unittest discover -s factory/scripts/tests -p test_factory_graph.py` — 2/2 passed.
- `rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c24-ci-conflict-return/public-controls.py --gate factory/scripts/ci-gate.py --cases conflict,unknown,stale-head,green --child-timeout-seconds 30` — passed with native `REJECTED`, `FAILED`, `REJECTED`, `ACCEPTED` and clean exit 0.
- `rtk proxy python3 -m py_compile factory/scripts/ci-gate.py factory/scripts/ci-wait.py factory/scripts/tests/test_ci_gate.py factory/scripts/tests/test_ci_wait.py docs/temp/projects/audio-runtime/audio-runtime-c24-ci-conflict-return/public-controls.py` — passed.
- `rtk git diff --check` — passed.

The public matrix recorded zero requested sleeps for current conflict and stale
head cases, bounded logical polling for UNKNOWN, one logical convergence sleep
for green, sanitized native feedback, and a mutation control that failed closed
when conflict classification was removed. No live GitHub writes or CI polling
was performed. The prior CI failure remains the out-of-scope
`go-agent-loop/pkg/agentloop` cancellation assertion documented in
`ci-rejection-34399798166.md`.
