# C24 sanitization repair checkpoint

## Review inbox and ownership

The canonical `~default` board was read with the full `--max-results 500 --all`
query. The exact `audio-runtime-c24-ci-conflict-return` task row (`work-task-69`)
and concluded review row (`work-review-72`) carried the same latest rejection:

> Actionable sanitization defect in factory/scripts/ci-gate.py:62-71. _safe_text preserves ANSI/control bytes and leaks whitespace-separated bearer secrets (e.g. Authorization: Bearer TOPSECRET -> Authorization=[redacted] TOPSECRET). Adversarial review reproduced both leaks. Unit suites, graph tests, and public controls otherwise passed. Strip control/C1 characters and robustly redact authorization/token values; add regression controls, then rerun the focused suites and public matrix.

This repair remains within the admitted ownership lease:

- `factory/scripts/ci-gate.py`
- `factory/scripts/tests/test_ci_gate.py`
- this C24 evidence folder

No `factory.json`, runtime, graph, policy, lock, baseline, or other owner path
was changed.

## Cause and repair

Before `f32de8176cf54375d9fb958c8f32bf7a67232956`, `_safe_text` collapsed
whitespace and only matched a sensitive key followed directly by `:` or `=` and
one non-whitespace value. For `Authorization: Bearer TOPSECRET`, it redacted
only `Bearer`, leaving `TOPSECRET` in the envelope. ANSI escape sequences and
C1 bytes also survived the boundary.

The repair at `f32de8176cf54375d9fb958c8f32bf7a67232956` first removes ANSI
sequences and replaces C0/C1 bytes with separators, then collapses whitespace.
It redacts authorization/proxy-authorization values including bearer/basic
forms, token/password/secret/key assignments including quoted values, and
standalone bearer/basic credentials. Redaction occurs before the feedback limit,
so truncation cannot expose the original value. The existing waiter conflict,
head-identity, convergence, merged-state, and routing behavior is unchanged.

## Causal validation

All commands were run in the isolated worktree on source
`f32de8176cf54375d9fb958c8f32bf7a67232956`:

- `rtk proxy python3 -m unittest discover -s factory/scripts/tests -p test_ci_gate.py` — 16/16 passed.
- `rtk proxy python3 -m unittest discover -s factory/scripts/tests -p test_ci_wait.py` — 23/23 passed.
- `rtk proxy python3 -m unittest discover -s factory/scripts/tests -p test_factory_graph.py` — 2/2 passed.
- `rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c24-ci-conflict-return/public-controls.py --gate factory/scripts/ci-gate.py --cases conflict,unknown,stale-head,green --child-timeout-seconds 30` — passed; mutation control killed the conflict classification, and the public outcomes were `REJECTED`, `FAILED`, `REJECTED`, `ACCEPTED` with clean exit 0.
- `rtk proxy python3 -m py_compile factory/scripts/ci-gate.py factory/scripts/ci-wait.py factory/scripts/tests/test_ci_gate.py factory/scripts/tests/test_ci_wait.py` — passed.
- `rtk proxy git diff --check` — passed.

The public report records source revision
`f32de8176cf54375d9fb958c8f32bf7a67232956`, gate SHA256
`35002b3b3bb04261dc0fdb7c8c458482bfd523b4a091982c981abc70e00b2cc9`, waiter
SHA256 `611bf03c55b8138b17129bcce2d6b740457a9255491d8f523d2374e2423026d8`,
zero conflict/stale poll sleeps, bounded UNKNOWN polling, and one logical
convergence sleep for green. The direct adversarial controls covered
`Authorization: Bearer TOPSECRET`, `Authorization=Bearer TOPSECRET`,
standalone `Bearer TOPSECRET`, token/key assignments, quoted values, ANSI
sequences, and C1 bytes; none leaked and no control byte remained.

`origin/main` was freshly fetched at
`5d5afcb14d7b269378020809f5a2418c499ac94d`. Required startup integration
`8bdafc7f947a3a2c9856220abdc539437035bd21`, observed main, and baseline
`3194edd97aed588f7cdf2f8c58a69ac21da4c9ad` are ancestors of the candidate.

This is same-task repair evidence only. Script CI has not been polled or
claimed green; independent review, guarded merge, safe deployment, and fresh
post-merge vertical validation remain external handoff stages.
