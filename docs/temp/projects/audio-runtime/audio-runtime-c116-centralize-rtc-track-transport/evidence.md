# C116 implementation evidence

This checkpoint belongs to the admitted `audio-runtime` project and task
`audio-runtime-c116-centralize-rtc-track-transport`.

## Admission and ancestry

- Worktree: `/Users/abdifamily/.codex/worktrees/af44/go-agent-harness/.claude/worktrees/audio-runtime-c116-centralize-rtc-track-transport`
- Branch: `codex/audio-runtime-c116-centralize-rtc-track-transport`
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

`agent-cli/internal/wire/rtc_runtime.go` now receives the public service from
the composition graph and uses it only to construct the outbound track. Pion
signaling and the local RTP writer remain at the adapter edge.

The existing gateway track constructors remain a compatibility surface for the
unowned device-probe caller and the existing v9 probe test. They are not used
by the owned production RTC composition. Their retirement requires migrating
those callers in their owning task; C116 does not edit the C96/device-pump or
provider-media paths.

C79 still holds the shared `scripts/wire-packages.txt` and architecture size
baseline lease. Those files were deliberately not edited in this checkpoint.

## Focused evidence

The following completed successfully:

- `go test ./services/rtctransport/...` — 9 tests across the public, private,
  and Wire packages.
- `go test ./internal/wire` and the owned runtime transport package — 93 CLI
  wire tests plus the runtime package tests.
- `GOWORK=off go test ./...` from `consumer/` — independent public-contract
  consumer passed.
- `GOWORK=off go list -deps ./...` from `consumer/` — no `agent-cli` or
  `go-llm-gateway/pkg/transport/rtc` dependency.
- `git diff --check` — clean.

## Next action

Retain C116 ownership. Before final script-CI handoff, migrate or obtain the
owning-task transfer for the remaining gateway compatibility callers, run the
accumulated RTC/device/media regressions, and apply the C79 Wire registry and
size-baseline edits only after an explicit lease transfer. Then commit, push,
and submit this same candidate to the script CI gate without polling it here.
