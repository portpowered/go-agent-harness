# Phase 2 Factory Worktree Hygiene Validator

## Subject Under Review

This validator reviews the completed
`phase-2-factory-worktree-hygiene-repair` slice. Run this pass only after that
implementation work is complete and the branch under review is intended to
represent the candidate Phase 2 baseline for factory worktree hygiene
convergence.

The validator inspects the delivered repository state as an observable surface.
It does not reopen the repair scope or expand into general repository CI
coverage.

## Scope

This validator records findings for exactly three areas:

1. Checklist convergence against `CTRL-FAC-01`, `CTRL-FAC-02`, and
   `CTRL-FAC-03`
2. Repaired `setup-workspace` behavior
3. Durable-queue recovery status

Every finding records:

- `outcome`: `pass`, `fail`, or `uncertain`
- supporting evidence tied to observable repository state or direct runtime
  exercise
- affected runtime surfaces, work IDs, branch names, or work item names
- required follow-up or manual recovery actions, if any

Duplicate CI review is out of scope. Existing deterministic checks such as
`make test-factory-scripts`, `make typecheck`, and `make test` may be cited
only when they provide direct evidence for factory convergence behavior.

## Evidence Inputs

This convergence pass cites the following authoritative repository surfaces:

- `docs/internal/checklist.md`
- `factory/docs/overview.md`
- `factory/scripts/setup-workspace.py`
- `factory/scripts/tests/test_setup_workspace.py`
- durable queue inspection commands and their observed output, such as
  `you work list` and `you session list`
- any direct `setup-workspace` exercise or deterministic reproduction evidence
  recorded during the validator run

If queue state, runtime evidence, or work item metadata are unavailable during
the validator run, the validator must record that gap explicitly instead of
inferring a pass from prior batch history.

## Shared Finding Template

Use this shape for every finding group:

### [Finding Group Name]

- `outcome`: `pass` | `fail` | `uncertain`
- `checklist rows / commitments inspected`:
- `affected files / runtime surfaces / work IDs`:
- `evidence`:
- `required follow-up or manual recovery`:

## Outcome Rules

### `pass`

Use `pass` only when the reviewed repository state or direct validator exercise
shows the inspected control or recovery claim is satisfied and no further
operator action is required for that finding.

### `fail`

Use `fail` when observed setup behavior, checklist evidence, or durable queue
state directly contradicts the intended factory worktree hygiene repair or
still requires concrete manual recovery.

### `uncertain`

Use `uncertain` when the validator cannot verify the claim from current
repository state and observable runtime evidence alone. Missing queue access,
missing runtime evidence, or incomplete reproduction data belong here unless
the absence itself contradicts a required claim and should therefore be marked
`fail`.

## Required Finding Groups

### Checklist Convergence

The validator must inspect `CTRL-FAC-01`, `CTRL-FAC-02`, and `CTRL-FAC-03`
directly from `docs/internal/checklist.md` and map each row to observed
repository evidence. Each row must include the cited evidence surface, the
affected runtime surface, and the reason the outcome is `pass`, `fail`, or
`uncertain`.

The validator may cite `CTRL-FAC-04` as supporting deterministic-proof context,
but the required convergence verdict must still be anchored to
`CTRL-FAC-01` through `CTRL-FAC-03`.

### Repaired `setup-workspace` Behavior

The validator must exercise or deterministically reproduce the repaired
`factory/scripts/setup-workspace.py` path and conclude whether the previously
reproduced concurrent setup failure is:

- fixed
- still reproducible
- inconclusive

The finding must name the evidence used for that conclusion, including the
observed setup outcome and any affected branch names, worktree paths, or work
item names involved in the validation run.

### Durable-Queue Recovery Status

The validator must inspect the durable queue after the repair-focused setup
validation and determine whether any `plan`, `task`, or `thoughts` work items
remain stranded because of the prior worktree hygiene failure mode.

For each stranded item found, the validator must name the exact work ID or work
item name and record the exact recommended manual `you work move` action or
operator recovery step. If no stranded work remains, the finding must state
that no manual recovery is required.

## Reporting Contract

The final validator output must:

- name `phase-2-factory-worktree-hygiene-repair` as the repaired slice under
  review
- separate findings by checklist convergence, repaired setup behavior, and
  durable-queue recovery status
- use only `pass`, `fail`, or `uncertain`
- cite direct evidence and affected surfaces or work IDs for every finding
- record exact manual `you work move` actions or explicitly state that none are
  required
- end with one overall convergence verdict for Phase 2 factory worktree
  hygiene: `pass`, `fail`, or `uncertain`

## Reproducible Execution

Generate the reviewer-facing report from the current repository and queue state
with:

```sh
python3 factory/scripts/validate_worktree_hygiene_convergence.py \
  --write-report docs/internal/phase-2-factory-worktree-hygiene-convergence-report.md
```

That command must:

- run the committed `setup-workspace` runtime suite directly as validator
  evidence
- inspect live durable queue state through `you session list` and
  `you work list --session <id>`
- rewrite `docs/internal/phase-2-factory-worktree-hygiene-convergence-report.md`
  from the current run instead of relying on a hardcoded snapshot
