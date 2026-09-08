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
  checkpoint was `b01dbb573a15eb61d1cacfff11d37c2461167987`; before this
  delivery, `origin/main` advanced to accepted C12 merge
  `668f2d8816beaa078d058b3f0bcc59600b71a023`.
- `git merge-base --is-ancestor` passed for startup integration
  `8bdafc7f947a3a2c9856220abdc539437035bd21`, baseline
  `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`, and fetched `origin/main`.
- Accepted main was merged in this isolated worktree at
  `HEAD=39d177009bfca1ada9b01d5a64a071fce9d69f44`; the running host checkout
  was not merged or reset. The accepted C12 main remains an ancestor of the
  current profiler repair checkpoint
  `f2a5be30e93a5d436d123ed828e75e91e4e88e30`.
- Initial status was clean. Only `scripts/hermetic-profile/` and this matching
  evidence directory are in C11 scope; predecessor checkpoints, C08's active
  owner/worktree/PR400, the parent checkout, and factory configuration were
  preserved.

## Board and ownership evidence

Complete initial canonical responses are retained in `canonical-board.json` and
`worker-sessions.json`; the immediate before/after snapshots are retained as
`pre-measurement-*` and `post-measurement-*`. The final canonical board response
used for this handoff is retained in `canonical-board-f2a5be3.json` (SHA-256
`330d72d6d5d98f65f31f20d91a47d238fe0c94cfd081e0da14a6f10eb01d95c6`). The prior
canonical rejection is recorded in `ci-rejection.json`; the latest current-head
Windows checkout rejection is recorded in `ci-rejection-windows.json`; and the
subsequent hermetic rejection is recorded in `ci-rejection-hermetic.json`.
Neither is claimed green or attributed to the profiler's Go lane.

## Implementation and causal verification

- `scripts/hermetic-profile/profile.py` provides `inventory`, `warm`, `run`,
  and offline `analyze`; all subprocesses are argv-based, bounded, grouped,
  and retain stdout/stderr plus hashes and monotonic intervals.
- `controls.py` invokes the public entry point with synthetic JSONL fixtures and
  covers pass/repeat, low-duration fail, nonzero apparent pass, truncated,
  malformed, empty, missing package, cached, no-test, overlapping subtests,
  cross-package concurrency, help/offline no-spawn, invalid shared-host
  evidence, redirected artifact paths, forged captured source identity,
  missing per-record source identity, missing quiet evidence, incomplete
  repetition counts, missing quiet runner or load observations, expired quiet
  evidence, required run timing, weak quiet observations, manifest-root cache
  containment, no-test inventory conflicts, malformed run records with stale
  analysis replacement, and absolute/traversal redirects for stdout, stderr,
  and quiet-evidence artifacts.
- AST parsing of all shipped Python files: PASS.
- `profile.py --help`: PASS.
- `ctrl-0564fb5/controls.json`: PASS; 36 declared cases and 22 result groups,
  zero Go/network/build invocations; hash
  `ce3417ad45b77357ef0cee93679a6499d2290a19a0b6aa02acd945855227fee3`.
  Top-level `raw_evidence_retained=false` and the
  `missing-raw-artifact` case reports `raw_artifacts_retained=false`, because
  that fixture deletion is intentional negative-control evidence.
- Exact-head repair source hashes at `f2a5be30e93a5d436d123ed828e75e91e4e88e30`:
  `profile.py`
  `ea156465ffe23697e185c7e721c7fcc445fb23b30d85613daf3a3ac031e0d3a6`,
  `controls.py`
  `49cf2365e4bb9902d9dd06e23db0e6ac1f9754039ebae7dff319c21b28b55e79`, and
  `fixtures/emit_jsonl.py`
  `d4e4e8a43ace6ed89615362583649f71999096d9ca256cfc8a8a868f65045249`.
- `ctrl-f2a5be3/controls.json`: PASS; 42 declared cases and 22 result groups,
  zero Go/network/build invocations; hash
  `602f156dd848d9ec0bd0f2113ddf25a6c7902f317209b1741dbd77498d982b0f`.
  Top-level raw_evidence_retained=false and the missing-raw-artifact case
  report false because that fixture deletion is intentional negative-control
  evidence. The source-identity control includes three forged-capture
  provenance mutations plus the post-inventory dirty-source rejection.
- The exact-head controls cover legal repeated package terminals, Go
  `time.Duration` overflow and huge integer handling, forged captured
  head/repository/dirty-path provenance, malformed record schema, stale
  analysis replacement, and rejection of a post-inventory dirty source before
  the public fixture runs. The prior review controls for overlap, artifact
  containment, quiet provenance, repetition completeness, and raw-artifact
  retention pass again in the same report.
- `GOWORK=off go test . -count=1` in `tools/timingate`: PASS.
- The previous CI failure was read from job `102043028609` in run
  `34220749840`: `TestRunRoom_ReportsClosedTargetAsRejectedPeerIngress` failed
  in `agent-cli/internal/services/internal/agentruntime` with a closed PCM16
  mixer result. After rebasing onto current main, the exact test passed once;
  no C11-owned repair was identified. The same PR remains the delivery target.
- The latest current-head CI rejection was read in full from job `102078631796`
  in run `34231535549` at submitted head `0f8529a3`. The hermetic job reached Go and
  failed the existing
  `TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio/test46/provider_burst`
  case in `agent-cli/test/integration/session_tool_audio_remote_e2e_test.go`;
  its final PCM marker was not observed before the scenario deadline. The
  failing path is outside C11's lease and is owned by active C12 runtime work;
  this is not a C11 repair or a green-CI claim. The earlier Windows checkout
  failure remains recorded and was causally repaired by `ca12c8a`.
- `git diff --check`: PASS.
- Fresh exact-head recheck from `f2a5be3` passed: 42 public cases and 22
  result groups with zero Go/network/build invocations (tracked report SHA-256
  `602f156dd848d9ec0bd0f2113ddf25a6c7902f317209b1741dbd77498d982b0f`),
  profiler help, AST parsing of all three Python files, and
  `GOWORK=off go test . -count=1` in `tools/timingate`.
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

The current source/control evidence is pinned to `f2a5be3`; the accepted
`origin/main` ancestor remains `668f2d8816beaa078d058b3f0bcc59600b71a023`.
The latest canonical task rejection named forged captured source provenance,
malformed selected-package records, oversized monotonic timestamps, and stale
analysis output. Commit `f2a5be3` repairs those causes with causal public
controls while retaining the earlier review repairs for lane timing, quiet
observations, cache containment, no-test markers, repeated package terminals,
and artifact provenance. Offline analysis now retains package ranking and
lane-wall evidence while referencing the canonical 60-second `tools/timingate`
policy instead of duplicating it. Quiet evidence, all raw command artifacts,
and both Go cache paths are constrained to the manifest output root before use.
The next bounded step is to push/update PR #403 with the exact final head and
return `ACCEPTED` to script CI. Do not poll CI, self-review, claim CI green, or
close any of the nine immutable project gates.
