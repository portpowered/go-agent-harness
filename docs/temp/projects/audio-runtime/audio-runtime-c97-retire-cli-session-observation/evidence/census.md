# C97 pre-mutation census

This checkpoint is candidate-independent evidence captured before changing the
owned observation paths.

- Project: `audio-runtime`
- Contract: `audio-runtime-v1`
- Work: `audio-runtime-c97-retire-cli-session-observation`
- Branch: `codex/audio-runtime-c97-retire-cli-session-observation`
- Worktree: `/Users/abdifamily/.codex/worktrees/af44/go-agent-harness/.claude/worktrees/audio-runtime-c97-retire-cli-session-observation`
- Admission: `project-control.py verify-work --type task --name audio-runtime-c97-retire-cli-session-observation` returned `{"status":"admitted","project":"audio-runtime","name":"audio-runtime-c97-retire-cli-session-observation"}`.
- Manifest baseline: `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`
- Startup integration ancestor: `8bdafc7f947a3a2c9856220abdc539437035bd21`
- Planning origin ancestor: `3d3e72786ac6fc1fd47c7e029589e5117674b035`
- Fresh `origin/main`: `d4766c3dbbf2c198142047ead4449d58dd47d485`
- Integrated candidate before implementation: `a8b52cfa63f27a51183fed9abe344ec4fb076965` (`Merge current origin/main for C97 implementation`), with both required ancestors.

## Immutable legacy baseline

At planning origin `3d3e72786ac6fc1fd47c7e029589e5117674b035`:

| path | physical lines | SHA-256 |
| --- | ---: | --- |
| `agent-cli/internal/services/agentruntime/observations.go` | 93 | `50eb559fab133fa57fe295eb369f4aacaded304e69f421d38e79219cd0a65c85` |
| `agent-cli/internal/services/internal/agentruntime/session_runtime_observation.go` | 294 | `5aadc4bc459cd0391e2bb26bc191e36f07dc68634a94dc813d459e4291091592` |
| **combined** | **387** | — |

## Direct caller census

The adapter is constructed by `session_runtime_plan.go`, `session_room_run.go`,
and self-play setup. Its private methods are called by session audio input,
audio output, diagnostics/response lifecycle, tool evidence, and input commit
observation paths. The only direct legacy field consumer outside the recorder
is `session_diagnostics_observation.go` reading `providerBoundaryObserving`; the
existing observation test also checks that trace observers do not retain
`inputPayload`. These compatibility surfaces remain decision-free adapter
views while the new service owns their policy.

## Lease and scope

`scripts/wire-packages.txt` and
`docs/architecture/architecture-size-baseline.json` remain untouched while C79
holds the shared-file lease. No C79/C84/C91-C96, provider, agent-loop, device,
or preserved-failed-task path is in this change.
