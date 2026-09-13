## Summary

- Extract session failure normalization, provider-close synthesis, cancellation/non-terminal exclusion, output-state derivation, continuation projections, unsupported-tool diagnostics, synchronized first-observation state, and original-error preservation into go-agent-runtime/services/sessionfailure.
- Add the dedicated public Wire seam and retain an exactly 86-line Deprecated CLI forwarding adapter; delete the 246-line legacy implementation.
- Add the separate GOWORK=off consumer, adapter regressions, bounded causal-mutation verifier, and credential-free shipped-yui replay runner.

## Focused evidence

- go test ./go-agent-runtime/services/sessionfailure/... -count=1: 22 passed.
- go test -race ./go-agent-runtime/services/sessionfailure/... -count=3: 66 passed.
- Adapter-focused race tests: 12 passed; combined focused legacy/adapter regressions: 30 passed.
- verify.py --mode positive-and-two-mutations: accepted; both targeted mutants fail with --- FAIL: output.
- External consumer with GOWORK=off: passed and proves Wire isolation, errors.Is, errors.As, defaults, exclusions, close output, and continuation projection.
- run.py --case provider-error --case synthesized-provider-close --case cancellation-negative --case healthy-audio-tool-continuation --child-timeout 60 --aggregate-timeout 240: accepted; pinned fixtures, bounded process groups, provider failure/close terminal manifests, cancellation negative, and healthy audio/tool output/effect.
- session_diagnostics_failure.go is absent; adapter is 86 lines; accepted-main retirement accounting is 246 CLI production lines; excluded caller/shared fingerprints remain unchanged.

## Fresh executor checkpoint

- Exact source head: `ba5337e411d97f932ff426b7d1eed588f1d550ee`; the worktree is clean and the pushed branch/PR #505 point at this head.
- Focused normal/race tables and accumulated named session diagnostics/cancellation regressions pass; the separate `GOWORK=off` consumer passes; `verify.py --mode positive-and-two-mutations` is accepted and both mutants fail their intended semantic oracles.
- `verify.py --mode retirement-and-owned-paths` is accepted. Fresh replay evidence `run-1789274464-93155.json` is accepted from the same source head and rebuilt yui: provider-error and synthesized provider-close surface typed terminal evidence, cancellation-negative remains excluded, and healthy audio/tool continuation produces output/effect; every child is bounded, reaped, and leaves no process group survivor.
- `origin/main` was freshly fetched at `ea53be13ce5e4ef14fd8c89c695c21744a1f7686` and is not yet an ancestor of this candidate. C79 PR #470 remains open and its shared Wire/architecture registries remain leased; no shared-file edit or stale-main integration is claimed.

## Current executor handoff

- Exact source head is `221da3ffed74be69812079ed7c237605cd19d4c7`; the worktree and pushed PR #505 are clean at this head. Task admission reverified as `admitted`; `prd.branchName` matches; no second project or acceptance waiver is present.
- Fresh bounded evidence remains green: service normal `22` tests, service race `66`, external `GOWORK=off` consumer, `11` caller regressions, causal artifact `verify-646.json` with both mutants failing their intended oracles, and retirement/owned-path artifact `verify-647.json` with `86` adapter lines and `246` retired accepted-main CLI lines.
- `COUNT=1 bash scripts/test-session-ci-regressions.sh all` passed normal, coverage, and race modes. Replay artifact `run-1789284027-1152.json` is accepted from this source head and rebuilt yui `6d9ebab3bf94eded5f9e5d5bcb9db43256bc640aba199b6b6bce6891c121db06`; all four credential-free cases passed, all child groups were reaped, and no survivors remained.
- Fresh `origin/main` is `ea53be13ce5e4ef14fd8c89c695c21744a1f7686` and is not an ancestor of this candidate. C79 PR #470 still holds the shared Wire/architecture lease, so integration and shared-file reconciliation remain deferred. No Script CI result, independent review, merge, or vertical acceptance is claimed.

## Static budget repair checkpoint

- The full current-head CI static log for run `34745142367`, job `103691571589`, at submitted head `d905345d094805d3b11457a48cda038494cdddad` had exactly two findings: `agentruntime` was `238 > 237`, and `services/sessionfailure/wire/wire_gen.go` was unregistered.
- The C119-owned `session_failure_projection_test.go` had no callers outside itself and duplicated the service/unchanged-caller coverage. Commit `20fd1e5` deletes that 97-line test-only file; the live agentruntime package is now exactly `237` Go files. The production adapter remains exactly `86` lines, the legacy implementation remains deleted, and no baseline or registry path was changed.
- Post-repair `rtk make architecture-size-check` reports exactly one remaining issue: generated-file-spoof for `services/sessionfailure/wire/wire_gen.go`. Service normal/race tests, the GOWORK=off consumer, unchanged caller regressions, both causal mutants, and the accumulated normal/coverage/race session regressions all pass; `verify.py --mode positive-and-two-mutations` produced accepted artifact `verify-72645.json`.
- C79 PR #470 remains open, mergeable and 9/9 green with no review or guarded merge. The remaining Wire registration and any released-main baseline reconciliation stay deferred until C79 explicitly releases its shared ownership. This checkpoint is not yet pushed or submitted to Script CI.

## Handoff

This head is based on accepted main 3963bc3566da24f8214634c17a9d0f79a6724171 and preserves startup ancestry. scripts/wire-packages.txt and docs/architecture/architecture-size-baseline.json remain untouched because C79 still holds their active lease; C119-05 must integrate current origin/main only after C79's reviewed guarded merge and explicit release. The executor has not polled or claimed Script CI green.

## Final post-C79-release executor checkpoint

- C79 PR #470 was guarded-merged at `1a8467246c6607a06ffc7289075da2595724ce8b`. After a fresh fetch, `origin/main` is that revision; it was integrated into this isolated branch with a no-fast-forward merge commit `d7a5e7322e0b06cb734fd23c0f94908027806ad`. Startup `8bdafc7f947a3a2c9856220abdc539437035bd21`, accepted main `3963bc3566da24f8214634c17a9d0f79a6724171`, and current main ancestry all verify.
- The C119 static repair remains commit `20fd1e544`: only the redundant 97-line `session_failure_projection_test.go` was removed, bringing `agentruntime` to exactly 237 Go files. The released sharded architecture baseline remains unchanged; no limit was raised and no coverage floor was weakened.
- After C79 release, commit `6fae9457331bcbec95bbc6a06323ab9b5d64f8b0` adds only the demonstrated `sessionfailure` generated-Wire rule to `docs/architecture/architecture-policy.json` and the matching `scripts/wire-packages.txt` entry. The verifier now scopes candidate mutations against current `origin/main`, validates that exact shared diff, and still proves the immutable 246-line accepted-main source and startup admission.
- Final bounded gates are green: `make fmt`, `make vet`, pinned golangci-lint `2.9.0`, pinned staticcheck `2026.1`, coverage registration (182 packages), coverage/floor (182 registered packages across 7 profiles), Wire (10 graphs), architecture/size (192 packages, 1,911 files, 28,218 functions), and `git diff --check`.
- `verify.py --mode final` is accepted in `verify-29328.json`: service normal/race checks, isolated `GOWORK=off` consumer, unchanged caller regressions, and both causal mutants have the required results. The accumulated session regression script passed normal, coverage, and race modes at `COUNT=1`.
- Fresh source-pinned replay `run-1789288132-31227.json` is accepted from source `6fae9457331bcbec95bbc6a06323ab9b5d64f8b0` with yui SHA256 `87740daba99c4f8b0f42c6b7820e7a3f1ae153b8380df0344c635fbfad62ea5c`; provider error, synthesized provider close, cancellation-negative, and healthy audio/tool continuation all passed their bounded observable oracles with zero Realtime sessions and no surviving process groups.
- This executor has not polled or claimed Script CI, independent review, guarded merge, or vertical acceptance. The next action is push this same candidate to PR #505 and return `ACCEPTED` for the script CI gate.
