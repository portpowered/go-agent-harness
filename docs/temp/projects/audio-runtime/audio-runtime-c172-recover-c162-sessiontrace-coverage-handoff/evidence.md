# C172 recovery evidence ledger

Task: `audio-runtime-c172-recover-c162-sessiontrace-coverage-handoff`
Project: `audio-runtime`; contract: `audio-runtime-v1`; session: `~default`

## Admission and preserved history

- `project-control.py verify-work --type task --name audio-runtime-c172-recover-c162-sessiontrace-coverage-handoff --root "$FACTORY_ROOT"` returned `{"status": "admitted", "project": "audio-runtime", "name": "audio-runtime-c172-recover-c162-sessiontrace-coverage-handoff"}`.
- `prd.json.branchName` and the isolated branch are both `codex/audio-runtime-c172-recover-c162-sessiontrace-coverage-handoff`; the worktree was clean before integration. No second project, acceptance waiver, reset, rebase, squash or host-checkout mutation was used.
- The exact C162 candidate `67865c27ac4c065f3e8833d48ad2b7cea4c329a2` was adopted by fast-forward, preserving its C153/C158/C162 history, terminal `work-task-70`, failed `work-review-85`, and PR `#519`.
- C154 dependency evidence was inspected before integration: PR `#520` head `1f22332894443cd79e3ad71a97292ee1a3612d32` has nine green current-head lanes and was guarded-merged as `8490f8dcad63adde99036016e1e7ffd9ecf61e34`; canonical `work-review-120` is terminal-complete. C172 authored no C154 room/coordinator/test path.
- The current validation-policy override dated 2026-09-13 removes model-driven vertical acceptance and forbids staging validation Work or waiting for a validator. No vertical PASS is claimed here; that older prerequisite is retained as an explicit limitation.

## Preserved failed handoff and review findings

- PR `#519` run `34789726222` at exact head `67865c27ac4c065f3e8833d48ad2b7cea4c329a2` had eight green lanes and only `CI (coverage)` failed. The first coverage log reports `TestRunRoom_BoundGraceExpiryCancelsActiveResponseCleanly/duration` with `active response cancellations = 0, want exactly one`; this remains C154-owned historical negative evidence, not a C162/sessiontrace defect.
- `work-review-85` rejected the earlier C162 handoff because the green candidate was stale against `origin/main=2c79ec6a931c3e86944d6a625d0a5060b4f85aa0`. The review required current-main integration and exact-head resubmission; this recovery performs that merge.
- The predecessor `work-review-53` finding remains preserved: the old C153 implementation produced a 50/100 simultaneous-ready cancellation failure and lost the exact close sentinel. C162's 100-trial oracle is retained unchanged.

## Non-rewriting integration and scope

- Accepted C154 main `8490f8dcad63adde99036016e1e7ffd9ecf61e34` was merged into the adopted C162 line as `1a547d5313eb2ffb084f2d8f46c815781f258368`.
- Fresh main advanced during bounded verification to `1b1c0296b9471b930c2b290f3bf6fa10559957de` (CI partition/deduplication changes only) and was merged as `f1de875ee`. The final candidate must be checked against this fresh main before handoff.
- Required ancestry is retained: startup integration `8bdafc7f947a3a2c9856220abdc539437035bd21`, planning main `2c79ec6a931c3e86944d6a625d0a5060b4f85aa0`, C162 `67865c27ac4c065f3e8833d48ad2b7cea4c329a2`, accepted C154 `8490f8dcad63adde99036016e1e7ffd9ecf61e34`, and fresh `origin/main` `1b1c0296b9471b930c2b290f3bf6fa10559957de` are ancestors.
- Before this evidence update, `git diff --name-status origin/main...HEAD` contained only the two C162 sessiontrace service files and the inherited C162 evidence directory. C154 changes are inherited from main, not authored by C172.

## Focused and accumulated proof

All commands were bounded and run without credentials or Realtime access on the merged candidate:

- Room-bound causal control: normal count 20 passed (`60` assertions); `CGO_ENABLED=0 -tags=nomicrophone` coverpkg count 20 passed at `7.9%`.
- C162 close-precedence causal control: normal count 100 passed (`400` assertions); nomicrophone coverpkg count 100 passed at `16.8%`.
- Sessiontrace targeted race control passed `200` tests; full normal and race suites each passed `19` tests in `3` packages; nomicrophone coverpkg passed with `87.7%` internal-service and `1.8%` Wire coverage.
- Retained-path/redaction/no-overwrite controls passed `15` tests at count 5; the C154 completion regression `TestRunRoom_MaxTurnsDrainsResponseAlreadyInFlight` passed count 20.
- Credential-free scheduled-audio/tool lifecycle replay passed in the accumulated matrix and in the explicit nomicrophone run (`4.479s`); the expected mismatch/PCM/transcript negative controls remained rejecting their mutations.
- `COUNT=1 scripts/test-session-ci-regressions.sh all` passed normal, coverage and race modes, including high-rate tool/audio, simulated-device, provider-tool continuation, lifecycle and cleanup controls.
- Targeted vet, `make wire-check`, `make fmt`, `make architecture-size-check` (`202` packages, `1,941` files, `28,805` functions), pinned `make staticcheck` `2026.1`, pinned `make lint LINT_BASE=origin/main` `v2.9.0` (`0 issues` in all `15` modules), and `git diff --check` passed.
- Refreshed ignored coverage profiles are retained for this handoff: `canceled-close.cover.out` SHA-256 `a1c1bc1d05421bda4b083b1593b424f77a710271ca51ed0ce6cb3f814e167759`, `room-bound.cover.out` SHA-256 `828e35f43d0377e3378e935c4e91c36c286fcef281acf4b39e9caa6f5ca309f6`, and `sessiontrace.cover.out` SHA-256 `8c38a014bf9edd606f8046c46a21042490fb436396d2a87f7409bac823ca0131`.

## Gate boundary and next action

This ledger is executor handoff evidence only. It does not claim current-head SCRIPT CI success, independent C172 review, guarded merge, vertical acceptance or project acceptance. After the final clean-source recheck, push one changed head to the existing PR `#519` remote branch, update its body with this exact provenance, submit that changed SHA once to SCRIPT CI, and stop without polling. Any exact CI rejection returns to this same task for causal repair.
