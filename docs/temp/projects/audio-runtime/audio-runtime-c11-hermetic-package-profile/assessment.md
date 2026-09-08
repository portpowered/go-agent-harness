# C11 hermetic package assessment

## Decision

`fresh_timing=BLOCKED`. No fresh Go inventory, warm build, or hermetic test
timing was launched on the shared factory host. At the resumed checkpoint there
was no isolated or dedicated runner evidence; the current board/worker
snapshot establishes that the shared host is not an admissible quiet runner.
exact machine-readable blocker is
`quiet-evidence-blocked.json`, with before/after board, worker, process, and
load artifacts beside this report.

This is the permitted immutable-log fallback. It is not a performance PASS,
not a waiver, and not a claim that the under-three-minute target is met.

## Source and admission provenance

- Project: admitted `audio-runtime`; work: `audio-runtime-c11-hermetic-package-profile`.
- Session: `~default`; server: `http://127.0.0.1:7439`.
- Admission command: `python3 "$FACTORY_ROOT/factory/scripts/project-control.py" verify-work --type task --name audio-runtime-c11-hermetic-package-profile`.
- Admission result: `{"status":"admitted","project":"audio-runtime","name":"audio-runtime-c11-hermetic-package-profile"}`.
- `prd.json.branchName` and the isolated branch are
  `codex/audio-runtime-c11-hermetic-package-profile`.
- Fetched and integrated `origin/main`:
  `668f2d8816beaa078d058b3f0bcc59600b71a023`.
- Current delivery candidate checkpoint:
  `bc7609b5fcad50f65eff89f0ce91f3f08edabeb3`.
- The implementation repair is committed at
  `f2a5be30e93a5d436d123ed828e75e91e4e88e30`; the current delivery checkpoint
  adds only owned evidence/provenance updates and preserves the same source
  files and repair behavior.
- Payload main observation: `7a3a5e8a93f05c2d2818b55aef7c4cf528e5cd8d`.
- Preserved startup integration ancestor:
  `8bdafc7f947a3a2c9856220abdc539437035bd21`.
- Preserved baseline ancestor:
  `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`.
- `progress.txt` remains the inherited historical C07 checkpoint and was not
  rewritten. C08 paths, the parent checkout, and factory configuration remain
  outside this candidate.

The complete canonical board and worker-session responses are retained in
`canonical-board.json`, `canonical-board-current.json`,
`canonical-board-0564fb5.json`, `canonical-board-f2a5be3.json`,
`canonical-board-be9f11a5.json`,
`canonical-findings-be9f11a5.json`, and
`worker-sessions.json`; the immediate measurement snapshots are
`pre-measurement-*` and `post-measurement-*`. The current board rejection and
its exact failed job metadata are captured in `ci-rejection.json`; the latest
current-head Windows rejection is captured in `ci-rejection-windows.json`, the
prior pre-C12 hermetic rejection is captured in `ci-rejection-hermetic.json`,
and the latest current-head hermetic metadata/full log are captured in
`ci-rejection-current.json` and `ci-rejection-current.log`. The latest
rejected head was `bc7609b5`; the hosted hermetic job reported the existing
multi-turn duplex schedule and missing-commit positive-baseline failures.
This is a recheck and handoff record, not a claim that script CI is green.
The exact current board response, including current task/review states and
leases, is preserved in `canonical-board-bc7609b.json`; historical C08/C12
worker IDs are not used as current ownership evidence. C11 does not claim
runtime ownership outside `scripts/hermetic-profile/` and this evidence root.

## Implemented tooling and focused evidence

The public surface is `scripts/hermetic-profile/profile.py` with
`inventory`, `warm`, `run`, and offline `analyze`, plus `controls.py` and the
JSONL fixture. It captures effective commands, source/run identity, raw streams,
hashes, process status, monotonic intervals, package inventory, explicit caches,
runner metadata, package timing, repeat variation, no-test/skip classification,
subtest overlap, and assessment-specific wall/ranking evidence. The canonical
`tools/timingate` package-budget evaluator is referenced rather than duplicated.
Heavy commands fail closed without explicit opt-in and valid isolated/dedicated
evidence.

Source file hashes at the current repair checkpoint are:
`profile.py` `ea156465ffe23697e185c7e721c7fcc445fb23b30d85613daf3a3ac031e0d3a6`,
`controls.py` `49cf2365e4bb9902d9dd06e23db0e6ac1f9754039ebae7dff319c21b28b55e79`,
and `fixtures/emit_jsonl.py`
`d4e4e8a43ace6ed89615362583649f71999096d9ca256cfc8a8a868f65045249`.

Focused evidence:

- Python AST parsing of all shipped profiler/fixture scripts: PASS.
- `python3 scripts/hermetic-profile/profile.py --help`: PASS.
- `python3 scripts/hermetic-profile/controls.py --output .../ctrl-bc7609b`:
  PASS; 42 declared cases and 22 result groups, zero Go, network, or build
  invocations. The report hash is
  `b3a96ae9f05e973e7692e1a257476de0a795703fe891828875879949f71c70a2`.
  The report truthfully marks top-level raw evidence retention `false` because
  the `missing-raw-artifact` case deliberately deletes its stdout fixture as a
  negative control; the per-case result also records `false`. The source
  identity control includes three forged-capture provenance mutations and the
  post-inventory dirty-source rejection.
- Review repair controls additionally prove that cross-package test activity is
  not subtest overlap, `--allow-heavy` reaches invalid shared-host validation,
  absolute and traversal stdout/stderr/quiet-evidence redirects are rejected, and
  tampered per-record source identity including captured head/repository/dirty
  paths, quiet-evidence provenance, requested versus completed repetition
  counts, missing runner/load observations, expired quiet evidence, repeated
  package terminals, oversized `Elapsed` values, oversized integer handling,
  stale-analysis replacement, and post-inventory source dirtiness remain
  fail-closed.
- `GOWORK=off go test . -count=1` from `tools/timingate`: PASS.
- Rejected CI regression recheck:
  `go test ./agent-cli/internal/services/internal/agentruntime -count=1
  -run '^TestRunRoom_ReportsClosedTargetAsRejectedPeerIngress$' -timeout 30s`:
  PASS after rebase.
- `git diff --check`: PASS.
- Fresh exact-head recheck from `bc7609b5` passed: 42 public cases/22
  result groups with zero Go/network/build invocations (tracked report
  SHA-256 `b3a96ae9f05e973e7692e1a257476de0a795703fe891828875879949f71c70a2`),
  `profile.py --help`, AST parsing of all three Python files, and
  `GOWORK=off go test . -count=1` in `tools/timingate`.

The latest prior current-head CI rejection was inspected in full from run
`34231535549`, job `102078631796` (`CI (hermetic)`) at pre-C12 submitted head
`0f8529a3`. Eight
required jobs passed, but the existing `agent-cli/test/integration` suite
failed `TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio/test46/provider_burst`:
`session_tool_audio_remote_e2e_test.go:182` reported that remote playback did
not reach its final PCM marker before the scenario deadline. This path is
outside C11's owned directories and was owned by the C12 runtime/integration
task. C12 is now merged at `668f2d88`; that historical failure is not treated
as proof of a new C11 result. The new C11 repair is not claimed CI-green and is
ready for the script-owned gate. The earlier Windows checkout failure remains
recorded in `ci-rejection-windows.json` and was causally repaired by `ca12c8a`.

The latest current-head rejection is run `34270089010`, job `102209193353`,
at submitted head `bc7609b5`. The full 376-line hosted log and job metadata are
retained in `ci-rejection-current.log` and `ci-rejection-current.json`. The
hermetic job failed the existing `TestSessionCLI_DuplexPCMMultiTurnSchedule`
positive harness and the
`TestSessionCLI_DuplexPCMMultiTurnRejectsLaterTurnCommitControls/missing_commit`
case before its negative mutation. The log also contains expected negative
control diagnostics; they are not additional failures. Both named test paths
are outside C11's two owned directories. Bounded exact local rechecks on this
branch passed the tool-barge oracle control and both multi-turn tests; this does
not convert the hosted failure to green or establish a runtime repair. The new
evidence checkpoint is submitted again to script-owned CI without a waiver or
C11 runtime edit.

The controls deliberately prove that a low-duration package failure, a nonzero
process with passing-looking JSON, truncated/malformed/empty streams, missing
package terminals, cached output, missing raw artifacts, redirected artifacts,
missing provenance, forged captured source validation, incomplete repeats, weak
quiet observations, missing lane timing, out-of-root caches, no-test inventory
conflicts, malformed run records, stale-analysis replacement, and quiet-evidence
paths outside the manifest root do not become a fresh timing PASS. The offline
report retains
package ranking and lane-wall evidence while leaving the canonical 60-second
policy to `tools/timingate`; it does not duplicate that evaluator.
The repeated synthetic cohort also proves lane wall is not the sum of package
durations. The broad six-module hermetic/coverage suites were not duplicated
locally because no isolated/dedicated quiet runner was available and the handoff
assigns those broad checks to the script CI gate.

## Immutable hosted evidence

These files are references, not fresh C11 measurements and not current-source
proof:

| artifact | SHA-256 | source/runner/provenance | useful observations |
| --- | --- | --- | --- |
| `$FACTORY_ROOT/docs/temp/projects/audio-runtime/ci-34132278337-failed.log` | `8715136ba2c199229e01771abd686e3ad869297803ba906dd72a2baa18516593` | PR398 merge `c849c9a1fe9ae399290bad389472d60e1d26b2eb`, merging `8502cbd2ab233566bb7610019daca37f25f162ab` into `ddebb8f44346cde4c7e989a1880b8ffac6179df0`; Go 1.26.7 linux/amd64; hosted caches `/home/runner/go/pkg/mod` and `/home/runner/.cache/go-build` | coverage `agent-cli` package observations include `internal/services/internal/agentruntime` 67.119s, `internal/transport/cli` 15.915s, `internal/transport/cli/internal/events` 10.058s, `internal/testtimeout` 9.082s; integration failed at 276.685s after replay mismatch; wrapper elapsed 5m36.631s. |
| `$FACTORY_ROOT/docs/temp/projects/audio-runtime/ci-34154917253-failed.log` | `268ace28829689cdb4c100960be39714ccb3a51c97c3d5a0944e7c23e601e091` | saved hosted workflow log; this file does not carry a complete checkout SHA or runner/cache identity, so those fields remain unknown | hermetic `agent-cli` used `CGO_ENABLED=0`, `tags=nomicrophone`, and target-wide 480s; `internal/services/internal/agentruntime` 61.608s, `internal/transport/cli` 14.893s, `internal/transport/cli/internal/events` 10.032s, `internal/testtimeout` 8.954s, `internal/webmcp/chrome` 4.172s; integration failed at 241.780s with replay mismatch and `TestSessionCommand_LiveScheduledAudioDoesNotCrossDelayedSessionUpdated`. |

The logs do not contain a complete uncached C11 `go test -json` stream or three
same-source cohort trials. Their package values are retained as historical
diagnostics only; no timingate PASS, lane-wall PASS, or measured savings is
derived from them. The known runtime/replay failures remain outside C11's
lease; C11 does not alter those paths or claim to repair broad intermittency
from a focused local pass.

## Ranked observations and future work

The historical ranking is dominated by the agent-cli integration package and
the internal agent-runtime service package. The two hosted runs are not a
controlled repeat: sources, cache state, wrapper mode, and lane contention are
not equivalent. Therefore the following savings are estimates, not measured
results:

1. `agent-cli/test/integration/` -> a future test-only fixture composition
   boundary under `agent-cli/test/integration/internal/fixtureprofile/`, keeping
   replay/session assertions in the owning integration package. Owner: C08 plus
   the CLI integration maintainer. Dependency: first repair the exact replay
   mismatch and obtain a quiet same-source baseline. Estimate: reduce repeated
   fixture setup on the slow integration cohort; savings and confidence are
   unmeasured.
2. `agent-cli/internal/services/internal/agentruntime/` -> a future focused
   package-level setup seam under
   `agent-cli/internal/services/internal/agentruntime/testsupport/`, with
   production imports unchanged and test-only helpers depending inward. Owner:
   agent-runtime/CLI maintainers. Dependency: compare warm compile and package
   execution on the same runner after C08 is quiet. Estimate: isolate service
   fixture/build overhead; savings and confidence are unmeasured.
3. `agent-cli/internal/transport/cli/internal/events/` and
   `agent-cli/internal/transport/cli/` -> a future shared event-fixture builder
   under `agent-cli/internal/transport/cli/internal/eventtestkit/`, consumed by
   tests only. Owner: CLI transport maintainer. Dependency: prove event
   ordering/terminal assertions remain byte-for-byte identical and measure cold
   versus warm setup separately. Estimate: lower repeated event fixture setup;
   savings and confidence are unmeasured.

No safe optimization is ready to implement in C11: all three proposals touch
future test/runtime ownership, and the missing quiet same-source evidence makes
any claimed benefit speculative. C11 intentionally delivers the measurement
protocol and honest fallback rather than restructuring suites.

## Residual handoff

The current C11 implementation repair remains pinned to `f2a5be3`; the current
delivery/evidence head is `bc7609b5` and the tracked `ctrl-bc7609b` report is
recorded above. The canonical review findings named
forged captured source provenance, malformed selected-package records,
oversized monotonic timestamps, and stale analysis output. Commit `f2a5be3`
repairs those causes and the public controls reproduce each positive/negative
boundary, while retaining the earlier repairs for lane timing, quiet
observations, cache containment, no-test markers, repeated package terminals,
and artifact provenance. The next action is to commit/push this exact evidence
checkpoint, update PR #403, and return `ACCEPTED` to the script-owned CI gate
without polling it. Any C11-owned CI/review finding returns to this task;
independent review and post-integration vertical validation remain external
stages. No fresh timing or under-three-minute claim is made.
