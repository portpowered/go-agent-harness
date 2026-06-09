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

The landed provider capability and local validation evidence is preserved by
PR `#35` and remains visible in gateway capability and provider validation
surfaces. The landed dependency, result, context, and lifecycle evidence is
preserved by PR `#36`, including the public gateway README update from that
merge. The landed audit reconciliation evidence is preserved by PR `#34` and
the convergence validator evidence is preserved by PR `#38` in
`docs/internal/phase-4-api-contract-validator.md`.

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
