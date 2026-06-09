# Phase 4 Authoritative Baseline Sync

This note records the branch relationship evidence for the Phase 4
authoritative baseline sync. It is a planner and reviewer reference only; it
does not close Phase 4 checklist rows and does not rerun the final Phase 4
convergence validator.

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
