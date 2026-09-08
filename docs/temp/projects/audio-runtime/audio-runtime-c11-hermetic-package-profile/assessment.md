# C11 hermetic package assessment

## Decision

`fresh_timing=BLOCKED`. No fresh Go inventory, warm build, or hermetic test
timing was launched on the shared factory host. The current board and worker
snapshot shows C08 as the active owner of `work-task-5`, alongside this C11
worker (`work-task-4`); there is no dedicated runner or exclusive lease. The
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
- Fetched `origin/main`: `b01dbb573a15eb61d1cacfff11d37c2461167987`.
- Implementation checkpoint after the delivery rebase:
  `03bbb0f4ce570381e145808993ef76aaf3ba8441`.
- Payload main observation: `7a3a5e8a93f05c2d2818b55aef7c4cf528e5cd8d`.
- Preserved startup integration ancestor:
  `8bdafc7f947a3a2c9856220abdc539437035bd21`.
- Preserved baseline ancestor:
  `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`.
- `progress.txt` remains the inherited historical C07 checkpoint and was not
  rewritten. C08 paths, the parent checkout, and factory configuration remain
  outside this candidate.

The complete canonical board and worker-session responses are retained in
`canonical-board.json` and `worker-sessions.json`; the immediate measurement
snapshots are `pre-measurement-*` and `post-measurement-*`. The C11 board has
the admitted idea/task but no C11 review row and no C11 rejection feedback.
The live worker IDs in the snapshot are C08
`9ecd5b0c-de15-4254-9852-3a678840ea78` and C11
`f0db221e-3881-44c5-a500-8101ac247010`.

## Implemented tooling and focused evidence

The public surface is `scripts/hermetic-profile/profile.py` with
`inventory`, `warm`, `run`, and offline `analyze`, plus `controls.py` and the
JSONL fixture. It captures effective commands, source/run identity, raw streams,
hashes, process status, monotonic intervals, package inventory, explicit caches,
runner metadata, package timing, repeat variation, no-test/skip classification,
subtest overlap, and timingate-compatible diagnostics. Heavy commands fail
closed without explicit opt-in and valid isolated/dedicated evidence.

Source file hashes at the rebased checkpoint are:
`profile.py` `0cb0ed7ea403c2da0ee072aa38f760abaf977616cb92ef1a23982fd9b4e365e2`,
`controls.py` `3adacedcd97491bdc86c1289928828b78b7580ba49fee03e66214681fd780bcb`,
and `fixtures/emit_jsonl.py`
`0f1892f763e02dcb43bd5008729ededd14a45ef1c7580ac73271811a2938e8c8`.

Focused evidence:

- Python AST parsing of all shipped profiler/fixture scripts: PASS.
- `python3 scripts/hermetic-profile/profile.py --help`: PASS.
- `python3 scripts/hermetic-profile/controls.py --output .../controls-rebased`:
  PASS; 11 public cases and 14 assertions, raw evidence retained, zero Go,
  network, or build invocations. The report is
  `controls-rebased/controls.json`.
- `GOWORK=off go test . -count=1` from `tools/timingate`: PASS.
- `git diff --check`: PASS.

The controls deliberately prove that a low-duration package failure, a nonzero
process with passing-looking JSON, truncated/malformed/empty streams, missing
package terminals, cached output, and missing raw artifacts do not become a
fresh timing PASS. The repeated synthetic cohort also proves lane wall is not
the sum of package durations. The broad six-module hermetic/coverage suites
were not duplicated locally because the active C08 owner makes this host
non-quiet and the handoff explicitly assigns the script CI gate those broad
checks.

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

The exact next action is push this rebased branch and open/update one C11 PR.
Return `ACCEPTED` to the script-owned CI gate after that submission; do not poll
CI or claim that CI is green. Any exact current-head rejection returns to this
same task for a scoped repair. Independent review and post-integration vertical
validation remain external stages.
