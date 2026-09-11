# C40 interactive policy retirement evidence

This evidence directory contains the standalone policy consumer and bounded
verification runner for `audio-runtime-c40-retire-cli-interactive-tool-policy`.
The consumer is a separate Go module and is always built with `GOWORK=off`.
It imports the public runtime tools contract, tools Wire, and the shared
message value type only. It has no CLI, WebMCP, device, credential, terminal,
or ambient configuration dependency.

The executor commands are:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c40-retire-cli-interactive-tool-policy/run.py inventory
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c40-retire-cli-interactive-tool-policy/run.py consumer
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c40-retire-cli-interactive-tool-policy/run.py wrong-oracle
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c40-retire-cli-interactive-tool-policy/run.py public-policy
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c40-retire-cli-interactive-tool-policy/run.py cleanup-control
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c40-retire-cli-interactive-tool-policy/run.py replay-regression
```

`run.py` records exact child argv, selected environment, cwd, exit status,
elapsed time, separate native exit/harness verdict, and bounded stdout/stderr
under `runs/`. Every mode shares one monotonic 600-second aggregate deadline;
each child is capped at 60 seconds and cleanup uses bounded TERM/KILL/reap.
The `cleanup-control` mode deliberately leaves a SIGTERM-ignoring descendant
so the runner's kill-group and reaping path is exercised. It uses the accepted
C16 audio/tool and interruption fixtures read-only, and checks their recorded
SHA-256 values before running the shipped YUI replay. The frozen audio
oracles are 4800 bytes / `0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502`,
3840 bytes / `6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22`,
and the 2400-byte healthy tail at offset 1440 /
`16508b8b42304d49869684c95e47c794b0eb9b54fd9137537dfaa4370097dfbf`.

The replay check is regression evidence only. It does not claim physical
device or acoustic behavior, live Realtime use, CI success, independent
review, merge, or project acceptance.

## Prior merged-head checkpoint (historical)

The current tested source checkpoint is `ecad8fb9633b5a41bfba8bdeaa0c4b9dd8ed2bc9`,
which merges fetched `origin/main` at `5f14c45313cfdc71e000fda209e3408fcf863faf`.
The evidence ledger is maintained in documentation-only descendants of that
tested source checkpoint.
The fresh inventory, GOWORK=off consumer, wrong-oracle, and public-policy runs
all pass on that head. Focused normal/race policy tests, replay-bundle and
strict allowlist regressions, Wire, architecture, registration, fmt, vet, and
diff checks also pass. The interruption replay is not rerun: C38/task198 still
owns the observed 2400-byte result against the frozen 3840-byte oracle, pending
reviewed repair and primary independent vertical acceptance. This checkpoint is
historical; its artifacts are not relabeled as current-head evidence.

## Exact candidate checkpoint

Candidate `cbb5559345da5c81ac7c698ba790c820af825e93c` is a documentation-only
descendant of tested runner repair `3fdbf5bb910ab064b1a10c076ab137c415a447a3`
and preserves the same runtime inputs. Exact-head runs are recorded
under `runs/run-*-20260910T221539Z-*`: inventory, standalone consumer,
wrong-oracle, public policy, and cleanup control all return accepted. Inventory
reports 246 tools dependency packages with no forbidden CLI/config/WebMCP import;
cleanup control records capped output, native exit `-15`, bounded SIGTERM/SIGKILL,
and descendant reaping. The shipped tool workflow retains the marker and exact
4800-byte PCM hash. The interruption replay remains explicitly blocked on the
unrepaired C38/task198 retention finding and is not relabeled or retried.

## Current-main integration checkpoint

Fetched `origin/main=fdf3b2d98914f50577865e825c733e73520b9ef3` was merged into
the preserved C40 branch as `6ada74ed5be03821478f4de2fa66fe7806e135e4`.
Baseline `926ded7b` and startup integration `8bdafc7f` remain ancestors. The
post-merge inventory `runs/run-inventory-20260910T225900Z-56423` accepts this
exact head, with 247 runtime dependencies and no CLI/config/WebMCP imports;
the C40 policy file remains 155 physical lines and aggregate CLI agentruntime
production is 41,131 lines under the same counting method.

Post-merge bounded policy coverage passes: tools normal/race each pass 340 tests
in 17 packages, CLI interactive-policy normal/race each pass 13 tests, coverage
registration passes 175 workspace packages across 6 modules, and diff-check
passes. No new executable was built: the merge changes executable inputs and
the host has only about 1.0 GiB free against the required 2 GiB reserve.
Existing consumer/YUI artifacts therefore remain tied to their recorded source
revisions and are not relabeled as current-head evidence.

The interruption replay remains blocked on C38/task198's unreleased review and
primary vertical acceptance. Its frozen oracle is 3840 bytes with SHA-256
`6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22`, while the
retained failure is the 2400-byte healthy tail with SHA-256
`16508b8b42304d49869684c95e47c794b0eb9b54fd9137537dfaa4370097dfbf`. After
that prerequisite and storage recovery, rebuild from this merged source, rerun
the unchanged replay and same-source evidence, then submit the changed head to
SCRIPT CI. No CI-green, review, merge, vertical, hardware, or project
acceptance is claimed here.

## Current-head independent policy evidence

The current exact head is `2f3c0463cf1bd4f9a57f8ca05fe6ba542c0e1725`, with
`origin/main=fdf3b2d98914f50577865e825c733e73520b9ef3` and a clean source tree.
The refreshed run IDs and full child logs are recorded under
`runs/run-*-20260910T2324*` and summarized in `candidate-evidence.json`.

Inventory is accepted with 247 runtime dependencies, no forbidden CLI/config/
WebMCP dependencies, 155 policy lines, and 41,131 aggregate CLI agentruntime
production lines. The standalone GOWORK=off consumer is accepted with binary
SHA-256 `bee8cea199a5aac5caf052455457185738087d9dc02b8d19acba0aaea276fa9d`,
literal 5s/20s/2s defaults, 7s/15s/1.2s overrides, snapshot isolation,
pre-effect invalid rejection, zero provider setup calls, and the deliberate
wrong-oracle child exits `1` at the class decision assertion.

The shipped public-policy control builds YUI (50,895,602 bytes,
SHA-256 `a2fdb97f7018fd5bf34e4dc2e5a6c068e8b10c1025ed1c800ba19b0d060436f7`),
reaches the policy call chain, retains `PROBE_TOOL_MARKER_9182`, and produces
the frozen 4,800-byte PCM SHA-256
`0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502`.
Focused normal/race policy tests, 98.3% private-policy coverage, architecture
size (185/1891/27813), 175-package coverage registration, Wire, vet, pinned
lint/staticcheck, and diff checks pass. Cleanup control records bounded
SIGTERM/SIGKILL, descendant reaping, and accepted harness timeout behavior.

The interruption replay remains intentionally unrun: C38/task198 owns the
reviewed close/drain repair and primary vertical acceptance. The retained
failure is 2,400 bytes / `16508b8b42304d49869684c95e47c794b0eb9b54fd9137537dfaa4370097dfbf`
against the frozen 3,840-byte oracle /
`6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22`. This
checkpoint does not claim script CI, independent review, merge, or project
acceptance; after C38 is reviewed and accepted, rebuild and refresh the
dependent replay/evidence before the same PR is submitted to SCRIPT CI.

## Post-C38 acceptance current-head checkpoint

Accepted C38 is now integrated through merge `ab08f6d3a681464730041a3176b492c7f1f23f18`,
with fetched `origin/main=cdf416a8060251a6b4bbf3680241f1ee79953cff` and the
required baseline/startup ancestry preserved. The exact run records are
`runs/run-inventory-20260911T072916Z-41561`,
`runs/run-consumer-20260911T072925Z-41706`,
`runs/run-wrong-oracle-20260911T072932Z-42370`,
`runs/run-public-policy-20260911T072939Z-42431`,
`runs/run-cleanup-control-20260911T072954Z-42638`, and
`runs/run-replay-regression-20260911T073002Z-42688`.

Inventory accepts `247` runtime dependencies with no CLI/config/WebMCP imports;
the CLI policy is `155` physical lines, runtime contract `80`, private policy
`159`, Wire `29` plus generated `28`, and aggregate CLI agentruntime production
is `41,129` physical lines. The standalone `GOWORK=off` consumer rebuilt at
`6,249,666` bytes (`d465d99cb20ca04d0e532bec5a407ca43699538041a8662bbd88e85cf128c507`),
proving the `5s/20s/2s` defaults, `7s/15s/1.2s` overrides, immutable snapshots,
pre-effect invalid rejection and zero provider setup calls; its wrong-oracle
child exits `1` at the literal class assertion.

The shipped policy workflow rebuilt YUI at `50,931,362` bytes
(`f6e480b75f3c84910857e01bb781e90c07b09313755a611465e5c27e4c8431a2`), reached
the runtime policy call chain, emitted `PROBE_TOOL_MARKER_9182` and
`strict replay continuation`, produced the exact `4,800`-byte tool PCM
(`0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502`), and
closed cleanly. The deliberate cleanup control recorded bounded SIGTERM/SIGKILL
and descendant reaping.

The now-unblocked interruption replay passes its frozen `3,840`-byte oracle
(`6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22`) with the
expected `2,400`-byte healthy tail at offset `1440`
(`16508b8b42304d49869684c95e47c794b0eb9b54fd9137537dfaa4370097dfbf`),
`15` wire events, zero tool calls, `replay_complete`, and clean
`recording_closed`. Audio-tool replay remains exact at `18` wire events/one tool
call; all missing/truncated/marker/terminal/tampered negative controls reject.

Focused tools normal/race pass `340` tests in `17` packages each; CLI policy
normal/race pass `13` each; replay-bundle passes `19`, strict allowlist `2`,
policy coverage is `98.3%` against the authorized `80.0%` floor, coverage
registration passes `175` packages across `6` modules, architecture-size passes
at `185/1,891/27,887`, Wire is reproducible, pinned lint/staticcheck are green,
and diff-check is clean. This is executor evidence only: SCRIPT CI, independent
review, guarded merge, the primary's exact-artifact vertical probe, and project
acceptance remain open.
