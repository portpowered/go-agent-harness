# C162 evidence ledger

Task: `audio-runtime-c162-repair-sessiontrace-canceled-close-precedence`
Project: `audio-runtime`; contract: `audio-runtime-v1`; factory session: `~default`

## Admission and preserved history

- `python3 "$FACTORY_ROOT/factory/scripts/project-control.py" verify-work --type task --name audio-runtime-c162-repair-sessiontrace-canceled-close-precedence` returned `{"status": "admitted", "project": "audio-runtime", "name": "audio-runtime-c162-repair-sessiontrace-canceled-close-precedence"}`.
- `prd.json.branchName` is `codex/audio-runtime-c162-repair-sessiontrace-canceled-close-precedence`, matching this isolated worktree.
- The worktree began clean at accepted/fetched PR 515 merge `97d3dcfb1e97a2611aa26b203a7f893442db4768`; `origin/main` resolved to the same revision. Required startup integration `8bdafc7f947a3a2c9856220abdc539437035bd21` and baseline `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad` are ancestors.
- Canonical negative history is preserved: C153 `work-task-42` remains terminal and independent `work-review-53` remains failed. Its exact finding is retained: when `p.closed` and a pre-canceled context are both ready, `Finish` can return only the retained close-path error; its 100-trial observation was 50/100 failures. The review also identified stale C153 evidence heads `2b86e658` and `ed5dc6ed` relative to PR 515 head `1c044cd6`; those are historical negative-evidence identities only. C162 provenance is the accepted PR 515 merge `97d3dcfb1e97a2611aa26b203a7f893442db4768` and the current source checkpoint below.
- The current board shows no other active writer on the two C162 service files. C162 owns only the two service files and this evidence directory; no peer path or predecessor checkpoint was changed.

## Failing-before: accepted-main causal regression

Before any `service.go` implementation change, C162 added only the owned behavioral regression `TestFinishCanceledClosePrecedence` to `service_test.go`. It uses a bounded close-start/completion barrier, a pre-canceled context, exact close sentinel, staged path and one-close assertion; its simultaneous-ready subtest runs 100 trials without sleeps or retry-until-green behavior.

- `rtk go test ./go-agent-runtime/services/sessiontrace/internal/service -run '^TestFinishCanceledClosePrecedence$' -count=100 -timeout=180s` exited `1`: `100 passed, 300 failed in 1 packages`.
- Representative unfiltered fixed-oracle run, `rtk proxy go test ./go-agent-runtime/services/sessiontrace/internal/service -run '^TestFinishCanceledClosePrecedence$' -count=1 -timeout=180s`, exited `1`. It reported `simultaneous-ready failures: cancellation=46/100 close-cause=54/100 staged-path=0/100`; the cancellation-at-completion case returned only `context canceled` and retained the staged path, losing the exact close sentinel.
- The prior accepted-main package command `rtk go test ./go-agent-runtime/services/sessiontrace/internal/service -run '^TestFinishCanceledClosePrecedence$' -count=100 -timeout=180s` had no test to execute (`Go test: No tests found`), so it was not treated as causal evidence. The existing service package had 14 passing tests before the regression was added.

No production implementation change, script-CI submission, review, merge, vertical probe or acceptance is claimed at this checkpoint. The next action is the smallest context-authoritative `prepared.close` repair, preserving shared close/timeout semantics, staged-path retention and exactly one `closeTrace` call.

## Repair and focused proof

The repair is limited to `prepared.close` in `service.go`. It checks the
caller context before waiting, rechecks it after completion, cancellation and
timeout selection, and joins a cancellation with an already-observed close
error. The existing first-caller timeout, shared `sync.Once` close and
publication/retention paths are unchanged.

- `rtk go test ./go-agent-runtime/services/sessiontrace/internal/service -run '^TestFinishCanceledClosePrecedence$' -count=100 -timeout=180s` exited `0` (`400` assertions passed).
- `rtk proxy env CGO_ENABLED=0 go test ./go-agent-runtime/services/sessiontrace/internal/service -tags=nomicrophone -run '^TestFinishCanceledClosePrecedence$' -count=100 -coverpkg=github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/... -coverprofile=docs/temp/projects/audio-runtime/audio-runtime-c162-repair-sessiontrace-canceled-close-precedence/canceled-close.cover.out -timeout=240s` exited `0` with `16.8%` covered statements in the sessiontrace service set.
- `rtk go test -race ./go-agent-runtime/services/sessiontrace/internal/service -run 'Finish.*(CanceledClosePrecedence|Close|Timeout|Cancel|Retain|Unpublished)' -count=20 -timeout=420s` exited `0` (`200` tests passed).
- `rtk go test ./go-agent-runtime/services/sessiontrace/... -count=1 -timeout=300s` and the matching race command exited `0` (`19` tests in `3` packages each). The nomicrophone coverpkg package run exited `0` with `87.7%` internal-service coverage and `1.8%` Wire coverage.
- Existing retained-path, redaction, no-overwrite, observer/timing and PCM controls exited `0` (`15` tests); targeted vet, `make wire-check`, `make fmt` and `git diff --check` exited `0`.
- Pinned `make lint LINT_BASE=origin/main` exited `0` with `0 issues` across all `15` modules. Pinned `make staticcheck` using Staticcheck `2026.1` exited `0`.
- `rtk proxy env CGO_ENABLED=0 go test ./agent-cli/test/integration -tags=nomicrophone -run '^TestSessionCommand_ActiveScheduledAudioPreservesToolResultLifecycle$' -count=1 -timeout=180s` exited `0` in `5.500s`.

The candidate currently changes only the two owned service source/test files
plus this owned evidence directory. No script-CI result, independent review,
guarded merge, vertical probe or project acceptance is claimed. The next
action is to commit the exact candidate and evidence, push the same branch,
open/update its PR, and return `ACCEPTED` to the script-owned CI gate without
polling.

## Current-head static repair and handoff provenance

- The latest canonical CI rejection is run `34786857773`, job `103803791131`,
  at head `aaf4804ead2f0c8b9e4769eea0b5f53471b55cdb`. Its complete static log
  reports exactly five architecture findings in the new regression: cognitive
  complexity `49 > 20`, cyclomatic complexity `30 > 20`, file lines `653 >
  600`, function lines `129 > 120`, and function statements `97 > 80`.
  Format, Wire, vet, lint, staticcheck, unit, race and the completed platform
  lanes were successful; the remaining CI lanes were still owned by the
  script gate at inspection.
- Code checkpoint `7b75776b7b2f5a861c0a5dd93e2ad7882c2448ce` refactors only the
  owned `service_test.go` regression into three bounded scenario functions and
  shared test helpers. It preserves the 100 simultaneous-ready trials, the
  pre-canceled and cancellation-at-completion barriers, exact close-cause and
  staged-path checks, and the one-call assertion. `service_test.go` is now
  exactly `600` lines, and `make architecture-size-check` passes at `202`
  packages, `1,940` files and `28,778` functions.
- On source checkpoint `7b75776b`, focused normal/race cancellation proof
  passed (`400` and `200` tests), the `nomicrophone` 100-trial coverpkg proof
  passed with `16.8%` sessiontrace-set coverage, full sessiontrace normal/race
  passed (`19` tests in `3` packages each), and full coverpkg passed with
  `87.7%` internal-service / `1.8%` Wire coverage. Retained-path/redaction/
  no-overwrite controls passed (`15` tests), targeted vet, Wire, fmt and diff
  checks passed, pinned golangci-lint `v2.9.0` reported `0 issues` across `15`
  modules, pinned staticcheck `2026.1` passed, and the credential-free
  scheduled-audio replay passed in `4.789s`.
- The later evidence-ledger update is documentation-only: no production
  source or executable build input changes after `7b75776b`. The candidate
  remains executor evidence only; script CI, independent review, guarded
  merge, immutable vertical validation and project acceptance are open.
