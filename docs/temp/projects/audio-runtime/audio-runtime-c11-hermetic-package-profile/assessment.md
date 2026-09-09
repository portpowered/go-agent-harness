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
- Fetched and integrated the latest `origin/main`:
  `c3bb663e118de9e73ea3eb211b381e8f86c4f480`.
- Current delivery candidate implementation checkpoint:
  `aeef9126ae54ea925dabb260818082db8675b324`.
- The implementation and exact-head control evidence are committed against
  that candidate. The evidence report is generated from that exact candidate
  SHA and packaged separately in a later evidence commit so the record does not
  make a self-referential future-commit claim. This checkpoint consolidates metadata, inventory, warm-up,
  full-lane, and cohort command records into one `manifest.commands` array,
  validates the whole input before `--group` display filtering, binds Git/test
  command identity, enforces warm/full/cohort scheduling, and records the
  old-to-new mapping in `schema-control-mapping.md`. It also rejects
  contradictory timeout/status/exit/signal/spawn declarations before analysis.
  The repair also verifies that every hermetic full record covers the complete
  inventoried module package list, that successful warm binaries still exist
  with matching hashes/bytes, and that declared wall time matches the captured
  monotonic interval. The streaming capture repair closes OpenAI response
  bodies before publishing the terminal event and explicitly closes the CLI
  stream before Save/Flush.
- Payload main observation: `7a3a5e8a93f05c2d2818b55aef7c4cf528e5cd8d`.
- Preserved startup integration ancestor:
  `8bdafc7f947a3a2c9856220abdc539437035bd21`.
- Preserved baseline ancestor:
  `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`.
- Fresh current-head focused evidence is
  `ctrl-aeef9126/controls.json`: 67 declared cases, 27 result groups, zero
  Go/network/build invocations, SHA-256
  `52226b0accd97a785c93589ea6ec13ec0fd82ea7ff787ce15c98daf082cddef3`.
- The post-CI canonical board is
  `canonical-board-after-ci-20260909.json` (SHA-256
  `30f5576e2886465b87d863fc7d2c718ad49083913dac6252d14a800eec427d07`).
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
`ci-rejection-current.json` and `ci-rejection-current.log`. The exact latest
rejection inspected for the predecessor candidate is captured in
`ci-rejection-34277521278.json` and `ci-rejection-34277521278.log`: coverage
failed at head `43519903572c4047bbb0068d9ed73e7027a257d7` on the existing
multi-turn duplex positive baseline. The new candidate is not claimed
CI-green.
This is a recheck and handoff record, not a claim that script CI is green.
The exact raw board and findings responses read for this continuation are also
retained in `canonical-board-executor-20260909.json` (SHA-256
`3992a0a76ef15fcc5c409fad20a13710d74ce305a1b44ad254baaa8522639cad`) and
`canonical-findings-executor-20260909.txt` (SHA-256
`f796774f3dee3213f53b10a6cd285a997da1dc4a75dfc6fbbb1a2c0a68083e7b`).
The exact current board response, including current task/review states and
leases, is preserved in `canonical-board-recheck-20260908.json` as well as the
historical snapshots; historical C08/C12
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
`profile.py` `fe740adc0333033bf526a935d9b91e5ff84508bcb15eedbb14adea4a78ab3f75`,
`controls.py` `cf8800c34306eea85fcacaf0e86e1115661f36cb504f58dfe120a17a70adeb5a`,
and `fixtures/emit_jsonl.py`
`0bba10cf4e37fa0203146172f44ee5c0ea1c1fd22bfb9e948f4d74cc113fb12a`.

Focused evidence:

- Python AST parsing of all shipped profiler/fixture scripts: PASS.
- `python3 scripts/hermetic-profile/profile.py --help`: PASS.
- `python3 scripts/hermetic-profile/controls.py --output .../ctrl-aeef9126`:
  PASS; 67 declared cases and 27 result groups, zero Go, network, or build
  invocations. The report hash is
  `52226b0accd97a785c93589ea6ec13ec0fd82ea7ff787ce15c98daf082cddef3`.
  The report truthfully marks top-level raw evidence retention `false` because
  the `missing-raw-artifact` case deliberately deletes its stdout fixture as a
  negative control; the per-case result also records `false`. The source
  identity control includes three forged-capture provenance mutations and the
  post-inventory dirty-source rejection. New public cases reject unreferenced
  invalid groups, incomplete command-record schemas, forged retained
  source-validation artifacts, forged source-output identity, and aggregate
  package-duration overflow, malformed canonical phase records, a malformed
  warm summary, and a hermetic capture that skips the required warm phase. The
  new timed-out/PASS status mutation is rejected as INVALID before aggregation.
  lifecycle controls also reject `--cohort --repeat 3`, failed full trials,
  and partial full-trial module coverage before any Go command can start. The
  hermetic analyzer baseline is valid before each new tamper, and the new
  public regressions reject omitted full-inventory packages, deleted warm
  binaries, and forged wall/monotonic timing.
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
- Focused causal Go regressions passed at `aeef9126`:
  `go test ./agent-cli/internal/acceptance ./go-llm-gateway/pkg/providers/openai
  ./go-llm-gateway/pkg/testing
  ./go-agent-runtime/services/providers/internal/service
  ./go-agent-runtime/services/session/internal/service
  ./go-agent-loop/pkg/agentloop -count=1 -timeout 180s`.
  This includes streaming/non-streaming record/replay, the barrier-controlled
  response-body join and exactly-once cleanup, provider close-error truthfulness,
  recorder close/read failures, cancellation cleanup, and session lifecycle
  cleanup. The barrier regression was repeated with `-count=10` for the two
  capture/replay tests.
- Fresh exact-head recheck from `aeef9126` passed: 67 public cases/27 result
  groups with zero Go/network/build invocations (tracked report SHA-256
  `52226b0accd97a785c93589ea6ec13ec0fd82ea7ff787ce15c98daf082cddef3`),
  Python AST parsing, profiler help, the focused Go packages above, and
  `git diff --check`.

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

The latest inspected current-head rejection is run `34277521278`, job
`102234088178` (`CI (coverage)`) at submitted head
`43519903572c4047bbb0068d9ed73e7027a257d7`. Eight required jobs passed; the
coverage job failed the existing
`TestSessionCLI_DuplexPCMMultiTurnRejectsLaterTurnAudioControl` because its
positive harness-A baseline exceeded the two-second response deadline with
`context deadline exceeded`. The full 367-line hosted log and exact metadata
are retained in `ci-rejection-34277521278.log` and
`ci-rejection-34277521278.json`. This test path is outside C11's two owned
directories, so no runtime repair is authorized here. The new C11 checkpoint
changes the script-owned surface and is being submitted to script CI without a
waiver; this is not a claim that hosted CI is green.

The latest inspected predecessor rejection is run `34294562855`, job
`102288251336` (`CI (coverage)`) at submitted head
`6079ce29b98e553514c2aeb82e78a762f0131faa`. Eight required jobs passed; the
coverage job failed the existing
`TestAskRecordsAndReplaysThroughProviderService/stream=true` at
`agent-cli/test/integration/ask_capture_test.go:58` because
`failed to flush captures: HTTP capture has an active response body`. The exact
metadata and 82-line log are retained in
`ci-rejection-34294562855.json` and `ci-rejection-34294562855.log`, with
SHA-256 values recorded in `assessment.json`. That rejection supplied the
current C11 repair target; the causal fix and deterministic acceptance
regressions are recorded above. The new candidate remains NOT_GREEN until the
script-owned gate runs.

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

Review-27 closure is covered at the exact implementation checkpoint above:
execution validity now requires every declared run group to validate, including
unreferenced and zero-request groups; retained command records require the
command-record-v1 schema, argv/cwd/environment/timeout, timing, status, output
paths, hashes, and byte counts; retained source-validation command artifacts
are checked against their actual captured Git output; and all package-duration
sums are bounded by Go's `time.Duration` maximum. These are public synthetic
regressions in the tracked control report, not claims about hosted CI.

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

The current C11 implementation and exact-head controls are pinned to
`aeef9126ae54ea925dabb260818082db8675b324`; the tracked
`ctrl-aeef9126/controls.json` report is recorded above. The canonical
review findings through review 37 named
unreferenced invalid groups, forged retained Git metadata, aggregate duration
overflow, incomplete command records, stale head/evidence references, and
cohort lifecycle admission gaps. This repair closes those causes and the public
controls reproduce each positive/negative boundary, including canonical phase
validation, exact Git argv/cwd/environment validation, warm/full/cohort schedule
rejection, failed/partial full-trial rejection, non-PASS phase-command
rejection, unmatched and unselected-group filters,
while retaining the earlier repairs for lane
timing, quiet observations, cache containment, no-test markers, repeated
package terminals, and artifact provenance. The next action is to
commit/push this exact evidence checkpoint, update PR #403, and return
`ACCEPTED` to the script-owned CI gate without polling it. Any C11-owned
CI/review finding returns to this task; independent review and post-integration
vertical validation remain external stages. No fresh timing or under-three-
minute claim is made.
