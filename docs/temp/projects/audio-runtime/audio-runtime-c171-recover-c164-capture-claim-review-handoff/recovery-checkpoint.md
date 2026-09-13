# C171 capture-claim review handoff checkpoint

## Identity and preserved candidate

- Factory session: `~default`.
- Project/contract: `audio-runtime` / `audio-runtime-v1`.
- C171 admission was verified with `factory/scripts/project-control.py
  verify-work --type task --name
  audio-runtime-c171-recover-c164-capture-claim-review-handoff` and returned
  `{"status":"admitted","project":"audio-runtime","name":"audio-runtime-c171-recover-c164-capture-claim-review-handoff"}`.
- The C171 isolated worktree branch is
  `codex/audio-runtime-c171-recover-c164-capture-claim-review-handoff`, matching
  `prd.json.branchName`.
- The adopted operational candidate remains the clean C164 worktree on
  `codex/audio-runtime-c164-recover-c160-capture-claim-policy-handoff`, with
  PR 514's preserved remote branch
  `codex/audio-runtime-c139-recover-c91-capture-claim-runtime`.
- Before this C171 checkpoint, the candidate was clean at
  `b7f66721986db1ddb43a109a58f510dd09453768`. A fresh fetch kept
  `origin/main=2c79ec6a931c3e86944d6a625d0a5060b4f85aa0`; the startup
  integration `8bdafc7f947a3a2c9856220abdc539437035bd21` and that main revision
  are both ancestors. No host checkout, predecessor worktree, reset, rebase,
  squash, force-push, second project, or acceptance waiver was used.

## Review and gate reconciliation

- The canonical `~default` inbox contains no prior C164 or C171 independent
  review finding. The prior C164 runner failure is recorded as
  `WORKERS_EXECUTION_FAILURE` with `type=unknown`; it is not a product or CI
  failure.
- PR 514 was open, non-draft, mergeable and clean at the preserved head. Settled
  run `34789415339` matched that SHA and passed all nine named lanes: static,
  unit, integration, coverage, race, hermetic, WebMCP Chrome, macOS audio
  release, and Windows audio portable. This settled result is not relabeled as
  green for the changed checkpoint below.
- The first current-candidate run of the inherited C91 verifier failed closed
  only because its historical scope allowlist rejected the later C139/C151/C164
  evidence and the four already-authorized C164 shared-file repairs. No runtime
  behavior failed.
- The verifier repair is fail-closed: it adds only the C160/C164/C171 evidence
  prefixes, permits the three exact captureclaim shared files, and requires the
  exact appended Wire entry, exact generated-file policy object, and deletion of
  only the three `acquireSessionRecordingClaim` baseline entries. Peer entries,
  thresholds, and the global architecture baseline remain protected.

## Focused evidence on the preserved source

- `go test ./go-agent-runtime/services/captureclaim/... -count=3 -timeout=240s`
  passed across the public, private, and Wire packages.
- `go test -race ./go-agent-runtime/services/captureclaim/... -count=3
  -timeout=360s` passed across the same packages.
- The focused CLI adapter selector
  `SessionRecordingClaim|Record.*Replay|Conflicting.*Record|AudioOut|Audio.*Tool|Clean.*Shutdown`
  passed at count 3.
- The `GOWORK=off` external consumer passed.
- `COUNT=1 bash scripts/test-session-ci-regressions.sh all` passed in normal,
  coverage, and race modes, including the expected negative replay diagnostics.
- After the verifier repair, `verify.py --mode owned-and-excluded-paths` and
  `verify.py --mode all` both passed at the preserved source head. Python
  compilation and `git diff --check` also passed.

This is executor handoff evidence only. The verifier/evidence checkpoint must be
committed and pushed to PR 514's existing remote branch, then the PR body must
be updated with the final changed SHA and exact evidence. Submit that genuinely
changed head once to the canonical SCRIPT CI gate and return `ACCEPTED` without
polling. Fresh independent review, guarded merge, and the post-merge immutable
vertical probe remain required; no CI green, review, merge, vertical, device,
acoustic, or project-completion claim is made here.
