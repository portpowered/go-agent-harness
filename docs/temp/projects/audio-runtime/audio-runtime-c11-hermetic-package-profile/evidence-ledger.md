# audio-runtime-c11-hermetic-package-profile evidence ledger

Task `audio-runtime-c11-hermetic-package-profile`; admitted project
`audio-runtime`; factory session `~default`; server
`http://127.0.0.1:7439`.

## Latest canonical continuation

The current exact-head implementation checkpoint is
`ad73e05bd51d9e777be934c5ca4a82aec25b0068`, rebased in this isolated worktree
onto `origin/main` `c3bb663e118de9e73ea3eb211b381e8f86c4f480`. The startup
integration ancestor `8bdafc7f947a3a2c9856220abdc539437035bd21` and baseline
ancestor `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad` remain ancestors. The
remote predecessor head `e367da898437464ffa707d2cfa58626d10f0fe18` was
preserved by an explicit lease-guarded branch update after the required rebase;
the running host checkout was not merged or reset.

This repair makes `manifest.commands` the sole command-record authority across
metadata, inventory, warm, full, and cohort phases. ID arrays reference that
stream, repetition groups are derived, every phase is schema/timing/artifact
validated before filtering, Git/test argv/cwd/environment identity is bound,
and hermetic analysis requires PASS warm-up followed by one full trial and one
two-repeat cohort schedule. Legacy duplicate arrays and nested command copies
are rejected.

The exact board response is retained in
`canonical-board-recheck-20260908.json` (SHA-256
`2e59896a24498f27bb22c6eca9a9cea581b0c16087d6da2ea2dd3d2a6bc2c5e6`). The
current public control report is
`ctrl-c11-repair/controls.json` (SHA-256
`771b2db3678e4d3956b74616add9cb00d7e1910675d675433afed5a10675e57c`): PASS,
63 cases, 26 result groups, zero Go/network/build invocations. It was generated
from the exact implementation SHA above; evidence is packaged in a subsequent
commit so the record does not make a self-referential future-commit claim. Raw retention is
truthfully false only because the missing-raw-artifact negative control removes
its own stdout fixture. `profile.py --help`, Python AST parsing,
`GOWORK=off go test . -count=1` in `tools/timingate`, and `git diff --check`
also pass. Fresh timing remains BLOCKED because no isolated/dedicated runner is
available; no broad suite was started on the shared host and no CI result is
claimed.

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
  checkpoint was `b01dbb573a15eb61d1cacfff11d37c2461167987`; the current
  fetched `origin/main` is `c3bb663e118de9e73ea3eb211b381e8f86c4f480`.
- `git merge-base --is-ancestor` passed for startup integration
  `8bdafc7f947a3a2c9856220abdc539437035bd21`, baseline
  `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`, and fetched `origin/main`.
- The isolated worktree was rebased onto the fetched `origin/main`; the running
  host checkout was not merged or reset. The current implementation checkpoint
  `ad73e05bd51d9e777be934c5ca4a82aec25b0068` has that main as an ancestor.
- Initial status was clean. Only `scripts/hermetic-profile/` and this matching
  evidence directory are in C11 scope; predecessor checkpoints, the C08
  predecessor worktree/PR400, the parent checkout, and factory configuration
  were preserved. No historical C08 lease is used as current ownership
  evidence.

## Board and ownership evidence

Complete initial canonical responses are retained in `canonical-board.json` and
`worker-sessions.json`; the immediate before/after snapshots are retained as
`pre-measurement-*` and `post-measurement-*`. The prior canonical board response
is retained in `canonical-board-f2a5be3.json` (SHA-256
`330d72d6d5d98f65f31f20d91a47d238fe0c94cfd081e0da14a6f10eb01d95c6`). The prior
canonical rejection is recorded in `ci-rejection.json`; the latest current-head
Windows checkout rejection is recorded in `ci-rejection-windows.json`; and the
subsequent hermetic rejection is recorded in `ci-rejection-hermetic.json`.
Neither is claimed green or attributed to the profiler's Go lane.
The prior canonical board response is retained in
`canonical-board-bc7609b.json` (SHA-256
`d4471d07631548e19dd6d9e186742217c8a9b19ad713c6fc39eaf8afd51e213c`). It records
the earlier task/review state and rejected C11 head. The prior
hermetic rejection metadata and full log are retained in
`ci-rejection-current.json` (SHA-256
`7c04cd1014413642b1f4ef28298cfdbc73afa7d33176783e8c98803f765a42a5`) and
`ci-rejection-current.log` (SHA-256
`75599affc7fdada1598ea95c9f419d82c182167b61d3ec46aeb3dde028d63f42`).
The final pre-repair board response used for this checkpoint is also retained
in `canonical-board-c11-final.json` (SHA-256
`64ad2f9c0b2d4a095e312274154554162d1522d06c693f57bb21179f3706e10c`).
The exact CI coverage rejection inspected before this repair is retained in
`ci-rejection-34277521278.json` (SHA-256
`34a8a8e7f839811418bc8caf4f75a20772ca1e360450dedb3bbb5145bfa169ab`) and
`ci-rejection-34277521278.log` (SHA-256
`9d0a885aaa8fc40ccbd266fef9fbf1bf78ada462cd38821f9f1f3eb162116aa9`).

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
- Prior submitted-head report `ctrl-bc7609b/controls.json`: PASS; 42 declared
  cases and 22 result groups, zero Go/network/build invocations; hash
  `b3a96ae9f05e973e7692e1a257476de0a795703fe891828875879949f71c70a2`.
  It is retained as historical evidence for rejected head `bc7609b5`.
- Historical repair-head report `ctrl-59f370b/controls.json`: PASS; 47 declared
  cases and 23 result groups, zero Go/network/build invocations; hash
  `af631fa173222da2ae78ec67047ee9c1fa06a95e73da28260211e3283912853c`.
  It was generated from implementation checkpoint
  `59f370b6239744a2bf6d7234097a10cbd9f364fe` and preserves the intentional
  missing-raw-artifact negative-control truth.
- Prior repair-head report `ctrl-c11-final/controls.json`: PASS; 48 declared
  cases and 23 result groups, zero Go/network/build invocations; hash
  `626785c4f464a46c207e5d8a8998bb5f64a53d4550d58df0d02f27061ae2ea95`.
  It was generated from implementation checkpoint
  `9b3f4ebc315fe8eb26af75148fd16629f2d5c58b`; the new controls cover both
  unmatched group selection and invalid unselected groups while retaining the
  intentional missing-raw-artifact negative-control truth.
- Current exact implementation report `ctrl-c11-repair/controls.json`: PASS; 63
  declared cases and 26 result groups, zero Go/network/build invocations; hash
  `771b2db3678e4d3956b74616add9cb00d7e1910675d675433afed5a10675e57c`.
  It was generated from implementation checkpoint
  `ad73e05bd51d9e777be934c5ca4a82aec25b0068`; the lifecycle regressions reject
  cohort repeat 3, failed full trials, partial full module coverage, and
  non-PASS inventory/warm phase commands before Go work can start.
- The review-27 regressions in that report reject unreferenced invalid and
  zero-request groups, incomplete command-record-v1 fields and artifacts,
  forged retained source-validation output/identity, and package-duration sums
  beyond Go `time.Duration`.
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
  failing path is outside C11's lease and belonged to the C12 runtime work that
  is now merged;
  this is not a C11 repair or a green-CI claim. The earlier Windows checkout
  failure remains recorded and was causally repaired by `ca12c8a`.
- The latest inspected current-delivery CI rejection was read in full from job
  `102234088178` in run `34277521278` at submitted head
  `43519903572c4047bbb0068d9ed73e7027a257d7`. The coverage job failed the
  existing `TestSessionCLI_DuplexPCMMultiTurnRejectsLaterTurnAudioControl`
  because its positive harness-A baseline exceeded the two-second response
  deadline with `context deadline exceeded`. Eight required jobs passed. The
  exact metadata and 367-line log are retained above. This path is outside
  C11's owned directories; no runtime edit is authorized, and this does not
  claim hosted CI green for the new checkpoint.
- `git diff --check`: PASS.
- Fresh exact-head recheck from implementation head `ad73e05b` passed: 63 public
  cases and 26 result groups with zero Go/network/build invocations (tracked
  report SHA-256
  `771b2db3678e4d3956b74616add9cb00d7e1910675d675433afed5a10675e57c`),
  profiler help, AST parsing of all three Python files, and
  `GOWORK=off go test . -count=1` in `tools/timingate`.
- No broad hermetic/coverage suite was launched on the shared host. No
  isolated/dedicated quiet runner lease was available, and broad current-head
  checks belong to the script CI gate under the handoff.

## Fresh timing disposition

The before/after load, process, board, and worker snapshots are recorded in
`quiet-evidence-blocked.json`. Runner metadata observed without starting Go
tests: Darwin/arm64, Apple M1 Max, Go 1.26.7. The factory board and worker
activity showed no admissible isolated/dedicated quiet runner at both
snapshots, so fresh inventory/warm/run is `BLOCKED`; elapsed time was not used
as quiet evidence. `blocked-manifest.json` and `blocked-analysis/analysis.json`
are the offline machine-readable fallback.

Immutable hosted references and their hashes, provenance, historical package
costs, limitations, and at-most-three future optimization proposals are in
`assessment.md`; the compact machine-readable summary is `assessment.json`.
No fresh C11 run hash or same-source three-trial cohort exists; none is
fabricated. The review repair was validated only through the bounded public
synthetic controls and the focused timingate package test.

## Handoff

The implementation source/control checkpoint remains pinned to
`ad73e05bd51d9e777be934c5ca4a82aec25b0068`; the current `origin/main`
ancestor is `c3bb663e118de9e73ea3eb211b381e8f86c4f480`. The latest review-35
task rejection named stale exact-head evidence, cohort repeat overrun,
failed/partial full-trial admission, non-PASS inventory/warm phase commands,
unreferenced invalid groups, forged retained Git metadata, aggregate duration
overflow, and incomplete command records. This repair closes those causes with
causal public controls while retaining the earlier review repairs for lane timing,
quiet observations, cache containment, no-test markers, repeated package
terminals, and artifact provenance. Offline analysis now retains package
ranking and lane-wall evidence while referencing the canonical 60-second
`tools/timingate` policy instead of duplicating it. Quiet evidence, all raw
command artifacts, and both Go cache paths are constrained to the manifest
output root before use. The current canonical board snapshot, exact rejected
coverage log, schema/control mapping, and final control report are retained
above. The next bounded step is to push this checkpoint, update PR #403 with
the exact head and evidence, and return `ACCEPTED` to script CI. Do not poll CI, self-review,
claim CI green, or close any of the nine immutable project gates.
