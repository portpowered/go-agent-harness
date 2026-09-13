# C165 current-main integration evidence

This checkpoint records the C165 recovery/adoption work for the admitted
`audio-runtime` project. The C165 dispatch worktree and PRD branch are
`codex/audio-runtime-c165-recover-c161-session-failure-ci-transition`. The
admitted PRD directs this task to preserve and operate the authoritative C119
candidate worktree and PR #505; no second project, acceptance waiver, or new
implementation branch was created.

## Admission and preserved history

- `project-control.py verify-work --type task --name audio-runtime-c165-recover-c161-session-failure-ci-transition` returned `status=admitted`, project `audio-runtime`.
- The C161 predecessor task `work-task-67` is preserved as a terminal model-free transition failure. Its C119 candidate remained clean and pushed; no predecessor mutation or review finding was rewritten.
- PR #505 remains the authoritative candidate: operational branch `codex/audio-runtime-c119-retire-cli-session-failure-projection`, predecessor clean/pushed head `c8734b36e36eac3c7ce4c636bd179ae13f19a283`.
- Historical Script CI run `34786121940` remains the exact nine-lane successful run for `c8734b36`; this evidence is CI evidence only and is not a claim that the current head is green.
- Historical run `34747817964` remains retained as the unwaived peer high-rate audio negative with exactly `6400` lost samples. C119/C165 does not relabel it or change excluded audio/device/transport paths.

## Current-main reconciliation

- A fresh `origin/main` fetch resolved to `2c79ec6a931c3e86944d6a625d0a5060b4f85aa0`.
- The C119 worktree integrated it with non-rewriting merge commit
  `e813bd08cbb3e4b394fe6acfe20d6e7ef3727e5c` (`merge current main for C165 CI transition`).
- Required ancestry is present for startup
  `8bdafc7f947a3a2c9856220abdc539437035bd21`, accepted main
  `97d3dcfb1e97a2611aa26b203a7f893442db4768`, and current main.
- The merge was clean and did not reset, rebase, or force-push. The running
  host checkout and predecessor worktrees were not merged or reset. The
  merge introduced only current-main C156 files; the C119 candidate diff from
  current `origin/main` remains confined to the admitted C119 paths and the
  released shared Wire/architecture reconciliation.

## Focused causal and accumulated verification

- `verify.py --mode final` exited `0` with accepted artifact
  `artifacts/verify-78496.json`: service normal and race repetitions, the
  isolated `GOWORK=off` external consumer, unchanged CLI caller regressions,
  and both causal mutants passed their required oracles. The
  `drop-terminal-facts` and `convert-cancellation-to-failure` mutants both
  exited `1` and `survived=false`.
- `verify.py --mode retirement-and-owned-paths` exited `0` with accepted
  artifact `artifacts/verify-70025.json`. The adapter is exactly `74` lines,
  the legacy `session_diagnostics_failure.go` implementation is absent, and
  accepted-main retirement remains `246` CLI production lines.
- `COUNT=3 rtk proxy bash scripts/test-session-ci-regressions.sh all` exited
  `0` in normal, coverage, and race modes. The matrix covered transport and
  integration session regressions, device/provider regressions, the 20-trial
  high-rate audio/tool regression, the replay/PCM/transcript negative controls,
  and tool-result continuation.

## Bounded quality gates

The integrated tree passed `make fmt`, `make vet`, pinned golangci-lint 2.9.0
with `0 issues` in all 15 modules, pinned Staticcheck 2026.1, `make wire-check`,
`make coverage-registration` (`195` workspace packages),
`make architecture-size-check` (`205` packages, `1,947` files, `28,830`
functions), and `git diff --check`.

## Handoff boundary

This is executor evidence. No current-head Script CI result, independent
review, guarded merge, fresh vertical acceptance, or project acceptance is
claimed. After this evidence and PR-body checkpoint are committed and pushed
on the same PR #505 branch, the exact next action is to return `ACCEPTED` to
the script-owned CI gate without polling it. If that gate rejects, inspect the
exact failed check/log, repair only with an ownership-grounded cause, and
resubmit this same task with `CONTINUE`.
