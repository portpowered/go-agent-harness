# C11 hermetic package assessment

## Decision

`fresh_timing=BLOCKED`. No fresh Go inventory, warm build, or hermetic test
timing was launched on the shared factory host. At the resumed checkpoint there
was no isolated or dedicated runner evidence; the historical board/worker
snapshot that established the shared-host blocker is retained unchanged. The
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
- Current implementation/control candidate checkpoint:
  `0564fb562802e7cc2185883498936b3cfa9c87ba`.
- The source/control evidence in this report was generated from that exact
merged-main checkpoint; the later evidence commit only adds owned reports.
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
`canonical-board-0564fb5.json`, `canonical-board-be9f11a5.json`,
`canonical-findings-be9f11a5.json`, and
`worker-sessions.json`; the immediate measurement snapshots are
`pre-measurement-*` and `post-measurement-*`. The current board rejection and
its exact failed job metadata are captured in `ci-rejection.json`; the latest
current-head Windows rejection is captured in `ci-rejection-windows.json`, and
the latest pre-C12 hermetic rejection is captured in `ci-rejection-hermetic.json`.
The rejected head was `5c6d24ac…`, before C08 merged into main; after the
required rebase, the narrow rejected test passed once and no C11-owned path was
implicated. This is a recheck and handoff record, not a claim that script CI is
green.
The exact current board response, including current task/review states and
leases, is preserved in `canonical-board-0564fb5.json`; historical C08/C12
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
`profile.py` `dd3766b36eebf231b137c70d61d6b9ec9875e922eff7ba5a5e7732bb71cd339f`,
`controls.py` `64f500281f020cc8efa074eb1cfc0e8727c2710d2e30169afa687a468cce96f4`,
and `fixtures/emit_jsonl.py`
`d4e4e8a43ace6ed89615362583649f71999096d9ca256cfc8a8a868f65045249`.

Focused evidence:

- Python AST parsing of all shipped profiler/fixture scripts: PASS.
- `python3 scripts/hermetic-profile/profile.py --help`: PASS.
- `python3 scripts/hermetic-profile/controls.py --output .../ctrl-0564fb5`:
  PASS; 36 declared cases and 22 result groups, zero Go, network, or build
  invocations. The report hash is
  `ce3417ad45b77357ef0cee93679a6499d2290a19a0b6aa02acd945855227fee3`.
  The report truthfully marks top-level raw evidence retention `false` because
  the `missing-raw-artifact` case deliberately deletes its stdout fixture as a
  negative control; the per-case result also records `false`.
- Review repair controls additionally prove that cross-package test activity is
  not subtest overlap, `--allow-heavy` reaches invalid shared-host validation,
  absolute and traversal stdout/stderr/quiet-evidence redirects are rejected, and tampered
  per-record source identity, quiet-evidence provenance, requested versus
  completed repetition counts, missing runner/load observations, expired quiet
  evidence, repeated package terminals, oversized `Elapsed` values, oversized
  integer handling, and post-inventory source dirtiness remain fail-closed.
- `GOWORK=off go test . -count=1` from `tools/timingate`: PASS.
- Rejected CI regression recheck:
  `go test ./agent-cli/internal/services/internal/agentruntime -count=1
  -run '^TestRunRoom_ReportsClosedTargetAsRejectedPeerIngress$' -timeout 30s`:
  PASS after rebase.
- `git diff --check`: PASS.
- Fresh merged-main recheck from `0564fb56` passed: 36 public cases/22
  result groups with zero Go/network/build invocations (tracked report
  SHA-256 `ce3417ad45b77357ef0cee93679a6499d2290a19a0b6aa02acd945855227fee3`),
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

The controls deliberately prove that a low-duration package failure, a nonzero
process with passing-looking JSON, truncated/malformed/empty streams, missing
package terminals, cached output, missing raw artifacts, redirected artifacts,
missing provenance, incomplete repeats, weak quiet observations, missing lane
timing, out-of-root caches, no-test inventory conflicts, malformed run records,
and quiet-evidence paths outside the manifest root do not become a fresh timing
PASS. The offline report retains
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
derived from them. The known replay failures remain with the C08/runtime owner;
C11 does not alter those paths or claim to repair them.

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

The current C11 implementation/control evidence is pinned to `0564fb56` and
the tracked `ctrl-0564fb5` report above. The latest canonical task rejection
named five defects: missing lane timing fields, weak quiet observations,
out-of-root cache paths, ignored no-test markers, and malformed run records.
Commit `0564fb56` repairs those causes and the public controls reproduce each
positive/negative boundary. The next action is to push/update PR #403 with the
exact final head and return `ACCEPTED` to the script-owned CI gate without
polling it. Any C11-owned CI/review finding returns to this task; independent
review and post-integration vertical validation remain external stages. No
fresh timing or under-three-minute claim is made.
