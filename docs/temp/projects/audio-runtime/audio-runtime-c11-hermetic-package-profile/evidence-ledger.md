# audio-runtime-c11-hermetic-package-profile evidence ledger

Task `audio-runtime-c11-hermetic-package-profile`; admitted project
`audio-runtime`; factory session `~default`; server
`http://127.0.0.1:7439`.

## Required admission and preservation checks

- Read the immutable `prd.json`, inherited `progress.txt`, operating policy,
  implementation handoff, meta-planner handoff, C09 findings, coverage review
  finding, operator recovery status, current audio-runtime meta-status, source
  plan, Makefile hermetic/budget targets, `cmd/testtimeout`, and
  `tools/timingate`. No second project or acceptance waiver was used.
- Admission command:
  `rtk proxy python3 "$FACTORY_ROOT/factory/scripts/project-control.py" verify-work --type task --name audio-runtime-c11-hermetic-package-profile`
- Admission result:
  `{"status": "admitted", "project": "audio-runtime", "name": "audio-runtime-c11-hermetic-package-profile"}`.
- `prd.json.branchName` and the isolated branch are
  `codex/audio-runtime-c11-hermetic-package-profile`; the worktree is
  `/Users/abdifamily/.codex/worktrees/af44/go-agent-harness/.claude/worktrees/audio-runtime-c11-hermetic-package-profile`.
- `git fetch origin main` completed before implementation. The initial
  checkpoint was `b01dbb573a15eb61d1cacfff11d37c2461167987`; before delivery,
  `origin/main` advanced to `02e54e6a89a7a2d7ad1ce2fa0619145c16400339` after
  C08 merged.
- `git merge-base --is-ancestor` passed for startup integration
  `8bdafc7f947a3a2c9856220abdc539437035bd21`, baseline
  `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`, and fetched `origin/main`.
- Required delivery rebase completed onto `origin/main`; the review-repair
  implementation checkpoint is `HEAD=e83c4683396de37c8e558b35e99a08f6b539ebd7`.
- Initial status was clean. Only `scripts/hermetic-profile/` and this matching
  evidence directory are in C11 scope; predecessor checkpoints, C08's active
  owner/worktree/PR400, the parent checkout, and factory configuration were
  preserved.

## Board and ownership evidence

Complete initial canonical responses are retained in `canonical-board.json` and
`worker-sessions.json`; the immediate before/after snapshots are retained as
`pre-measurement-*` and `post-measurement-*`. The current canonical rejection
is recorded in `ci-rejection.json`; it is the previous head's `CI (hermetic)`
failure, not a profiler control failure.

## Implementation and causal verification

- `scripts/hermetic-profile/profile.py` provides `inventory`, `warm`, `run`,
  and offline `analyze`; all subprocesses are argv-based, bounded, grouped,
  and retain stdout/stderr plus hashes and monotonic intervals.
- `controls.py` invokes the public entry point with synthetic JSONL fixtures and
  covers pass/repeat, low-duration fail, nonzero apparent pass, truncated,
  malformed, empty, missing package, cached, no-test, overlapping subtests,
  cross-package concurrency, help/offline no-spawn, invalid shared-host
  evidence, redirected artifact paths, missing per-record source identity,
  missing quiet evidence, and incomplete repetition counts.
- AST parsing of all shipped Python files: PASS.
- `profile.py --help`: PASS.
- `controls-review-repair/controls.json`: PASS; 18 declared scenarios and 17
  result groups, zero Go/network/build invocations; hash
  `a070ed8ed89e36977fd9f002ea7f7babe50349cc5fbb4adaf02a0d90d9f09445`.
  Top-level `raw_evidence_retained=false` and the
  `missing-raw-artifact` case reports `raw_artifacts_retained=false`, because
  that fixture deletion is intentional negative-control evidence.
- Review repair source hashes: `profile.py`
  `ffb19849235315603db5a58136ca78a49ef9e064ca37adbb7546460d9988f7e2`,
  `controls.py`
  `7c79c772bf92cc88a9dc83170e5d1f0bd8adf1b50fe966d523309444f4bf5671`, and
  `fixtures/emit_jsonl.py`
  `8d433e20345342fb4d92405ecdfae4a2327c0047eb03ad3df0b6196468978856`.
- `GOWORK=off go test . -count=1` in `tools/timingate`: PASS.
- The previous CI failure was read from job `102043028609` in run
  `34220749840`: `TestRunRoom_ReportsClosedTargetAsRejectedPeerIngress` failed
  in `agent-cli/internal/services/internal/agentruntime` with a closed PCM16
  mixer result. After rebasing onto current main, the exact test passed once;
  no C11-owned repair was identified. The same PR remains the delivery target.
- `git diff --check`: PASS.
- No broad hermetic/coverage suite was launched on the shared host. The active
  C08 owner makes it non-quiet, and broad current-head checks belong to the
  script CI gate under the handoff.

## Fresh timing disposition

The before/after load, process, board, and worker snapshots are recorded in
`quiet-evidence-blocked.json`. Runner metadata observed without starting Go
tests: Darwin/arm64, Apple M1 Max, Go 1.26.7. C08 remained active at both
snapshots, so fresh inventory/warm/run is `BLOCKED`; elapsed time was not used as
quiet evidence. `blocked-manifest.json` and `blocked-analysis/analysis.json`
are the offline machine-readable fallback.

Immutable hosted references and their hashes, provenance, historical package
costs, limitations, and at-most-three future optimization proposals are in
`assessment.md`; the compact machine-readable summary is `assessment.json`.
No fresh C11 run hash or same-source three-trial cohort exists; none is
fabricated. The review repair was validated only through the bounded public
synthetic controls and the focused timingate package test.

## Handoff

The next bounded step is push/update the same PR #403 with this repair and
return `ACCEPTED` to script CI. Do not poll CI, self-review, claim CI green, or
close any of the nine immutable project gates.
