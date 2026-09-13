# C80 room evidence extraction

This is the admitted `audio-runtime/audio-runtime-v1` task
`audio-runtime-c80-retire-cli-room-evidence-runtime`. No second project or
acceptance waiver is used. Native Windows hardware/endpoints and physical or
acoustic proof are **OUT OF SCOPE** under the user amendment; Windows software
and hermetic replay remain required.

## identity and ancestry

- `prd.branchName`: `codex/audio-runtime-c80-retire-cli-room-evidence-runtime`;
  isolated worktree matches exactly.
- admission: `project-control.py verify-work --type task --name
  audio-runtime-c80-retire-cli-room-evidence-runtime` returned `admitted`.
- accepted behavior: `d5d6f84363d8569d5dc1a59985f8d45cf50e1d06`.
- startup integration: `8bdafc7f947a3a2c9856220abdc539437035bd21`.
- planning origin/main: `84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f`.
- fetched review-time `origin/main`: `84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f`.
- baseline line census: 1,147 + 360 + 287 = 1,794 physical lines.
- no previous C80 review finding or evidence bundle was present; predecessor
  checkpoints and unrelated worktree changes were preserved.

## implementation

`go-agent-runtime/services/roomevidence` now exposes a host-neutral contract;
its private service owns output admission, bounded writers, WAV/PCM artifacts,
clock stamping, speech edges, mix finalization, latency, redaction, integrity,
status and first-error precedence. `wire` constructs independent service
instances without modifying the leased shared Wire registry. The CLI's three
owned production files are decision-free adapters/replay decode fixtures only.

The separate `external-consumer` module is tested with `GOWORK=off` and its Go
source imports only `roomevidence` and `roomevidence/wire`. It records two
participants, consumed sent/received audio, a tool diagnostic and an explicit
drop event, then checks finalized manifest/timeline/digest effects and typed
post-finalize rejection.

## focused evidence

- CLI room evidence focused suite: normal and race patterns passed; the
  recorder-produced room replay roundtrip passed 10 consecutive runs after
  timeline timestamp linearization; full affected agentruntime package and
  roundtrip regressions pass.
- Wire-backed public service suite: 6 tests across 3 packages passed;
  standalone consumer: 5 tests passed with `GOWORK=off`.
- measured retired CLI evidence files: 559 physical lines, leaving 1,235
  lines retired (limit 994; minimum retired 800).
- pinned vet, Staticcheck 2026.1, golangci-lint 2.9.0, coverage registration,
  and `make coverage-changed COVERAGE_BASE=d5d6f84363d8569d5dc1a59985f8d45cf50e1d06`
  pass. Repository architecture/Wire gates report only the unowned shared
  registry and downward baseline entries; those files remain unchanged pending
  exact lease release.
- the first current-head script-CI run was PR `#472`, run `34679683562`, at
  source `05fd938a0eb02fb6f209e56e231ec5ec1a47da54`. Unit, race, coverage,
  hermetic, WebMCP Chrome, macOS audio release and Windows portable software
  passed. Static rejected only the demonstrated shared-file boundaries:
  `make wire-check` reports the C80 generated path
  `go-agent-runtime/services/roomevidence/wire/wire_gen.go` is unregistered;
  architecture reports the C80 downward/stale entries for the reduced
  `session_room_evidence.go` plus its retired `cleanupSetup`,
  `recordingHealth` and `writeManifest` findings, and the same generated-file
  registration. `scripts/wire-packages.txt` remains under C57's active lease
  and `docs/architecture/architecture-size-baseline.json` remains under C61's
  active lease, so neither was edited or waived.
- Integration rejected the known C47-owned high-rate audio regression at
  `TestAgentBinaryTest45HighRateToolAudioRegression/trial_19`: rendered
  `171191/177591` compared samples, exactly `6400` missing, with zero device
  drops/discards and `70` underflow events. C80 does not own that transport or
  audio path and did not weaken the every-sample oracle.
- Bounded post-CI revalidation at the clean source passed C80 service normal
  (`6` tests), service race (`12` tests), external `GOWORK=off` consumer,
  four named negative controls, CLI room/evidence normal (`176` tests),
  C80-owned CLI race (`18` tests), retirement/scope (`559` retained lines),
  and the non-room audio/tool replay. A broad agentruntime race run passed
  `175` tests and reproduced one C75-owned replay-runtime failure; its exact
  normal test passes. No C80 source repair is justified by that peer result.
- The next action is dependency-gated: after C57 releases the Wire registry
  and C61 releases the architecture baseline, integrate the then-current
  accepted `origin/main`, add only the demonstrated C80 registry entry and
  downward/deleted C80 baseline entries, rerun the focused gates, and push the
  same PR for script CI. Current-head CI, independent review, guarded merge
  and an immutable executable vertical probe remain unclaimed.
