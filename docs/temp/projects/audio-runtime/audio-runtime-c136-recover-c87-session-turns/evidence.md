# C136 recovery implementation checkpoint

At the start of implementation, admission was reverified with
`factory/scripts/project-control.py verify-work --type task --name
audio-runtime-c136-recover-c87-session-turns`, returning admitted for the sole
`audio-runtime/audio-runtime-v1` project. The isolated branch exactly matches
`prd.json.branchName`, the worktree is clean, and refreshed `origin/main`
is `bd6a1289218d1bef1a3af36e64e9d4496062416f`. The preserved C87 remote ref
still equals immutable PR 476/head
`28b5a9b18f67a4343ef9e12141ad5e5fc84ef18f`; C87 admission records
`previous_review_findings: []`.

The C87 implementation was transplanted as six new C136 commits on top of
current main, without changing the historical C87 branch or its evidence:
the feature, contract/architecture alignment, quality repair, and three C87
evidence checkpoints. Startup integration
`8bdafc7f947a3a2c9856220abdc539437035bd21`, planning main
`bd6a1289218d1bef1a3af36e64e9d4496062416f`, and fresh-main ancestry all
verify. The candidate delta is limited to the admitted session-turn source,
tests, runtime service, coverage manifests, and C87 provenance tree; shared
registries remain untouched.

Focused implementation evidence at the pre-evidence checkpoint passed:

- service session-turn normal tests: 42 tests in 3 packages, count 3;
- service session-turn race tests: 39 tests in 3 packages, count 3;
- deprecated CLI compatibility normal/race: 6/2 tests;
- separate external `GOWORK=off` consumer: pass;
- mutation, retirement/adapter, and owned/excluded-path verifiers: pass;
- credential-free bounded consumer runner: pass with one provider connection,
  two turns, copied audio/snapshot state, preserved invalid transition,
  isolated second service, and one close;
- bounded C136 public regression runner: both requested cases passed (three
  tests each), including the shipped credential-free audio/tool workflow and
  the existing interruption/tool continuation regression; output stayed under
  1 MiB and both child process groups were reaped with no survivors;
- formatting, focused vet, pinned staticcheck, pinned golangci-lint, coverage
  registration (188 packages/6 modules), changed-package coverage, Wire
  generation/check, and `git diff --check`: pass;
- accumulated normal and coverage session regressions: pass;
- accumulated race regressions: pass with an isolated Go build cache after an
  unrelated shared-cache import-file race in the first run.

The expected shared architecture gate remains blocked by the unreleased
migration leases: `make architecture-size-check` reports 13 findings,
specifically the generated sessionturns Wire file plus twelve stale downward
C87 baseline entries. No shared file was edited. No current-head script CI,
independent review, guarded merge, vertical probe, or project acceptance is
claimed.

Exact next actions are to commit and push this candidate, then retain C136
ownership while C110/C111/C112/C119 release the shared paths and C127 reaches
accepted main. After release, integrate the accepted current main, apply only
the demonstrated sessionturns registry/baseline entries permitted by the
manifest, rerun bounded focused/accumulated gates, and submit one changed head
to script CI without polling. Repair any exact C136 rejection on this same task.
