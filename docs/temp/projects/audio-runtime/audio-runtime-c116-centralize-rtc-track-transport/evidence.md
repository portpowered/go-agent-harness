# C116 implementation evidence

This checkpoint belongs to the admitted `audio-runtime` project and task
`audio-runtime-c116-centralize-rtc-track-transport`.

## Admission and ancestry

- Worktree: `/Users/abdifamily/.codex/worktrees/af44/go-agent-harness/.claude/worktrees/audio-runtime-c116-centralize-rtc-track-transport`
- Branch: `codex/audio-runtime-c116-centralize-rtc-track-transport`
- Candidate head: `b74b7bd7` (PR #500, https://github.com/portpowered/go-agent-harness/pull/500)
- Accepted main: `3963bc3566da24f8214634c17a9d0f79a6724171`
- Startup revision: `8bdafc7f947a3a2c9856220abdc539437035bd21`
- Manifest hash: `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`
- Admission: `project-control.py verify-work --type task --name audio-runtime-c116-centralize-rtc-track-transport` returned `{"status":"admitted","project":"audio-runtime","name":"audio-runtime-c116-centralize-rtc-track-transport"}`.
- `git fetch origin main` completed. `3963bc...` is an ancestor of the
  candidate and `origin/main` is unchanged; no host checkout was merged or
  reset.

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
constant; the old gateway track tests were removed with the retired policy.

C79 still holds the shared `scripts/wire-packages.txt` and architecture size
baseline lease. Those files were deliberately not edited in this checkpoint.

## Focused evidence

The following completed successfully:

- `go test ./services/rtctransport/...` — 10 tests across the public, private,
  and Wire packages; the focused race run passed 27 tests across 3 packages.
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
- Workspace `fmt`, `vet`, pinned Staticcheck 2026.1, and pinned golangci-lint
  v2.9.0 — all passed with zero findings; coverage registration passed for 179
  packages across 6 modules; `git diff --check` is clean.
- The strict gateway retirement count is `4 + 7 = 11`, below the accepted-main
  `453 + 418 = 871` threshold.

The remaining architecture/Wire red is shared-file ownership, not an
implementation regression: `make wire-check` reports only the unregistered
`go-agent-runtime/services/rtctransport/wire/wire_gen.go`; the architecture
gate reports 42 stale entries for the retired gateway files and the same
unregistered generated file. The one-line v9 reduction is at 215 versus its
216 baseline, and `rtc_runtime.go` is exactly 674 lines versus its 674 baseline.
C79 retains `scripts/wire-packages.txt` and
`docs/architecture/architecture-size-baseline.json`, so those registration,
stale-entry, and downward-baseline edits were not made early.

Prior script-CI run `34730867802` on head `49e6474c` recorded static and
integration failures while unit, race, coverage, Wire-adjacent, and platform
checks passed. The static findings were repaired and reproduced locally above.
The integration log's high-rate remote-device tail loss (trial 15: exactly
6,400 samples, with no drop/overflow/discard counters) remains the documented
C64/provider-audio owner issue; CI was not polled or duplicated locally.

The live board still shows C79's active task in review with its shared Wire
registry and architecture-baseline lease retained. C96 and C107 are terminal,
but their excluded device/probe paths remain outside this task's manifest and
were not mutated.

## Next action

Retain C116 ownership through the shared-file handoff. Push `b74b7bd7`; after
C79's reviewed guarded merge and explicit lease transfer, apply only the
demonstrated ordered Wire registration plus deleted/stale and downward C116
baseline entries, rerun the Wire/architecture gates, and submit this same PR
head to Script CI without polling it here. Any exact-head CI rejection returns
to this task for repair and resubmission.
