# C164 capture-claim policy handoff checkpoint

## Identity and admission

- Factory session: `~default`.
- Project: `audio-runtime` / contract `audio-runtime-v1`.
- Work: `audio-runtime-c164-recover-c160-capture-claim-policy-handoff`.
- Admission command from `FACTORY_ROOT`:
  `python3 factory/scripts/project-control.py verify-work --type task --name audio-runtime-c164-recover-c160-capture-claim-policy-handoff --root "$FACTORY_ROOT"`
- Admission result: `{"status": "admitted", "project": "audio-runtime", "name": "audio-runtime-c164-recover-c160-capture-claim-policy-handoff"}`.
- Isolated worktree: `/Users/abdifamily/.codex/worktrees/af44/go-agent-harness/.claude/worktrees/audio-runtime-c164-recover-c160-capture-claim-policy-handoff`.
- PRD branch and current branch both: `codex/audio-runtime-c164-recover-c160-capture-claim-policy-handoff`.
- No second project, acceptance waiver, host-checkout merge, reset, rebase, squash, or force-push was used.

## Preserved recovery and ancestry

- C160 terminal work-task: `work-task-64`, transcript/attempt `22dede0b-869b-4673-88ee-a76cb17defc4d`.
- Adopted clean C160 checkpoint: `4a2048c7c4e2a8a22b01bd8ad09549fc47fdfa2f`.
- C160 adoption merge in this isolated C164 branch: `29f37135329adc00299ab71993caa17d9695528c`.
- Fresh `origin/main` was fetched after it advanced during validation to `2c79ec6a931c3e86944d6a625d0a5060b4f85aa0`.
- Normal current-main ancestry merge: `ac994395c0ea645b71a12c34ec5d97c0db3af78d`.
- The C160 checkpoint, startup integration revision `8bdafc7f947a3a2c9856220abdc539437035bd21`, planning origin `97d3dcfb1e97a2611aa26b203a7f893442db4768`, and fresh `origin/main` are ancestors of the candidate.
- The C160 worktree remained clean and unchanged; the running host checkout was not modified.

## Recovered rejection and exact repair

Previous PR 514 head was `16f073183e3b61ccd8c401af4fdd6eb2e8440440`. Run `34772244215`, static job `103763829025`, was read from the complete job log. Eight lanes passed; static failed only these four architecture findings:

1. stale cognitive-complexity baseline for `acquireSessionRecordingClaim`;
2. stale cyclomatic-complexity baseline for `acquireSessionRecordingClaim`;
3. stale function-lines baseline for `acquireSessionRecordingClaim`;
4. generated-file-spoof for `go-agent-runtime/services/captureclaim/wire/wire_gen.go` because its Wire header was not registered.

The failed C160 handoff identified the same blocker and the required scope correction. C164 applied only the five authorized entry-level repairs:

- one `services/captureclaim/wire/wire_gen.go` generated-file record in `docs/architecture/architecture-policy.json`;
- one `go-agent-runtime/services/captureclaim/wire` line in `scripts/wire-packages.txt`;
- deletion of only the cognitive-complexity, cyclomatic-complexity, and function-lines entries for `acquireSessionRecordingClaim` in its per-file baseline.

No generated source, architecture thresholds, peer baseline entry, or remaining typed-error baseline entry was changed. The preserved adapter is 77 lines, retiring 240 lines from the accepted 317-line legacy baseline.

Implementation checkpoint before this evidence commit: `5d7fcb0fc7df4f2f4394f71fe3151fac0db1c666`.

## Candidate gate evidence

All commands below passed on the current-main merged candidate unless a preserved predecessor checkpoint is explicitly named.

- Preserved C91 `verify.py --mode all`, invoked from the adopted C160 worktree as required by the PRD: passed at C160 head `4a2048c7c`.
- `make wire-check`: passed; all registered Wire injectors regenerated without a worktree diff.
- `make architecture-size-check`: passed, 205 packages / 1,952 files / 28,905 functions.
- `go test ./go-agent-runtime/services/captureclaim/... -count=3`: 105 passed in 3 packages.
- `go test -race ./go-agent-runtime/services/captureclaim/... -count=3`: 105 passed in 3 packages.
- Focused CLI adapter normal tests, count 3: 99 passed.
- Focused CLI adapter race tests, count 1: 26 passed.
- `GOWORK=off go test ./...` in the external public-consumer module: passed.
- `COUNT=1 bash scripts/test-session-ci-regressions.sh all`: passed in normal, coverage, and race modes, including high-rate tool-audio, integration lifecycle/negative controls, device, and provider lanes.
- `make fmt`: passed with no changes.
- `make vet`: passed across all modules.
- pinned Staticcheck 2026.1: passed across all modules.
- pinned golangci-lint 2.9.0: passed with zero issues across all modules.
- `make coverage-registration`: passed, 195 workspace packages across 6 modules.
- `make coverage-changed COVERAGE_BASE=97d3dcfb1e97a2611aa26b203a7f893442db4768`: passed.
- `git diff --check`: passed; final worktree clean.

## Handoff boundary

The existing PR is 514 on remote branch `codex/audio-runtime-c139-recover-c91-capture-claim-runtime`. The candidate is ready to push to that same branch. Local gates do not claim script CI green, independent review, guarded merge, immutable post-merge YUI vertical evidence, or project acceptance. Exact next action: push the final candidate head to PR 514 and submit that exact head once to the canonical SCRIPT CI gate; SCRIPT owns CI polling and rejection feedback.
