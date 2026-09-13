# C116 implementation evidence

This evidence belongs to the admitted `audio-runtime` project and task
`audio-runtime-c116-centralize-rtc-track-transport`. It describes the source
tree tested at the current candidate head
`82eed81dcb022b6ace632423a44f52f83d4b016f`. The current candidate includes
the adapter lint/coverage repair `3b40b8b8dd20a7f1816165c65e95aaa7967e3e2d`,
the split coverage-test checkpoint
`7d0a4e80a8a5c97bc9ab13c715f437aa9fb44aba`, and the clean current-main merge
at the candidate head.

## Current candidate checkpoint

- `origin/main` was fetched to
  `09c70f51243caeaf1184c4806b99bbf7749e3044` in this isolated worktree and
  merged with `--no-ff` as `82eed81dcb022b6ace632423a44f52f83d4b016f`.
- The current branch is
  `codex/audio-runtime-c116-centralize-rtc-track-transport`; the worktree was
  clean before this evidence refresh. The running host checkout was never
  merged or reset.
- The pinned golangci-lint failure was repaired by making both compatibility
  adapter `reflect.Kind` switches exhaustive. The prior RTC package coverage
  failure was repaired with behavior-focused compatibility-adapter tests;
  `go-llm-gateway/pkg/transport/rtc` now reports 88.5% against its 87.60%
  manifest floor.
- Current structural gates pass: `make lint` (0 issues), `make vet`,
  `make staticcheck`, `make wire-check` (11 graphs, no drift),
  `make architecture-size-check` (195 packages, 1,920 files, 28,285
  functions), `make coverage-registration` (185 packages across 6 modules),
  and `make coverage` (exit 0).
- Current focused and accumulated gates pass: `make test-rtc-race`,
  `make test-regressions` (agent-cli replay fixtures and all gateway replay
  fixtures), and all five `verify.py` modes. The bounded runner passes
  `rtc-track-roundtrip` (15), `external-media` (93),
  `device-probe-software` (6), `c21-consumption-replay` (3), and
  `credential-free-audio-tool` (3).
- The public external consumer passes with `GOWORK=off` and race detection
  (`ok audio-runtime-c116-rtc-transport-consumer 1.180s`). Runtime RTC race
  coverage passes in 3 packages (45 tests), and the gateway RTC package passes
  its 3x race run (261 tests).
- The earlier Script-CI rejection at the pre-repair head identified exactly
  the two repaired gates: static exhaustive-switch diagnostics and the RTC
  coverage floor. No green Script-CI result is claimed for this new head.

## Admission and ancestry

- Worktree: `/Users/abdifamily/.codex/worktrees/af44/go-agent-harness/.claude/worktrees/audio-runtime-c116-centralize-rtc-track-transport`
- Branch: `codex/audio-runtime-c116-centralize-rtc-track-transport`, matching `prd.json`.
- Admission command from the Factory root:
  `project-control.py verify-work --type task --name audio-runtime-c116-centralize-rtc-track-transport`
  returned `{"status":"admitted","project":"audio-runtime","name":"audio-runtime-c116-centralize-rtc-track-transport"}`.
- The admitted manifest has `project=audio-runtime`,
  `contractRevision=audio-runtime-v1`, and
  `baselineRevision=3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`.
- The exact admitted manifest SHA-256 is
  `ec1439b3b1edf5ab935a59cfe67756b67f4a27e51c20ffcaad87ffab35acdf3d`.
- Startup revision: `8bdafc7f947a3a2c9856220abdc539437035bd21`.
- Accepted main revision: `3963bc3566da24f8214634c17a9d0f79a6724171`.
- The earlier fresh-main checkpoint
  `1a8467246c6607a06ffc7289075da2595724ce8b` remains an ancestor; the final
  `git fetch origin main` updated the isolated worktree's `origin/main` to
  `09c70f51243caeaf1184c4806b99bbf7749e3044`.
- Merge checkpoint `82eed81dcb022b6ace632423a44f52f83d4b016f` integrates that
  current main into this worktree. Startup, accepted main, and current
  `origin/main` are ancestors of the candidate. The running host checkout was
  never merged or reset.

The accepted-main gateway baselines used by the retirement check are:

| file | lines | SHA-256 |
| --- | ---: | --- |
| `go-llm-gateway/pkg/transport/rtc/track_in.go` | 453 | `0e3c85debcd522cf165a4eeac1c12e36e56df81f1dc9faa43dc610d594abc17d` |
| `go-llm-gateway/pkg/transport/rtc/track_out.go` | 418 | `7ebbe790ac32bbfa7fbf7e496793bfc93513574132847bc8715486ac77f67b1d` |

## Implemented boundary

`go-agent-runtime/services/rtctransport` now exposes the host-neutral public
contract and checked-in Wire composition. Its private implementation owns RTP
sequence/SSRC/payload validation, wrap and continuity, reorder and duplicate /
late suppression, one-PLC-per-gap behavior, Opus/PCM frame sizing and rate
conversion, bounded delivery, cancellation, idempotent close, typed errors,
outbound pacing and packetization, caller-sample ownership, and
commit-after-success lifecycle state.

`agent-cli/internal/wire/rtc_runtime.go` constructs the outbound track through
the public `rtctransport` service. Pion signaling and the local RTP writer stay
at the adapter edge. The gateway files retain only narrow compatibility
adapters for unchanged legacy Pion/device-probe callers; transport policy is
owned by the runtime service. The restored device/probe and v9 caller files
are unchanged from fresh `origin/main`, preserving the excluded physical
device/provider-media scope and the existing two-argument probe compatibility
path.

The focused gateway adapters are 262 and 241 lines, 503 combined, below the
strict 871-line accepted-main threshold. Their error identities are immutable
constants; no mutable package-global error state was introduced.

## Ownership and scope repair

C79's guarded merge `1a8467246c6607a06ffc7289075da2595724ce8b` released the
shared registry lease before C116 made its narrow follow-up. The final diff
touches shared files only as follows:

- insert the ordered `go-agent-runtime/services/rtctransport/wire` line in
  `scripts/wire-packages.txt`;
- insert the exact generated-file entry for
  `services/rtctransport/wire/wire_gen.go` in
  `docs/architecture/architecture-policy.json`;
- delete the four stale retired-gateway RTC baseline fragments for
  `track_in.go`, `track_in_test.go`, `track_out.go`, and `track_out_test.go`.

No rejected device-probe, device Wire, CLI probe, C96/device-pump, or
provider-media source path differs from fresh `origin/main`. The deleted
baseline fragments are recoverable from Git history; they are not runtime
source.

## Focused and accumulated gate evidence

At the earlier implementation checkpoint, the following passed:

- runtime RTC package: 15 normal tests and 15 race tests;
- gateway RTC package: 62 normal tests and 62 race tests;
- independent external consumer with `GOWORK=off` and race detection:
  `ok audio-runtime-c116-rtc-transport-consumer 1.181s`;
- causal runtime regressions covering terminal errors, bounded queues,
  cancellation and close propagation, error identity, obsolete packets,
  caller-buffer ownership, and typed-nil dependencies;
- `make test-rtc-race`, including
  `TestPeerS8ConcurrentConnectCloseAndReads`,
  `TestInboundTrackS8ConcurrentIngestReadCancelClose`,
  `TestOutboundTrackSerializesConcurrentWrites`, and
  `TestOutboundTrackConcurrentWriteCancelClose`;
- bounded `run.py` cases: RTC track roundtrip (15), external media (93),
  software device probe (6), C21 consumption replay (3), and credential-free
  audio tool (3);
- `make wire-check`: all 10 registered Wire graphs validated without drift;
- `make coverage-registration`: 182 workspace packages across 6 modules;
- `make architecture-size-check`: 192 packages, 1,914 files, and 28,188
  functions;
- `verify.py` modes `module-boundary`,
  `inbound-positive-and-causal-negatives`,
  `outbound-positive-and-causal-negatives`,
  `retirement-adapter-callers-and-scope`, and
  `final-scope-and-provenance`;
- `git diff --check`.

The bounded runner now enforces one aggregate deadline across all selected
cases and bounds each child by the remaining aggregate time. The focused
external consumer exercises public Wire outbound RTP, cancellation, overflow,
close, typed-nil, frame-size, writer-error identity, and caller-input
ownership behavior.

## Review finding repairs

The prior review's actionable findings were addressed in the source and
regressions:

1. Forbidden device/probe/CLI edits were restored; the verifier now fails
   closed on rejected paths and admits only the transferred shared follow-up.
2. Public external consumption now covers outbound RTP, ownership,
   cancellation, overflow, close, and typed errors.
3. The unchanged two-argument `NewProbeService` callers remain compatible;
   the legacy gateway adapters preserve their source/runtime path.
4. Resampled playout frames are cloned before returning, with an ownership
   regression test.
5. Evidence pins the admitted manifest identity, baseline revision, startup,
   accepted main, fresh main, current implementation checkpoint, and exact
   scope. The final handoff records the exact pushed descendant head.
6. `run.py` enforces the aggregate timeout rather than merely accepting the
   option.

## Preserved excluded finding

The C64/provider-audio high-rate remote-device tail loss and provider-burst
deadline remain owner handoffs. They were not edited or claimed as fixed by
C116; physical Windows hardware and acoustic proof are also out of scope.

## Script-CI handoff

Historical Script-CI results for earlier heads are retained only as context;
they do not certify this candidate. In particular, the prior rejection's
provider-burst residual remains excluded, and the earlier green result was for
an older head. No CI result is claimed for `26b1d45a...` or its docs-only
descendant until the script-owned gate evaluates the exact pushed head.

The next executor action is to push the final descendant, update PR #500 with
that exact head, and submit this same task to script CI. The executor must not
poll CI. Independent review, guarded merge, and the immutable engineering
vertical probe remain open; any exact-head rejection that touches C116 returns
to this task for repair and resubmission.
