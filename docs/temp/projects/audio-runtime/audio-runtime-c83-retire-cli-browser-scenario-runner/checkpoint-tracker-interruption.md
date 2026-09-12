# C83 bounded tracker/interruption checkpoint

## Admission and ancestry

- Factory task: `audio-runtime-c83-retire-cli-browser-scenario-runner`
- Project: `audio-runtime`, session: `~default`
- Admission: `project-control.py verify-work --type task --name audio-runtime-c83-retire-cli-browser-scenario-runner` returned `{"status": "admitted", "project": "audio-runtime", "name": "audio-runtime-c83-retire-cli-browser-scenario-runner"}`.
- Isolated branch: `codex/audio-runtime-c83-retire-cli-browser-scenario-runner`
- PRD branch: `codex/audio-runtime-c83-retire-cli-browser-scenario-runner`
- Accepted base and `origin/main` after `git fetch origin main`: `84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f`.
- Legacy baseline census at the accepted base: runner 1,280; run 956; tracker 487; interrupt 276; fixture options 144; total 3,143 lines.

## Bounded implementation

This checkpoint moves only the evidence-tracker and event-driven interruption behavior into the new public `go-agent-runtime/services/browserrunner` contract, private implementation, and generated Wire graph. The CLI files are thin adapters over that contract. The public boundary has no agent-cli, provider, device, credentials, flag, or mutable-global dependency.

The unchanged legacy runner, its large runner test, and C61 policy/report paths remain in place. The reviewed C61 public scenario contract is not present on `origin/main`; its local branch remains unreviewed and was not cherry-picked or copied. Shared `scripts/wire-packages.txt` and architecture baseline updates remain deferred under the current lease instructions.

## Gate evidence

- `GOWORK=off go test ./services/browserrunner/...`: passed.
- `GOWORK=off go test -race ./services/browserrunner/...`: passed.
- Focused unchanged CLI browser conversation tests: passed in normal and race modes.
- `GOWORK=off go test ./...` in the external consumer module: passed.
- `GOWORK=off go test -race ./...` in the external consumer module: passed.
- Coverage registration: passed, 179 workspace packages across 6 modules.
- New internal service coverage: 70.9% with the declared 70.00% floor.
- `git diff --check`: passed.

## Static rejection repair checkpoint

- Repair commit `351a3894` is pushed to PR `#473` on the same admitted branch. It
  reduces the extracted tracker/interruption complexity, keeps the ordinary
  scenario controller as an inactive non-nil implementation, makes service
  error identities immutable typed constants, moves the public service test to
  the Wire composition package, and registers the generated browserrunner
  graph in `docs/architecture/architecture-policy.json`.
- On this repaired source, normal and race browserrunner tests each passed 45
  cases per repetition across three repetitions; the focused CLI browser
  conversation/scenario controls passed 135 normal cases; the external
  consumer passed normal and race suites across three repetitions; coverage
  registration passed for 179 workspace packages; direct browserrunner Wire
  generation was deterministic; pinned vet, lint, staticcheck and diff checks
  passed.
- `make architecture-size-check` now reports exactly nine stale C83 baseline
  entries, all for the refactored tracker/interruption symbols. No new
  architecture violation remains. `scripts/wire-packages.txt` and
  `docs/architecture/architecture-size-baseline.json` remain untouched: the
  current handoff assigns their shared writer lease to C79.
- The earlier CI run `34680130446` was on pre-repair head `71e32168`: unit,
  race, integration, hermetic, WebMCP Chrome, macOS audio release and Windows
  portable software passed; static failed on the missing shared Wire entry,
  generated-file registration, code-gate findings and the nine stale entries.
  Coverage failed on the C47-owned
  `TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio/test46/slow_device`
  remote-playback final-marker timeout. The known race-only timeout in
  `session_browser_scenario_report_test.go` remains outside C83 ownership;
  its normal control passes.

## Cancellation-publication instrumentation checkpoint

- Commit `13794b8f885ae6c6baf7edabb6b8f1d3b27c4064` instruments the owned
  runner and run paths with ordered `(InvocationID, State, Terminal)`
  observations for `cancel`, `wait_invocation`, and
  `record_cancellation`. It does not alter the existing runner or evaluation
  assertions.
- The named cancellation test passed 3/3 in normal mode and 3/3 under the
  race detector. The hermetic `nomicrophone` plus `coverpkg` run passed 10/10
  with a task-local `GOCACHE`, reporting 7.7% package coverage. The first
  shared-cache coverpkg attempt failed before test execution because unrelated
  Go build-cache imports were missing; the isolated-cache rerun passed.
- Across the bounded verbose runs, every trial retained a terminal
  `wait_invocation` for the canceled invocation and a terminal
  `record_cancellation`; a few coverage trials varied the relative publication
  order of the wrapper `cancel` and wait observations. Existing assertions and
  evaluation behavior remained green, so this is causal interleaving evidence,
  not a demonstrated mechanical defect or claimed fix.
- The current fetch recorded `origin/main` at
  `d4766c3dbbf2c198142047ead4449d58dd47d485`; it is not yet an ancestor of
  this isolated branch. The accepted base `84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f`
  and startup checkpoint `8bdafc7f947a3a2c9856220abdc539437035bd21` remain
  ancestors. No shared C79 files or C61 paths were edited.

## Next action

After C79 releases the shared Wire/baseline lease and C61 supplies its reviewed
public contract through guarded main, fetch and reconcile current `origin/main`
on this same branch. Add only the demonstrated browserrunner Wire registration
and the nine exact downward baseline deletions, rerun the accumulated focused
gates, push PR `#473`, and submit that changed head to script CI without
polling. Then retain the task for any exact CI rejection and repair the same
task; no CI-green, review, merge, vertical probe or project acceptance is
claimed here.
