# C116 implementation evidence

This checkpoint belongs to the admitted `audio-runtime` project and task
`audio-runtime-c116-centralize-rtc-track-transport`.

## Admission and ancestry

- Worktree: `/Users/abdifamily/.codex/worktrees/af44/go-agent-harness/.claude/worktrees/audio-runtime-c116-centralize-rtc-track-transport`
- Branch: `codex/audio-runtime-c116-centralize-rtc-track-transport`
- Candidate implementation checkpoint: `59c75f6d5b7bbeff9ff21b2048c5e32e10ea196e` (PR #500, https://github.com/portpowered/go-agent-harness/pull/500)
- Accepted main: `3963bc3566da24f8214634c17a9d0f79a6724171`
- Review-time `origin/main`: `b7d25ca6f0e9b94c62b193059160dfbf446ef1d6`
- Startup revision: `8bdafc7f947a3a2c9856220abdc539437035bd21`
- Manifest hash: `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`
- Admission: `project-control.py verify-work --type task --name audio-runtime-c116-centralize-rtc-track-transport` returned `{"status":"admitted","project":"audio-runtime","name":"audio-runtime-c116-centralize-rtc-track-transport"}`.
- `git fetch origin main` completed. The current main revision is an
  ancestor of the candidate through merge checkpoint `2a83d8c1`; the
  accepted-main and startup ancestors also pass. Only this isolated worktree
  was merged; the running host checkout was never merged or reset.

The accepted-main gateway baselines remain recorded for the compatibility
caller audit:

| file | lines | SHA-256 |
| --- | ---: | --- |
| `go-llm-gateway/pkg/transport/rtc/track_in.go` | 453 | `0e3c85debcd522cf165a4eeac1c12e36e56df81f1dc9faa43dc610d594abc17d` |
| `go-llm-gateway/pkg/transport/rtc/track_out.go` | 418 | `7ebbe790ac32bbfa7fbf7e496793bfc93513574132847bc8715486ac77f67b1d` |

## Implemented boundary

`go-agent-runtime/services/rtctransport` now exposes a host-neutral service
contract and checked-in Wire composition. Its private implementation owns:

- RTP v2, SSRC, payload type, sequence-wrap, timestamp continuity, reorder
  window, duplicate/late suppression, and one-PLC-per-gap behavior;
- exact Opus/PCM frame sizing and supported-rate conversion;
- bounded inbound delivery, cancellation, idempotent close, and typed errors;
- outbound media-clock pacing, marker/sequence/timestamp construction,
  caller-sample ownership, and commit-after-success lifecycle state.

`agent-cli/internal/wire/rtc_runtime.go` now uses the public service only to
construct the outbound track. Pion signaling and the local RTP writer remain at
the adapter edge. The transferred device-probe implementation and v9 software
regression now construct tracks through the public service/Wire graph; no
C96/device-pump or provider-media path was edited.

The former gateway policy is retired. `track_in.go` is a four-line retirement
marker and `track_out.go` is a seven-line Pion-facing clock compatibility
constant; the policy-duplicating gateway track tests were removed with the
retired policy. The legacy race-gate names now cover only the remaining Pion
edge: inbound adapter ingest/read/close and local-track write/bind/unbind
concurrency. They do not reintroduce runtime transport policy.

The fetched main includes the ownership-safe registry migration, so the old
handwritten `scripts/wire-packages.txt` and monolithic architecture baseline
are no longer present on the candidate. C116 applied only its demonstrated
follow-up: register the exact rtctransport Wire output, delete the four stale
retired-RTC baseline fragments, lower the v9 function baseline from 216 to
215, and refresh the generated device Wire import. No C79 branch files or C64
provider-media/device-pump paths were edited.

## Focused evidence

The following completed successfully:

- `go test ./go-agent-runtime/services/rtctransport/... ./go-llm-gateway/pkg/transport/rtc -count=3` — 216 test
  executions passed across four packages.
- `make test-rtc-race` — all four required race-gate tests passed, including
  `TestInboundTrackS8ConcurrentIngestReadCancelClose`,
  `TestOutboundTrackSerializesConcurrentWrites`, and
  `TestOutboundTrackConcurrentWriteCancelClose`; the restored Pion-edge test
  remains under the race detector after its complexity-only helper split.
- The accumulated bounded cases passed 93 CLI wire tests, 6 software v9
  device-probe tests, 3 C21 consumption tests, and 3 credential-free
  audio-tool tests.
- The focused device/probe suite passed 13 tests across 6 packages and the
  focused CLI/v9 media suite passed 35 tests across 2 packages.
- `GOWORK=off go test ./...` from `external-consumer/` — independent public-contract
  consumer passed.
- `verify.py --mode module-boundary`, `--mode inbound-positive-and-causal-negatives`,
  `--mode outbound-positive-and-causal-negatives`,
  `--mode retirement-adapter-callers-and-scope`, and
  `--mode final-scope-and-provenance` — all passed, including module lists,
  dependency census, no reverse import, causal positives/negatives, and scope.
- `run.py` bounded cases — RTC transport, CLI external media, v9 software
  device probe, C21 simulated consumption, and credential-free audio-tool
  regressions all passed.
- `make architecture-check` and `make size-check` — both passed with 189
  packages, 1,903 files, and 27,999 functions; `make wire-check` regenerated
  all nine graphs without drift.
- Workspace `fmt`, `vet`, pinned Staticcheck 2026.1, and pinned golangci-lint
  v2.9.0 — all passed with zero findings; coverage registration passed for 179
  packages across 6 modules; `git diff --check` is clean.
- The modified gateway RTC package independently passes `GOWORK=off go test`,
  `go vet`, pinned Staticcheck 2026.1, and pinned golangci-lint v2.9.0 with
  zero findings after the race-gate repair.
- The strict gateway retirement count is `4 + 7 = 11`, below the accepted-main
  `453 + 418 = 871` threshold.

The prior static red is resolved on this checkpoint: the exact rtctransport
Wire output is now policy-registered, the four retired gateway baseline
fragments are deleted, the v9 baseline records 215, and the restored race test
is below the complexity budget. `rtc_runtime.go` remains exactly 674 lines
against its 674 accepted-main baseline. The focused gates do not claim the
unrelated provider-audio integration residuals are fixed.

Script-CI run `34734522403` on head `6a241da0` was rejected while unit,
coverage, hermetic, and platform checks passed. Its exact actionable findings
were:

- the race gate could not find the three legacy gateway test names; checkpoint
  `5d708990` restored those names as Pion-edge concurrency tests, and
  `59c75f6` split the inbound test into bounded helpers so the size gate also
  remains green;
- the static job still reports the unregistered
  `go-agent-runtime/services/rtctransport/wire/wire_gen.go`, 42 stale
  architecture entries for retired gateway files/generated composition, and
  downward size drift (`device-probe` 215 versus baseline 216 and the RTC
  package 17 versus baseline 19); after the accepted main registry migration,
  C116 applied only the exact registration/stale/decrease repairs now proven by
  the local architecture and size gates;
- integration still reports the C64/provider-audio-owned high-rate remote
  device tail loss of exactly 6,400 samples (`63197/69597`, with zero
  drop/overflow/discard counters) and the provider-burst deadline
  (`rendered_pcm=465600`, `nonzero=150871`, `expected=174391`,
  `final_marker=false`).

The C64/provider-audio high-rate tail loss and provider-burst deadline remain
documented owner handoffs; this task did not edit C64/provider-media/device-
pump paths and no duplicate full integration run was performed locally.

The live board still shows C79's separate PR and the C116 task remains the sole
owner of this branch. C96 and C107 are terminal, but their excluded
device/probe paths remain outside this task's manifest and were not mutated.

## Next action

Push the merged and repaired candidate, update PR #500 with the exact source
and gate evidence, and submit this same changed head to Script CI without
polling it here. Fresh independent review, guarded merge, and the immutable
engineering vertical probe remain open. Any exact-head CI rejection returns
to this task for repair and resubmission.
