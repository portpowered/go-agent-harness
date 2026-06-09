# Phase 4 Authoritative Baseline Sync

This note records the branch relationship evidence for the Phase 4
authoritative baseline sync. It is a planner and reviewer reference only; it
does not close Phase 4 checklist rows and does not rerun the final Phase 4
convergence validator.

## Published Baseline Source of Truth

Use this note as the durable reviewer-facing source of truth for Phase 4
baseline planning after batch 017. The authoritative baseline is `origin/main`
at `6e785952affad9cc5d07458c84f9a45b755c72c0` or a descendant that preserves
that commit in ancestry. Older local snapshots and pre-merge factory branches
are not authoritative when they omit, predate, or have been superseded by the
landed remote evidence listed below.

Checklist rows in scope for this sync are `P4-API-01`, `P4-API-02`,
`P4-API-03`, `P4-API-04`, `P4-API-05`, `P4-API-06`, `P4-API-07`,
`P4-GATE-01`, and `CTRL-FAC-03`. This sync does not close those rows, does not
implement new public API contract repairs, and does not rerun the final Phase 4
convergence validator. It only publishes the branch-status and preservation
evidence reviewers need before future repair or validator work is planned.

No branch listed in this note currently requires conflict repair before it can
be reconciled with the authoritative baseline: the originally stale or
unmerged Phase 4 branches are now ancestors of `origin/main` through PRs `#37`
and `#38`. Remaining follow-up is therefore planning disposition only:
future Phase 4 work should consume `origin/main` or a descendant, keep the
landed evidence from PRs `#33` through `#38`, and avoid treating older branch
heads as replacement baselines.

## Baseline Evidence

Evidence captured after fetching `origin` on 2026-06-09 04:16 UTC:

| Ref | Commit | Relationship |
| --- | --- | --- |
| local `main` | `34fc154e4e519cde4b35a91e5d10dd184a76ea71` | Local snapshot containing setup-workspace planner batch artifact tolerance. |
| `origin/main` | `6e785952affad9cc5d07458c84f9a45b755c72c0` | Remote authoritative baseline after batch 017 merges. |
| `phase-4-authoritative-baseline-sync` | `6e785952affad9cc5d07458c84f9a45b755c72c0` before this note | Fast-forwarded to `origin/main`, so the work branch starts from the remote authoritative baseline and remains a descendant of local `main`. |

The local `main` commit `34fc154e4e519cde4b35a91e5d10dd184a76ea71`
is an ancestor of `origin/main` and of this work branch. That commit changed:

- `factory/scripts/setup-workspace.py`
- `factory/scripts/tests/test_setup_workspace.py`
- `factory/docs/overview.md`

Those surfaces are the setup-workspace planner batch artifact tolerance evidence
that must remain preserved by later sync stories.

## Batch 017 Merge Evidence

`origin/main` includes these reviewer-visible batch 017 merge commits:

| Pull request | Merge commit | Subject |
| --- | --- | --- |
| `#33` | `89e3e02` | `Merge pull request #33 from portpowered/phase-4-api-contract-repair-validator` |
| `#34` | `7886f0a` | `Merge pull request #34 from portpowered/phase-4-audit-validator-015-reconciliation` |
| `#35` | `2853c1e` | `Merge pull request #35 from portpowered/phase-4-provider-capabilities-local-validation-contract` |
| `#36` | `b21f338` | `Merge pull request #36 from portpowered/phase-4-dependency-result-context-lifecycle-contract` |

At the time this note was started, `origin/main` also included later Phase 4
merge commits `a64ee15` for `#37` and `6e78595` for `#38`. Those later commits
make `origin/main` the reproducible baseline for this work item; using the
older local `main` snapshot as authoritative would omit landed remote evidence.

## Batch Branch Classification

Evidence recaptured on 2026-06-09 04:21 UTC shows that the current
authoritative baseline is ahead of the original sync prompt assumptions:

| Branch or work item | Current ref / merge evidence | Relationship to `origin/main` at `6e785952affad9cc5d07458c84f9a45b755c72c0` | Classification |
| --- | --- | --- | --- |
| `phase-4-api-contract-repair-validator` | Merge commit `89e3e02` / PR `#33` | Already merged into `origin/main`. | `landed` |
| `phase-4-audit-validator-015-reconciliation` | Merge commit `7886f0a` / PR `#34` | Already merged into `origin/main`. | `landed` |
| `phase-4-provider-capabilities-local-validation-contract` | Merge commit `2853c1e` / PR `#35` | Already merged into `origin/main`. | `landed` |
| `phase-4-dependency-result-context-lifecycle-contract` | Merge commit `b21f338` / PR `#36` | Already merged into `origin/main`. | `landed` |
| `phase-4-typed-errors-stream-terminal-contract` | Local and remote branch head `b50b6219f22c16ef97649842cb665eb8aec16d8f`; merge commit `a64ee15` / PR `#37` | Branch head is an ancestor of `origin/main`; `origin/main` is not an ancestor of the branch head. | `landed`; the old unmerged-candidate state is superseded by PR `#37`. |
| `phase-4-api-contract-convergence-validator` | Local branch head `4029637d13a899144f70f30736efc0da533c1650`; merge commit `6e78595` / PR `#38` | Branch head is an ancestor of `origin/main`; `origin/main` is not an ancestor of the branch head. | `landed`; the old stale-validator warning is superseded by PR `#38`. |

## Landed Phase 4 Repair Evidence Preservation

This sync branch is a descendant of `origin/main` at
`6e785952affad9cc5d07458c84f9a45b755c72c0`, so it preserves the already
landed Phase 4 repair evidence instead of replaying, replacing, or deleting it.
The preserved evidence is:

| Evidence area | Landed source | Preserved reviewer artifacts |
| --- | --- | --- |
| Provider capability and local validation | PR `#35`, merge commit `2853c1e` | `go-llm-gateway/pkg/capabilities`, `go-llm-gateway/pkg/gateway/capabilities_test.go`, provider capability tests, `go-llm-gateway/README.md`, `go-llm-gateway/docs/development.md`, and the provider capability rows in `docs/architecture/contract-gap-audit.md` plus `docs/internal/phase-4-api-contract-repair-validator.md`. |
| Dependency, result, context, and lifecycle contracts | PR `#36`, merge commit `b21f338` | `docs/architecture/dependency-result-contracts.md`, `docs/internal/phase-4-dependency-result-context-lifecycle-contract.md`, `agentloop.ExecuteResult.FinalText()`, `agentloop.Stream.Outcome()`, `messages.TypedBuffer` context helpers, `messages.SendSessionWithOutcome`, `testing.SessionReplayer.Outcome()`, session inferencer request tests, prompt-detail tests, replay lifecycle tests, and CLI session lifecycle tests. |
| Audit reconciliation and validator provenance | PR `#34`, merge commit `7886f0a`; later convergence evidence in PR `#38`, merge commit `6e78595` | `docs/architecture/contract-gap-audit.md`, `docs/internal/phase-4-validator-015-provenance.md`, and `docs/internal/phase-4-api-contract-validator.md`. |

This preservation story does not implement new public API contract repairs. It
also does not mark `P4-API-01`, `P4-API-02`, `P4-API-03`, `P4-API-04`,
`P4-API-05`, `P4-API-06`, `P4-API-07`, or `P4-GATE-01` complete in
`docs/internal/checklist.md`; their current status remains owned by the
underlying repair and validator artifacts listed above.

The preservation claim is reproducible with:

```sh
git merge-base --is-ancestor 2853c1e HEAD
git merge-base --is-ancestor b21f338 HEAD
git merge-base --is-ancestor 7886f0a HEAD
git merge-base --is-ancestor 6e78595 HEAD
git ls-tree -r --name-only HEAD -- docs/architecture/contract-gap-audit.md docs/architecture/dependency-result-contracts.md docs/internal/phase-4-api-contract-validator.md docs/internal/phase-4-dependency-result-context-lifecycle-contract.md docs/internal/phase-4-validator-015-provenance.md
```

## Setup Workspace Artifact Tolerance Preservation

The setup-workspace planner batch artifact tolerance from local `main` commit
`34fc154e4e519cde4b35a91e5d10dd184a76ea71` remains present in this baseline
through ancestry from `origin/main`. The preserved behavior is implemented in
`factory/scripts/setup-workspace.py` by the bounded planner-owned dirty path
allowlist:

- `docs/internal/checklist.md`
- `docs/internal/progress.txt`
- submitted planner batch request artifacts matching
  `docs/internal/*-batch-*.json`
- requested setup inputs under `tasks/todo/<prd-name>.json` and optional
  `tasks/todo/<prd-name>.md`

The behavior remains covered by committed runtime tests in
`factory/scripts/tests/test_setup_workspace.py`, including planner-owned dirty
root files, submitted batch request artifacts, reuse with planner-owned dirty
root files, and explicit failure for unrelated dirty root state. The factory
operator contract remains documented in `factory/docs/overview.md` under
`Setup Workspace Ownership Contract`.

`CTRL-FAC-03` remains listed in `docs/internal/checklist.md` as reviewer
evidence for the Phase 2 setup-workspace repair. This sync item does not change
that checklist row, does not mark it complete, and does not expand the
setup-workspace contract beyond preserving the already landed tolerance.

Because `phase-4-api-contract-convergence-validator` is now an ancestor of
`origin/main`, it is no longer a stale branch that would delete merged batch
017 evidence if treated as authoritative. That stale-state risk applied before
PR `#38` landed; the current reviewer action is to use `origin/main` at
`6e785952affad9cc5d07458c84f9a45b755c72c0` or a descendant of it as the only
authoritative baseline. Likewise,
`phase-4-typed-errors-stream-terminal-contract` is no longer an unmerged
candidate; its completed factory branch is landed through PR `#37`.

The classifications above are reproducible with:

```sh
git log --oneline --grep='Merge pull request #3[3-8]' origin/main
git rev-parse origin/main phase-4-typed-errors-stream-terminal-contract phase-4-api-contract-convergence-validator
git merge-base --is-ancestor phase-4-typed-errors-stream-terminal-contract origin/main
git merge-base --is-ancestor phase-4-api-contract-convergence-validator origin/main
```

## Baseline Decision

The authoritative Phase 4 baseline for this sync is `origin/main` at
`6e785952affad9cc5d07458c84f9a45b755c72c0`, with this work branch
fast-forwarded to that commit before adding sync evidence. This is observable
with:

```sh
git rev-parse main origin/main HEAD
git merge-base --is-ancestor 34fc154e4e519cde4b35a91e5d10dd184a76ea71 HEAD
git log --oneline --grep='Merge pull request #3[3-6]' origin/main
```

Reviewers should treat future commits on
`phase-4-authoritative-baseline-sync` as changes on top of that authoritative
remote baseline, not as evidence that the older local `main` snapshot competes
with `origin/main`.
