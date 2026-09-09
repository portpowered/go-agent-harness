# C14 replay boundary audit

## Decision

The smallest behavior-preserving future extraction is the complete **headless
strict bundle replay** workflow currently split across
`agent-cli/internal/services/replay` and
`agent-cli/internal/services/internal/replay`. It should move behind the
public `go-agent-runtime/services/replay` contract and its private
`services/replay/internal` implementation, with a generated replay Wire
constructor. The extraction must preserve the existing provider-only
`session --replay` route, the live session route, and the legacy passive/turn
route as separate behaviors.

C14 implements no runtime change. This branch contains only this audit and
canonical evidence under the admitted C14 evidence lease. The decision is
ready for the script-owned CI gate after commit and push; it is not a CI,
review, merge, vertical-acceptance, or project-acceptance result.

The current public runtime replay service is an admission/planning service,
not a complete externally reusable strict runner. A no-change conclusion is
therefore not supported: an external module can import
`go-agent-runtime/services/replay` and `replay/wire`, but it cannot import the
CLI-internal strict contract or implementation, and no public runtime Wire
constructor currently exposes `Prepare`, `Run`, and `ValidateComplete` as one
workflow.

Evidence labels used below:

- **Source-proved / C14-unrun** means the pinned source establishes the edge or
  invariant, but this audit did not build or execute it.
- **Historical software proof** means the exact C13 artifact/report proved the
  behavior on source `c3bb663e118de9e73ea3eb211b381e8f86c4f480`; it is retained
  as historical evidence, not a new C14 run or acoustic/device proof.
- **Proposed / unrun** means the destination contract, test, or migration is
  an actionable future slice, not an assertion about the current tree.
- **Known residual** means an intentionally retained route difference or open
  acceptance gap; it is not a waiver.

## Admission, provenance, and ownership

The admitted project is `audio-runtime`, contract `audio-runtime-v1`, session
`~default`, server `http://127.0.0.1:7439`. The exact task is
`audio-runtime-c14-replay-boundary-audit`. At the original PR406 recording, the
live board used task row `work-task-34` in `init`/`PROCESSING`, plan row
`work-plan-33` was complete, and the idea row had the same named Work. The
initial C14 task and review rows had no `_rejection_feedback`; that historical
board snapshot and its complete extracted rejection inbox are preserved in the
earlier checkpoint history. After the operator restart, the same admitted
task was recovered as `work-task-4` in `init`/`PROCESSING` with `work-plan-2`
complete; this is not a replacement task or project. The refreshed raw board is
the current `canonical-board.json`. The owned rejection extraction remains 17
rows: the 12 prior C11/C12/C13 rows plus C14 task and review-36/review-39/
review-41/review-42 rows. Preserved Review-44 feedback is read from the
stage4-restart recovery record and reconciled below without changing that
17-row inbox. Earlier C14 findings remain individually preserved.

The admission command, run from the admitted FACTORY_ROOT, returned:

```text
rtk proxy python3 "$FACTORY_ROOT/factory/scripts/project-control.py" verify-work --type task --name audio-runtime-c14-replay-boundary-audit
{"status": "admitted", "project": "audio-runtime", "name": "audio-runtime-c14-replay-boundary-audit"}
```

The authoritative board was captured with the exact handoff command:

```text
rtk proxy you --server "$FACTORY_SERVER_URL" --json work list --session "~default" --max-results 500 --all
```

The original raw board snapshot was
`dd635d054164e644fc6c8d6a502394c597f3f49392f6c501b976be151b5ce7c7` at the
initial checkpoint. The refreshed recovery board snapshot in
`canonical-board.json` has SHA-256
`2bb5e77cc4d1d1d5ebf396aa1509ca9e0e50f81829082c13674beed671c9b323`. The
later live re-read after the current CI rejection is preserved in
`canonical-board-after-ci-34309378555.json`, SHA-256
`623432ad099d360bc5216bd95547abec73080b684957abdb83b1ad0128e6e3f6`. The
exact current task-row extraction is
`canonical-feedback-after-ci-34309378555.json`, SHA-256
`d833977e2c130c2eab32787c417954b78bad27dca23ca0d4a3e28d6e222ccbb5`, and the
full rejected job metadata/log are preserved in
`ci-rejection-34309378555.json` (SHA-256
`99d76c9f11498a694a978edfc5cb94a9562c1183888f14547f60c80915bdf334`) and
the exact compressed log
`ci-rejection-34309378555-job102332669048.log.gz` (SHA-256
`9ad231f3dbe232a6c00cee6e92981fada8736b9e5da5c227604acbd1f532ebaf`). The
full historical extracted rejection inbox remains
`canonical-rejection-feedback.json`, SHA-256
`6b8f590fac6790e0fbbaa3d64b906a0e2ff45787c7e547a3def8546a397481f9`, with
17 JSON rows. The board was saved as raw JSON even though one historical
feedback string contains unescaped control characters; the extraction used a
permissive JSON reader solely to preserve that full feedback verbatim rather
than clipping or discarding it.

The task packet is `prd.json` at the worktree root. Its authority hashes are:

| authority | path | SHA-256 |
| --- | --- | --- |
| execution plan | `factory/projects/audio-runtime/source-plan.md` | `f715163fb20f46a18837d4a4d19ff6d880aaadf8dbf40acfff88a0a6c5800d37` |
| request | `factory/projects/audio-runtime/request.md` | `4d53be6795ea189d5ae3aac727a76ea5dc5ac3f07c3a5e6280a2bc1e9ddcfeb0` |
| immutable acceptance | `factory/projects/audio-runtime/acceptance.md` | `e08b64af98d5c6ded9deac36b7bc33d7af09c47de15e80821852f8e5553148d5` |

The initial C14 baseline capture, before evidence commits, recorded:

```text
worktree: /Users/abdifamily/.codex/worktrees/af44/go-agent-harness/.claude/worktrees/audio-runtime-c14-replay-boundary-audit
branch:   codex/audio-runtime-c14-replay-boundary-audit
HEAD:     c3bb663e118de9e73ea3eb211b381e8f86c4f480
origin/main after fetch: c3bb663e118de9e73ea3eb211b381e8f86c4f480
git diff origin/main...HEAD: empty before C14 evidence
prd.branchName: codex/audio-runtime-c14-replay-boundary-audit
```

The recovery candidate was still clean at
`d91bea8337a17aae9a1a88fed8491e533268465b` when Review-44 was recorded. The
recovery fetch then advanced the local `origin/main` ref to
`f8e0863222da1bdbf296e2220fcbc081461cc877`, the reviewed C11 merge. The pinned
C14 source and C13 historical evidence remain `c3bb663e`; current main is
recorded separately rather than substituted for that source. As the required
baseline-integration step, this isolated worktree then merged fetched
`origin/main` with `--no-ff`, producing `8e5546fdab0702726de33724aa58245576f085db`.
The merge preserved the C14 evidence lease as the only diff against current
main and did not touch the running host checkout. The candidate retains the
required startup/source/current-main ancestry.

`rtk git fetch origin main` was run in this isolated worktree, followed by the
required `--no-ff` merge of `origin/main`. No reset was performed, and the
running host checkout was not touched. The required ancestry checks passed:

```text
rtk git cat-file -e c3bb663e118de9e73ea3eb211b381e8f86c4f480^{commit}: PASS
rtk git merge-base --is-ancestor 8bdafc7f947a3a2c9856220abdc539437035bd21 HEAD: PASS
rtk git merge-base --is-ancestor c3bb663e118de9e73ea3eb211b381e8f86c4f480 HEAD: PASS
rtk git merge-base --is-ancestor f8e0863222da1bdbf296e2220fcbc081461cc877 HEAD: PASS
```

The startup/bootstrap integration pin is
`8bdafc7f947a3a2c9856220abdc539437035bd21`. The original refactor baseline
is `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`. The inspected audit source and
C13 accepted vertical are pinned to
`c3bb663e118de9e73ea3eb211b381e8f86c4f480`; the fetched current main is
`f8e0863222da1bdbf296e2220fcbc081461cc877` and contains the reviewed C11
merge, and the current candidate integrates it at merge commit
`8e5546fdab0702726de33724aa58245576f085db`. The C13 accepted vertical is a
read-only predecessor dependency;
C11/task4 and its scripts/evidence are disjoint and untouched.

The canonical predecessor validation is explicit: Work
`batch-audio-runtime-c13-recorded-pcm-integrity-vertical-probe-20260908-audio-runtime-c13-recorded-pcm-integrity-vertical-probe`
has `workTypeName=validation` and state `complete`/`TERMINAL` on the live
board. Its accepted assessment also records
`canonicalValidation: {"name":"complete","type":"TERMINAL"}`. This is the
C13 scoped validation prerequisite only; it does not close any C14 story or
project rubric.

The parent manager's source inspection is
`$FACTORY_ROOT/docs/temp/projects/audio-runtime/c14-replay-boundary-audit/source-inspection.json`,
SHA-256 `0f45a0d3679aea33353569ee99eb8bf62441ade7b9984ee87c9b741d25ef3ac6`.
It records the pinned source file set and explicitly identifies this as a
personal source inspection with no new runtime test. The manager's C14 files
remain outside this branch's owned lease; this branch does not overwrite them.

## Source map and public route graphs

All paths and symbols in this section refer to the pinned source revision
unless a historical evidence path is explicitly named. Search results were
used for navigation; the relevant function bodies and callers were inspected
before making the edges below.

### Route A: `session replay <bundle-directory>`

This is the strict, explicit directory command. It is a CLI presentation
adapter over a CLI-internal business service.

```text
root router
  -> cli.NewRouter / core_router.go
  -> core_router.go:186, NewPath("replay <bundle-directory>",
     SessionReplayCommand.Generate())
  -> session_replay.go:SessionReplayCommand.Generate
  -> agent-cli/internal/services/internal/replay.Service.Run
  -> Prepare
       -> recording_directory.go:prepareTraceDirectory
       -> go-agent-runtime/services/replay/wire.NewService
       -> go-agent-runtime/services/replay/internal/plan.Service.ResolveCapturePath
          (root manifest/status/declared-artifact/path/Lstat/hash admission)
       -> resolveTraceDirectory -> readTimeline
          (sequence, nonnegative elapsed_ns, nonempty timeline)
       -> timelineOrigin -> injected ClockFactory -> clock.Deterministic
          -> Prepared.Clock and audioReplay.Clock (evidence-loader timing only)
       -> go-audio/pkg/recording.OpenReplay
          (loads canonical audio-trace evidence and supplies Prepared.Audio;
           production packet input is derived separately from provider wires)
       -> deriveEvidence
          (provider send/receive, initial session.update, model, close/terminal,
           tool call/result shape and exact recorded-tool executor)
       -> go-llm-gateway/pkg/testing.NewReplayWebSocketDialerFromCapture
       -> trackingDialer/trackingConn
          (one connection, exact ordered message types and counts/writes/reads)
  -> agent-cli/internal/services/internal/replay.NewOpenAIRuntimeFactory
       -> OpenAI provider adapter with offline URL/API sentinel
       -> gateway.NewSessionGateway
       -> inference.NewSessionGatewayInferencer
       -> replayMediaInferencer -> headless MediaSession inbound drain
       -> agentloop.New(engine.DuplexSession, prepared tools, bounded buffer)
          (current runtime omits agentloop.WithClock(Prepared.Clock), so the
           engine defaults to clock.Real; future extraction must wire it)
       -> deriveInputActions
       -> coreRuntime.Run
            -> wait for SessionOpen
            -> deriveInputActions decodes captured provider-wire
               input_audio_buffer.append payloads via go-audio/codec
            -> send captured text/PCM actions through AgentLoop ordered ingress
            -> send MessageEnd boundaries
            -> count provider response.done terminals
            -> drain terminal deltas on cancellation
  -> prepared.ValidateComplete
       -> tracking state validation
       -> recordedToolExecutor.validateComplete
  -> session_replay.go prints verification only after Run returns nil
```

The public CLI command and result presentation are at
`agent-cli/internal/transport/cli/session_replay.go:10-31`. The command does
not open a provider, device, or executable tool. The strict service contract is
at `agent-cli/internal/services/replay/interface.go:33-119`; the private
business implementation is at
`agent-cli/internal/services/internal/replay/service.go:45` and its
`Run`, `Prepare`, `readTimeline`, `deriveEvidence`, `trackingDialer`, and
recorded-tool methods. The strict runtime factory begins at
`agent-cli/internal/services/internal/replay/runtime.go:27`, and its
`coreRuntime.Run` and `deriveInputActions` are the actual provider/AgentLoop
construction and action driver. `media.go:1-54` claims and drains inbound
media and supplies a virtual playback controller; it does not open hardware.

`agent-cli/internal/services/internal/replay/service.go:75-96` opens the
canonical `audio-trace` with `recording.OpenReplay`, attaches the deterministic
clock, and stores that evidence reader as `Prepared.Audio`. This is evidence
loading and scope accounting; the production strict factory does not consume
that reader. `runtime.go:31-70` uses `Prepared.Capture`, `Dialer`, and
`ToolExecutor`, while `deriveInputActions` at `:241-315` decodes the captured
provider-wire `input_audio_buffer.append` payloads and sends those PCM chunks
through `AgentLoop`. The only current `Prepared.Audio` packet consumer is the
test fake in `agent-cli/internal/services/internal/replay/service_test.go:260-270`.
The future extraction must keep these as separate evidence-loading and
runtime-packet-consumption contracts; a non-nil audio reader or a
`RecordedPCM` scope flag is not proof that production replay consumed it.

The same current factory also never reads `Prepared.Clock`: its
`agentloop.New` call at `runtime.go:56-62` omits `agentloop.WithClock`, and
`go-agent-loop/pkg/engine/engine.go:87-98` consequently retains its default
`clock.Real{}`. The future extraction must keep audio evidence loading and
runtime-packet consumption separate, while explicitly passing the prepared
deterministic scheduler into the AgentLoop. A non-nil audio reader, a
`RecordedPCM` scope flag, or a deterministic clock attached only to the trace
reader is not proof of production packet or timing-domain parity.

The strict path is produced by the generated CLI graph:

```text
agent-cli/internal/wire/wire.go:306-340
  -> servicewire.NewReplayClockFactory
  -> servicewire.NewReplayService
  -> cli.NewSessionReplayCommand
agent-cli/internal/wire/wire_gen.go:106-107
  -> wire2.NewReplayClockFactory
  -> wire2.NewReplayService(clockFactory)
  -> cli.NewSessionReplayCommand(service2)
```

`agent-cli/internal/services/wire/replay.go:12-34` currently constructs the
CLI-internal strict service. `go-agent-runtime/services/replay/wire` currently
constructs only the separate admission/planning service, via
`wire.go:15` and generated `wire_gen.go:17`; it has no strict constructor.

Completion is intentionally strict: `Service.Run` invokes the runtime, then
calls `Prepared.ValidateComplete`, so a successful presentation is downstream
of both provider-wire and tool-consumption validation. A provider response
terminal can be followed by the runtime's cancellation/close path when a
capture has no explicit `session.closed`; this is encoded by
`deriveEvidence` and the runtime's terminal drain, not by silently accepting a
partial timeline. Source-proved / C14-unrun.

### Route B: `session --replay <capture>` provider/live route

This route is not the strict command. It is a host-admitted continuous session
when the capture is classified as realtime and the invocation has a modifier
such as `--audio-out`; passive bare replay can deliberately remain legacy.

```text
SessionCommand.runSessionCommand
  -> buildSessionRequest
       (ReplayPath, ReplayTiming, audio/file/device/tool/prompt flags)
  -> runSessionRequest
  -> runtimeLiveAdmission
       -> if WebRTC or no LiveService: legacy
       -> if no replay: live
       -> if passiveReplayRequest: legacy
       -> runtimeReplay.Service.InspectCapture
          -> replay/wire.NewService
          -> replay/internal/plan.InspectCapture
             -> ResolveCapturePath / provider artifact admission
             -> classify CaptureKind and, for supported shapes, LivePlan
       -> inspection.IsRealtime
  -> runRuntimeLiveSessionWithAnnouncements
  -> livehost.Run
  -> livehost.BuildRequest
       -> admitReplay (requires realtime capture; uses admitted CapturePath)
       -> buildReplayPlan (copy bounded PCM chunks, prompt and lifecycle flags)
       -> assembleLiveRequest
          (opaque credential reference, provider/model, ReplayPolicy, ReplayPlan,
           rates, tools, finish/expected-response policy)
  -> sessionwire.NewLiveService / services/session/internal/live
  -> OpenLive (allocates a lazy handle)
  -> Start
       -> prepareStart
            -> host capability admission
            -> agent-cli/internal/wire/live_service.go:newLiveInferencerFactory
            -> providers.SessionService.BuildSession
       -> buildLoop
            -> capturingInferencer
            -> agentloop.New(engine.DuplexSession)
            -> bounded tool executor/definitions and injected scheduler clock
       -> bindPlaybackController
            -> device PlaybackController when a device port exists
            -> replayVirtualPlaybackController otherwise
       -> runReplay
            -> wait SessionUpdated when LiveReplayPlan requires it
            -> wait media readiness
            -> bounded Outbound.WriteFrame per chunk
                 -> go-audio/pkg/codec.EncodePCM16 in loopAudioOutbound.WriteFrame
            -> ordered Send(AudioCommit)
            -> wait preceding response before next captured turn
       -> live/control.go:Send
            -> ordered AgentLoop ingress; AudioCommit/ResponseCancel mapping
       -> observation.go
            -> output/tool/terminal/session.updated observations
            -> response ID de-duplication and replay response wakeups
       -> cancellation/interruption handling
            -> replay planner marks InterruptionReplacementExpected only when
               cancelled response.done is followed by a distinct response.created
            -> finite response completion waits for the healthy replacement
       -> lifecycle.go:finishOnceBody
            -> stop liveness, close capabilities, flush capture
            -> finishMedia (graceful inbound drain only; cancellation does not
               claim a local render tail)
            -> final terminal classification and event publication
```

### Provider-route tool and working-directory boundary

`session --replay` does not call the strict service proposed above. Its
provider-facing construction is the following separate chain:

```text
session_command_support.go:buildSessionRequest/runSessionRequest
  -> livehost/request.go:BuildRequest/admitReplay
  -> session/wire.NewLiveService
  -> agent-cli/internal/wire/live_service.go:newLiveInferencerFactory
  -> providers.SessionService.BuildSession
  -> providers/internal/service.sessionDialer:179-194
  -> gatewaytesting.NewReplayWebSocketDialer(cfg.ReplayPath)
```

`go-agent-runtime/services/replay/internal/plan.Service.InspectCapture` and
`ResolveCapturePath` only admit/classify the provider artifact; they do not
execute the recorded tool. The tool capability is injected by the CLI graph:
`agent-cli/internal/transport/cli/session_capabilities.go:28-75` builds
`NewSessionToolCapabilitiesFactory`/`NewSessionToolCapabilitiesFactoryFromService`,
which calls `agent-cli/internal/services/wire.NewToolCapabilitiesService` and
`services/internal/tools.Service.Resolve`. That resolver applies
`agent-cli/internal/tools.ResolveFilesystemPolicy` to the effective
`FilesystemWorkDir` and additional allowed roots before
`agentruntime.service.go:247-299` supplies `capabilities.Executor` to the
session loop. This is the exact capability/factory boundary; the replay
admission service is not a tool registry.

The C13 tool fixture is not cwd-independent: its `exec` arguments write to the
relative path `evidence/runs/exec-invocations-v4.log`, and the optional
`working_dir` field is part of the advertised tool schema rather than a
captured absolute execution root. The historical C13 provider replay passed
when launched from the evidence-root cwd. The independent pinned strict replay
also passed, but a provider replay launched from the reviewer worktree failed
on that relative tool path. Therefore a provider-route replay requires an
explicit effective `WorkDir` whose filesystem policy contains the fixture's
relative target (or a fixture rewritten to an explicitly rooted path); it must
not inherit an accidental repository cwd. A sandbox/allow-path restriction is
also a valid failure and must be reported as tool admission failure, not
silently treated as a successful replay.

This is a portability limitation of the provider/tool route, not evidence that
strict replay is broken or that both routes have parity. The C13 provider result
remains historical software proof under its evidence-root precondition; the
reviewer-worktree failure is a reproduced portability diagnostic; and C14 has
not run a new provider process. A future external parity control must set and
record an absolute WorkDir/allow-path policy, then assert the tool marker and
cwd-relative side effect. A run without that precondition is **BLOCKED**, not
PASS, and must not be used to close EMBED, TRACE, REPLAY, or PARITY.

The command-to-route edge is
`agent-cli/internal/transport/cli/session_command_support.go:179-190`. The
route classifier is
`agent-cli/internal/transport/cli/session_observability.go:88-112`; passive
classification is `legacyReplayOwnsPassiveInvocation`/
`passiveReplayRequest` at `:114-151`. The host boundary is
`agent-cli/internal/transport/cli/internal/livehost/request.go:31` and
`run.go:40-320`. The normalized public request is
`go-agent-runtime/services/session/live_request.go:8-109`; replay plan and
policy are `live_replay.go:21-94`.

The session Wire boundary is `go-agent-runtime/services/session/wire/live.go:14-26`; it accepts
`LiveDependencies` and does not construct a provider. The CLI provider
composition is `agent-cli/internal/wire/live_service.go:22-116`:
`provideLiveService` supplies `sessionwire.NewLiveService`, and
`newLiveInferencerFactory` calls the public
`go-agent-runtime/services/providers.SessionService.BuildSession` contract.
The provider implementation is
`go-agent-runtime/services/providers/internal/service/session.go:32-170`.
On `ReplayPath`, its `sessionDialer` uses
`gatewaytesting.NewReplayWebSocketDialer`, then the private provider replay
shim in `go-agent-runtime/services/providers/internal/service/replay.go` sends
the exact captured initial `session.update` before the normal provider gateway
and inference layers.

`OpenLive` is lazy: `services/session/internal/live/service.go` allocates the
handle, while `start.go:start` performs capability/provider construction and
loop installation. `start.go:70-122` installs the duplex AgentLoop and keeps
the session config event out of a self-driving replay that already owns a
replay plan. `replay.go:295-345` drives bounded chunks and commits. The
ordered-control edge is `control.go:Send`; the interruption and readiness
edges are `replay.go:waitReplayReady` and
`observation.go:observeOpeningPolicies`. The interruption terminal
classification is `go-agent-runtime/services/session/internal/live/control.go:324-341`
(`finiteResponseWasInterrupted`), while replay replacement detection is
`go-agent-runtime/services/replay/internal/plan/interruption.go`.
Terminal cleanup is
`lifecycle.go:finishOnceBody` and `finishMedia`.

### Route C: legacy turn/passive replay

This path is still reachable and must not be mistaken for the new runtime
strict workflow or treated as dead code:

```text
SessionCommand.runSessionRequest
  -> runtimeLiveAdmission returns false for WebRTC, turn, or passive replay
  -> internal/services/internal/agentruntime.Dispatcher.Run
  -> requestOptions / prepareTrace
  -> RunSession
  -> planSessionRuntime
  -> planReplaySessionRuntime
       -> planOpenAIReplayRuntime for supported OpenAI WebSocket captures
       -> replaySessionCapture for generic/non-WebSocket captures
       -> old loadReplaySessionConfiguration,
          loadReplaySessionPrompt, loadReplaySessionAudioTurns,
          newReplayInitialSessionUpdateDialer helpers
  -> legacy loop/device/recording/terminal path
```

The dispatcher entry is
`agent-cli/internal/services/internal/agentruntime/service.go:34-120`; the
replay helper family is
`agent-cli/internal/services/internal/agentruntime/session_replay.go`. The
legacy path is covered by CLI integration fixtures and is deliberately retained
for bare/passive and turn semantics. It is not the proposed extraction source;
moving it would mix presentation policy, legacy request translation, and the
complete strict evidence contract.

### Interruption and terminal ownership across routes

The realtime route's causal interruption edge is explicit in
`go-agent-runtime/services/replay/internal/plan/interruption.go` and
`go-agent-runtime/services/session/live_replay.go`: a cancellation is a
replacement boundary only when the capture contains a later distinct response.
`services/session/internal/live/replay.go` sends the next finite turn only after
the preceding response boundary; `observation.go` de-duplicates response IDs
and wakes the replay worker; `lifecycle.go` keeps cancellation from being
reported as a successful local render. This is source-proved / C14-unrun.

The strict route has a different but complete current boundary for the shapes it
supports: `deriveEvidence` converts recorded provider send/receive and tool
records into an exact replay capture; `deriveInputActions` rejects unsupported
response cancellation/overlapping unfinished action shapes; the tracking
transport rejects order/type divergence; `coreRuntime.Run` counts provider
terminal events and drains buffered terminal deltas. Interruption parity is a
required future external-consumer regression, not a claim that every capture
shape is currently externally reusable. Historical C13's artifact-3 strict
replay is retained below.

## Current ownership and extraction gap

| concern | current public contract | current private implementation | current Wire/composition | assessment |
| --- | --- | --- | --- | --- |
| capture admission/planning | `go-agent-runtime/services/replay/service.go:46-57`, `replay.Service` | `services/replay/internal/plan/{capture,directory,audio,tool_actions,interruption}.go` | `go-agent-runtime/services/replay/wire/{wire.go,wire_gen.go}` | Reusable runtime admission and bounded LivePlan; not strict `Prepare/Run`. Keep it. |
| strict replay boundary | `agent-cli/internal/services/replay/interface.go:33-119` | `agent-cli/internal/services/internal/replay/{service,runtime,media,recording_directory}.go` | `agent-cli/internal/services/wire/replay.go`, then `agent-cli/internal/wire/wire_gen.go` | Complete but CLI-internal; inaccessible to `tests/embedding`. Extract. |
| live session execution | `go-agent-runtime/services/session/{live_request,live_replay}.go` | `services/session/internal/live/{service,start,replay,control,observation,lifecycle}.go` | `services/session/wire/live.go` plus `agent-cli/internal/wire/live_service.go` | Runtime-owned live execution; host owns provider/replay construction. Preserve. |
| provider gateway | `go-agent-runtime/services/providers/service.go` | `services/providers/internal/service/{session,replay}.go` | provider Wire in the CLI graph | Provider protocol and raw replay adapter. Do not pull into strict business contract. |
| CLI presentation | Cobra command and route flags | `agent-cli/internal/transport/cli/{session_replay,session_command_support,session_observability}.go` | `agent-cli/internal/wire/{wire.go,wire_gen.go}` | Remains an adapter. It must not own strict validation after extraction. |
| external consumer | `tests/embedding/go.mod:1-45` imports runtime service/Wire only | current tests cover session/live/devices/recording, not strict replay | GOWORK=off module | Current strict workflow cannot be exercised without CLI imports. Add public replay test after extraction. |
| audio/buffer/device | `go-agent-runtime/services/devices/contract.go` and loop/audio contracts | `go-audio/pkg/audio`, device gateway implementations | device/session Wire | Boundaries are present; C14 must not claim subsystem or physical completion. |

The principal gap is not merely directory placement. The strict service's
`Prepared` contains a private validation closure, a strict transport, a
recorded tool executor, a deterministic clock, an audio-evidence reader, and a
runtime factory. The audio reader is loaded for trace evidence and scope; the
production runtime currently consumes input packets from `Capture` provider
wires, not from that reader. Moving only `service.go` would leave the external
caller without the actual `RuntimeFactory`/media/completion graph. Moving only
the provider factory would expose credentials/device/CLI composition. The
cohesive unit is the entire strict `Prepare`/`Run`/validation boundary, with
audio-evidence loading and packet consumption kept as distinct internal
responsibilities.

## Smallest future extraction

This is the exact destination map for a later implementation task. It is a
proposal, not a C14 write. The current admission service and live route remain
in place while this slice is migrated.

### Public runtime contract

Extend `go-agent-runtime/services/replay/service.go` with a strict namespace
alongside the existing `replay.Service` admission methods. Prefixing these
names avoids confusing the current `Service` (admission/planning) with the
strict runner:

```go
type StrictRequest struct {
    BundlePath string
    Provider   string
    Model      string
}

type StrictEvidenceScope struct {
    Protocol             bool
    Tools                bool
    RecordedPCM          bool
    RecordedRender       bool
    RenderTapUnavailable bool
    DeviceExecution      bool
}

type StrictPrepared struct {
    Capture    testing.SessionCapture
    Dialer     transport.Dialer
    ToolExecutor messages.ToolExecutor
    Clock      clock.Scheduler
    Scope      StrictEvidenceScope
    WireEvents int
    ToolCalls  int
    // ValidateComplete and Close remain methods backed by private state.
}

type StrictRuntime interface {
    Run(context.Context, io.Writer) error
}

type StrictRuntimeFactory interface {
    New(StrictPrepared) (StrictRuntime, error)
}

type StrictResult struct {
    Capture    testing.SessionCapture
    Scope      StrictEvidenceScope
    WireEvents int
    ToolCalls  int
}

type StrictService interface {
    Prepare(context.Context, StrictRequest) (StrictPrepared, error)
    Run(context.Context, io.Writer, StrictRequest) (StrictResult, error)
}
```

The imports above are existing runtime-module dependencies
(`go-agent-loop/pkg/messages`, `go-audio/pkg/clock`, and
`go-llm-gateway/pkg/{testing,transport}`); no CLI package, config, flags,
terminal, credential vault, device registry, or executable tool factory is
part of this public contract. The existing CLI-internal `Request`, `Prepared`,
`EvidenceScope`, `Runtime`, `RuntimeFactory`, and `Result` are the source shape
to migrate, with names made explicit because runtime `Service` already means
admission.

The proposed public `StrictPrepared` intentionally does not expose the current
`*recording.Replay` field as a runtime input. The pinned `Prepare` path still
opens that reader to validate/load canonical trace evidence and derive scope,
but `agent-cli/internal/services/internal/replay/runtime.go:31-70` does not
read it; production packet consumption is the `Capture` provider-wire path
described above. If a later consumer needs to inspect recorded PCM, it should
use a separately named, read-only evidence API with its own assertions. A
`RecordedPCM` scope bit alone must not be presented as runtime audio-consumption
parity.

### Private implementation files

Move the business implementation as one cohesive package under the runtime
service boundary:

| current symbol/file | future exact path and owner | required behavior |
| --- | --- | --- |
| `internal/replay.Service`, `Dependencies`, `ClockFactory`, `Run`, `Prepare` | `go-agent-runtime/services/replay/internal/strict/service.go`, private `strict.Service` | Own bundle admission handoff, origin/deterministic clock, `recording.OpenReplay` for audio evidence/scope (not production packet input), strict prepared state, `Run` then `ValidateComplete`. |
| `recording_directory.go:validateRecordingBundle`, `prepareTraceDirectory`, `resolveTraceDirectory` | `go-agent-runtime/services/replay/internal/strict/directory.go` | Keep root-manifest Lstat/nonregular/symlink rejection, absent-manifest legacy trace compatibility, trace path selection, and runtime admission dependency. |
| `readTimeline`, `timelineOrigin`, event decoding and `deriveEvidence` | `go-agent-runtime/services/replay/internal/strict/evidence.go` | Keep bounded JSONL parsing, exact sequence/elapsed checks, initial handshake/model/terminal validation, provider send/receive projection, and tool shape validation. |
| `trackingDialer`, `trackingConn`, replay state | `go-agent-runtime/services/replay/internal/strict/transport.go` | Keep one-connection, ordered message type/count/write/read validation and bounded divergence errors. |
| `recordedToolExecutor` and tool decoders | `go-agent-runtime/services/replay/internal/strict/tools.go` | Keep exact call ID/name/arguments, result matching, exact-once consumption, and unconsumed/missing-result failures. |
| `NewOpenAIRuntimeFactory`, `openAIRuntimeFactory`, `coreRuntime`, `deriveInputActions`, initial-update wrapper | `go-agent-runtime/services/replay/internal/strict/runtime.go` | Keep offline OpenAI gateway + AgentLoop construction, decode PCM from captured provider-wire input actions (not `Prepared.Audio`), explicitly reject a nil `Prepared.Clock`, pass `agentloop.WithClock(prepared.Clock)` beside the mode/inferencer/tool/buffer options, preserve input action boundaries/provider terminal counting/cancellation drain, and use no live credentials. |
| `replayMediaInferencer`, virtual playback controller, inbound drain | `go-agent-runtime/services/replay/internal/strict/media.go` | Keep headless media claim needed for provider-owned truncate/interruption and explicitly report no device execution. |
| `NewReplayClockFactory` behavior | `go-agent-runtime/services/replay/internal/strict/clock.go` or a constructor in `strict/service.go` | Build one fresh `clock.Deterministic` from the trace origin per preparation; never fall back to wall time. |

The package may use `go-agent-loop/pkg/{agentloop,engine,messages}`,
`go-audio/pkg/{audio,clock,codec,recording}` and
`go-llm-gateway/pkg/{gateway,inference,providers/openai,testing,transport}`.
It must not import `agent-cli`, `agent-cli/internal`, provider credential
configuration, device backends, the CLI tool/browser registry, or
`go-agent-runtime/services/replay/wire`. The last prohibition is structural:
the future runtime Wire package imports `internal/strict`, so a strict package
that called `replay/wire.NewService` would create the cycle
`replay/wire -> internal/strict -> replay/wire`.

The acyclic admission dependency is a narrow public interface declared in
`go-agent-runtime/services/replay/service.go`, for example
`CaptureAdmission` with only
`ResolveCapturePath(context.Context, string) (string, error)`. The existing
`go-agent-runtime/services/replay/internal/plan.Service` is the sole
implementation/provider of that interface. `strict.Service` receives this
interface in its constructor/dependencies and calls it directly; it never
constructs a Wire graph. This reuses the existing `plan.New` admission rule
without adding a second project-specific validator and without importing the
Wire package upward.

The existing provider shim at
`go-agent-runtime/services/providers/internal/service/replay.go` remains
private provider construction for the live `SessionConfig.ReplayPath` route.
The strict runtime should use its prepared captured dialer and exact initial
update wrapper, not call the provider service or duplicate credential
resolution. A later, separately reviewed protocol-helper consolidation could
consider `go-agent-runtime/services/replay/internal/protocol/initial_update.go`,
but C14 deliberately does not make that shared change: it would widen the
provider ownership surface and create an avoidable cycle/behavior-risk review.

### Wire and CLI composition

The exact future Wire ownership is:

1. Add the public `replay.CaptureAdmission` dependency contract and
   `replay.StrictService` contract in
   `go-agent-runtime/services/replay/service.go`. Keep
   `go-agent-runtime/services/replay/internal/plan.Service` as the shared
   admission implementation; `plan.New` remains the only root-manifest/path
   validator.
2. `go-agent-runtime/services/replay/wire/wire.go` and its generated
   `wire_gen.go` own both `NewService` and `NewStrictService`. The strict
   injector supplies one `plan.New` value as `replay.CaptureAdmission`, a
   deterministic clock factory, and `strict.NewOpenAIRuntimeFactory` to
   `strict.New`, then binds `*strict.Service` to `replay.StrictService`.
   In dependency order the graph is `plan.New -> strict.New -> public
   replay.StrictService`; strict imports the public replay contract and
   `internal/plan`, never `replay/wire`. The generated file is regenerated,
   never hand-edited. The production constructor remains deterministic and
   credential-free; a separate explicit runtime-factory seam is test-only.
3. Keep runtime `NewService` as the provider of the existing admission
   `replay.Service`. The two public services may share the stateless plan
   provider, but `NewStrictService` must not call `NewService` or construct a
   second validator.
4. Replace the strict business construction in
   `agent-cli/internal/services/wire/replay.go` with a thin call to
   `runtimeReplayWire.NewStrictService` (or an adapter whose only job is
   passing the runtime Wire dependencies). It must no longer import
   `agent-cli/internal/services/internal/replay`.
5. Regenerate `agent-cli/internal/wire/wire_gen.go` from
   `agent-cli/internal/wire/wire.go`. The CLI graph owns routing and adapter
   composition; runtime replay Wire owns strict business construction. No
   hand-edited generated output is acceptable.
6. Keep `agent-cli/internal/transport/cli/session_replay.go` as presentation:
   it receives the public strict service, maps request/result values to Cobra
   output, and retains success-only verification text. It must not parse
   timeline, validate manifests, construct gateways, execute tools, or select
   devices.
7. Keep `session_observability.go`, `livehost/request.go`,
   `services/session/wire/live.go`, `agent-cli/internal/wire/live_service.go`,
   and the provider Wire graph as the live/session owner. `session --replay`
   remains the separate `InspectCapture`/`LiveReplayPlan` provider contract;
   strict replay is headless and is not inserted into `LiveDependencies`.

### Tests and external consumer

The migration must add or move the following tests in the future slice:

- `go-agent-runtime/services/replay/internal/strict/service_test.go` for
  `Prepare`/`Run`/completion, timeline origin, tool exact-once behavior and
  missing/unconsumed evidence.
- `go-agent-runtime/services/replay/internal/strict/directory_test.go` for
  absent manifest compatibility and malformed/incomplete/root symlink/
  nonregular/path/digest/declared PCM controls.
- `go-agent-runtime/services/replay/internal/strict/transport_test.go` and
  `runtime_test.go` for handshake, ordered provider wires, model mismatch,
  premature terminal, response overlap/cancellation, output drain and clean
  shutdown.
- `go-agent-runtime/services/replay/internal/strict/media_test.go` for
  headless inbound claim, virtual interruption cursor, and the explicit
  no-device scope.
- Preserve CLI integration controls in
  `agent-cli/test/integration/session_recorded_pcm_integrity_test.go` and
  neighboring session replay tests so both public command routes retain their
  route-specific semantics.
- Add `tests/embedding/replay_test.go` in the existing separate module
  `example.com/agent-runtime-consumer`, using only
  `go-agent-runtime/services/replay` and `go-agent-runtime/services/replay/wire`
  plus ordinary runtime/test data. Add a checked-in minimal replay fixture
  under `tests/embedding/testdata/replay/` in that future task. Run it with
  `GOWORK=off`; it must not import `agent-cli`, flags, terminal output, or
  hidden global initialization.

The external test must execute the public `StrictService` end to end against a
valid fixture and assert the returned scope is protocol/tools plus recorded
PCM evidence, while asserting `DeviceExecution == false`. That scope assertion
is evidence availability, not proof that production consumed `Prepared.Audio`.
The future test must separately verify that the strict runtime sends the exact
PCM encoded in captured provider-wire `input_audio_buffer.append` records,
while the evidence-loader test verifies trace PCM availability and scope. It
must also include a scheduler-observability regression: inject a
`clock.Scheduler` whose `Now`/timer calls are observable, exercise a non-zero
hot-loop pacing case (or the equivalent strict runtime timing seam), and fail
if the AgentLoop uses `clock.Real`. A nil-clock constructor control must return
`ErrDeterministicClockRequired`, and a source-level control must retain
`agentloop.WithClock(prepared.Clock)` in the production option list. This
prevents a deterministic clock that is attached only to `Prepared.Audio` from
being mistaken for runtime timing parity. It
must also cover the negative controls below through the public Wire constructor,
not through a private implementation. This is the proof needed to close
EMBED/SERVICE for a future slice; C14 does not claim it.

## Behavioral parity and negative-control matrix

The exact historical source and artifact references are:

- C13 vertical report:
  `$FACTORY_ROOT/docs/temp/projects/audio-runtime/audio-runtime-c13-recorded-pcm-integrity-vertical-probe.json`.
- C13 accepted assessment:
  `$FACTORY_ROOT/docs/temp/projects/audio-runtime/c13-probe-result-reconciliation/assessment.json`.
- C13 build metadata:
  `$FACTORY_ROOT/docs/temp/projects/audio-runtime/c13-merged-artifact/build.json`.
- C13 executable identity: source `c3bb663e...`, staged `yui` SHA-256
  `c50aad1bc20dee1dc056ba7db2ed242e975fac923b2df1555d942418c1b36d91`.
- C13 source fixture tar SHA-256
  `93f057b556816f342eff26ffd51e54a62af43317b96ca75e8c8d3ba66ed10767`.
- C13 tool/audio fixture `artifact-2.json` SHA-256
  `38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169`.
- C13 interruption fixture `artifact-3.json` SHA-256
  `154477d4086c47f707441e19489dfa1a21d493475b4163e64a2833dca3f17206`.

| control | route and exact control | evidence status and required future assertion |
| --- | --- | --- |
| Valid finalized bundle, provider wire and tool | `session replay <bundle>` and `session --replay <bundle> --audio-out <file>` over C13 artifact-2; strict reported 18 wire events/1 tool call, flag route emitted `PROBE_TOOL_MARKER_9182`, continuation and clean terminal | **Historical software proof.** Future public runtime test must preserve both route results and exact tool count; no credential/device claim. |
| Provider-route tool cwd/sandbox portability | The fixture's `exec` call writes to relative `evidence/runs/exec-invocations-v4.log`; provider construction reaches `newLiveInferencerFactory` -> `providers.SessionService.BuildSession` -> `sessionDialer`/`NewReplayWebSocketDialer`, while CLI capabilities resolve `WorkDir`/allow paths through `NewSessionToolCapabilitiesFactoryFromService` and `ResolveFilesystemPolicy` | **Conditional historical proof / C14-unrun.** C13 provider replay passed from the evidence-root cwd; the independent strict route passed, but a provider replay from the reviewer worktree failed on the relative path. Future parity must record an explicit WorkDir and sandbox/allow-path policy, assert the marker and side effect, and report missing cwd prerequisites as BLOCKED. |
| Audio evidence loading versus strict input packets | Strict `Prepare` opens `audio-trace` with `recording.OpenReplay` and reports `RecordedPCM`; production `runtime.go:31-70` instead uses `Capture` and `deriveInputActions` to decode provider-wire `input_audio_buffer.append` records, while only the test fake calls `Prepared.Audio.Next()` | **Source-proved / C14-unrun.** Future extraction must test these separately: validate/read trace evidence and scope, then compare the runtime's encoded input writes with the captured provider-wire PCM. Do not infer packet-consumption parity from a non-nil audio reader or scope bit. |
| Prepared clock reaches the strict AgentLoop | `service.go:98-122` creates and stores a deterministic clock from `timelineOrigin`; `runtime.go:56-62` does not read `Prepared.Clock`; `agentloop.WithClock` is available at `go-agent-loop/pkg/agentloop/options.go:69-72`; `agentloop.New` forwards `cfg.Clock` at `agent_loop.go:191`; `engine.NewEngine` defaults to `clock.Real{}` at `engine.go:87-98` | **Current limitation / C14-unrun.** The present strict factory is deterministic only for the trace reader, not for AgentLoop hot-loop pacing. Future `strict/runtime.go` must reject nil and pass `agentloop.WithClock(prepared.Clock)`; a scheduler-observing non-zero pacing regression and nil-clock negative control must fail on omission or wall-clock substitution. |
| Provider PCM and rendered PCM | C13 artifact-2 provider PCM is 4,800 bytes, SHA-256 `0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502`; rendered output is 3,200 bytes, SHA-256 `7d2d8221eb8ec0be3e1da4a3ed518e1e183aa56e4ac0140ca0cf761068555805` | **Historical software proof.** Future extraction must keep sample-rate conversion and byte hashes; rendered/file output is not physical consumption. These hashes prove fixture/output bytes, not production consumption of `Prepared.Audio`. |
| Ordered handshake and turns | strict `trackingConn` validates exact message type/order; live `LiveReplayPlan.WaitForSessionUpdated`, `runReplay`, and `waitForResponse` keep PCM/commit behind the provider boundary | **Source-proved / C14-unrun.** Add public positive and reordered/early-append negatives. |
| Clean shutdown and explicit terminal | strict `deriveEvidence`/`coreRuntime.Run` plus `ValidateComplete`; live `finishOnceBody`, `finishMedia`, `TerminalReasonReplayComplete` | **Historical software proof** for C13 clean close and **source-proved / C14-unrun** for the boundary map. Future test must reject success before terminal/close evidence. |
| Interruption followed by healthy replacement | C13 artifact-3: cancelled `resp-c07-interrupted`, later healthy `resp-c07-healthy`; healthy tail 2,400 bytes, SHA-256 `16508b8b42304d49869684c95e47c794b0eb9b54fd9137537dfaa4370097dfbf`; strict replay reported 15 wire events/0 tools | **Historical software proof.** Future external test must preserve replacement identity, healthy tail, cancellation, and clean completion. No C14 rerun. |
| Cancellation without replacement | `InterruptionReplacementExpected` is false unless a distinct later response exists; live completion must not hang waiting for a nonexistent replacement | **Source-proved / C14-unrun.** Add cancelled-only negative/positive terminal control; do not turn timeout into success. |
| Missing/corrupt declared PCM | C13 integrity matrix mutated/deleted/symlinked/digest-corrupt declared `audio/out-000.pcm`; both directory and flag routes exited 1 for all declared controls | **Historical software proof.** Keep root manifest/hash/path admission before provider construction. |
| Digest/path escape | C13 matrix covers wrong digest, `..`, absolute path, parent symlink and escaping parent symlink; runtime `plan/directory.go` validates containment and hashes | **Historical software proof.** Future private and public tests must retain all controls. |
| Malformed/incomplete root manifest | C13 matrix covers malformed/incomplete manifest; current runtime admission rejects before `ResolveCapturePath` returns a provider artifact | **Historical software proof.** Keep declared-artifact completeness; no fallback when a root manifest is present. |
| Root manifest symlink/dangling/nonregular | C13 review-26/28 found old `os.Stat`/`os.ReadFile` bypasses; pinned c3bb source has `Lstat` and explicit symlink/nonregular rejection in `plan/directory.go:92-118` and `agent-cli/internal/services/internal/replay/recording_directory.go:19-40`; C13 matrix rejects dangling, valid external and directory manifests on both routes | **Historical software proof of the repaired baseline.** This reconciles the inherited C13 findings; C14 must not reintroduce the bypass. |
| Timeline sequence/order/elapsed | strict `readTimeline` rejects missing/empty, nonsequential or negative elapsed events; it derives one deterministic origin | **Source-proved / C14-unrun.** Future strict public test must assert sequence, elapsed, timestamp-origin and bounded scan failures. |
| Provider model/handshake mismatch | strict `deriveEvidence` requires initial outbound `session.update`, compares handshake/session.created/request model; provider admission validates provider/model and replays captured handshake | **Source-proved / C14-unrun.** Future public test must reject missing/mismatched model and missing initial update. |
| Missing tool result or unconsumed tool events | strict `recordedToolExecutor` tracks exact call ID/name/arguments and `ValidateComplete`; live restricts tool surface and observes continuation lifecycle | **Source-proved / C14-unrun.** Future public test must reject missing result, wrong ID/args, duplicate consumption, and leftover calls. |
| Premature terminal/response overlap | strict `deriveInputActions` rejects response cancellation/overlapping unfinished input shapes and `coreRuntime` counts provider terminals; live finite response gate counts distinct response IDs and continuation boundaries | **Source-proved / C14-unrun.** Add early-terminal, overlapping response, tool-continuation and cancellation controls. |
| Missing undeclared `timeline.jsonl` | C13 assessment matrix: strict directory route (`session replay`) exited 1; provider-only flag route (`session --replay`) exited 0 | **Known residual, intentionally preserved.** `ResolveCapturePath` validates the declared root/provider artifact and returns the provider capture; it does not semantically parse undeclared trace timeline. Do not mark this diagnostic as passing or silently add a producer/schema contract. |
| Corrupt undeclared trace WAV | C13 assessment matrix: strict directory route exited 1; provider-only flag route exited 0 | **Known residual, intentionally preserved.** Strict `prepareTraceDirectory` opens/validates trace evidence; provider-only replay follows the provider capture path and does not read undeclared trace WAV. This is the requested route-specific TRACE/REPLAY limitation. |
| Manifestless standalone trace | C13: strict directory replay passed the standalone trace; provider-only flag route rejected missing root manifest | **Known legacy scope.** Preserve the narrow strict trace compatibility and the provider route's root-manifest admission semantics; no waiver for malformed present manifests. |
| Device playback/consumption | strict `replayMediaInferencer` drains headless inbound and virtual controller; live file output/device ports use `devices.Service.Open`, playback pump and optional controller/tap | **Not proven by C13.** Queue admission/speaker-enqueued/file sink is not actual device consumption or acoustic output; DEVICE remains open. |
| Queue/drop and long-running behavior | `FrameBuffer.TrySubmit`, `Submit`, `Snapshot`, `Invalidate` are bounded and explicit; C13 reported no drop/overflow in its bounded software runs | **Historical/source evidence only.** Long-conversation slowdown, broad drop characterization, historical tool/duplex intermittency and physical behavior remain open. |

The C13 two failed diagnostic expectations are intentionally not repaired in
C14. The distinction is causal, not arbitrary: `go-agent-runtime/services/replay/internal/plan/directory.go`
and public `replay.Service.ResolveCapturePath` admit a provider capture for the
provider/live route and validate only the root manifest's declared artifacts;
they do not parse `audio-trace/timeline.jsonl` or decode an undeclared trace
WAV. Strict `agent-cli/internal/services/internal/replay/recording_directory.go`
first invokes runtime admission when a root manifest exists, then
`resolveTraceDirectory`, `readTimeline`, `timelineOrigin`, and
`recording.OpenReplay`; it therefore rejects missing/corrupt trace semantics.
Changing provider-only behavior would be a producer/schema and route-contract
change outside C14, not a justified documentation fix.

## Review-39 strict boundary repair reconciliation

The refreshed canonical board was read before this repair. It retains the same
admitted task `work-task-34`, the concluded provenance review `work-review-36`,
and the latest concluded review `work-review-39`; no replacement task, second
project, acceptance waiver, or ownership transfer exists. Review-36's finding
remains resolved by the corrected baseline token at the admission section:
`3194edd97aed588f7cdf2f8c58a69ac21da4c9ad` is the manifest/PRD/source-plan
object, and the recorded object and ancestry checks were rerun.

Review-39 reported two documentation defects on PR #406 head `5824a495`:

1. The route graph cited a nonexistent live `interruption.go`. The actual live
   terminal classifier is `go-agent-runtime/services/session/internal/live/control.go:324-341`,
   specifically `finiteResponseWasInterrupted`; the replay-plan replacement
   detector is `go-agent-runtime/services/replay/internal/plan/interruption.go`.
   The route graph and interruption section now cite those exact owners.
2. The proposed strict boundary implied that production replay consumed
   `Prepared.Audio`. The pinned source instead opens the trace in
   `agent-cli/internal/services/internal/replay/service.go:75-96`, while
   `agent-cli/internal/services/internal/replay/runtime.go:31-70` never reads
   that field. Production input PCM is decoded from captured provider-wire
   `input_audio_buffer.append` records by `deriveInputActions`; only the test
   fake at `service_test.go:260-270` iterates `Prepared.Audio`. The proposed
   contract, implementation map, external-consumer requirements, parity matrix,
   and audio-boundary audit now distinguish evidence loading from packet
   consumption and require separate future controls for each.

Bounded validation after this repair passed without changing runtime source:

- `agent-cli`: `go test ./internal/services/internal/replay ./internal/services/replay ./internal/transport/cli -count=1` — `719` tests in `3` packages.
- `go-agent-runtime`: `go test ./services/replay/... -count=1` — `43` tests in `3` packages; `go test ./services/session/internal/live -count=1` — `68` tests in `1` package.
- Accumulated C13 controls from `agent-cli/test/integration` — `5` tests passed for `TestSessionRecordedPCMIntegrity` and `TestSessionCommand_OpenAIRealtimeReplayPositiveMaxDurationPreservesCompletedArtifact`.
- Source/path controls confirmed `control.go:324` and `plan/interruption.go:10`, no live `interruption.go`, the production `runtime.go` provider-wire input branch, the corrected baseline/startup/source ancestry, matching PRD/branch identity, nonempty audit, and `git diff --check`.

These are focused source and baseline regressions only; no product build,
provider/device invocation, broad local CI, or CI polling is claimed.

This is a documentation repair against the pinned source, not a runtime change.
The prior C13 root-manifest, undeclared-trace, and historical audio/interruption
findings remain explicitly preserved as resolved predecessor evidence or known
residuals; none is silently converted into a current C14 pass.

## Review-41 Wire, provider-cwd, and canonical-validation repair

The live canonical board was re-read for the same admitted task before this
repair. It retains `work-task-34`, concluded `work-review-36`, concluded
`work-review-39`, and latest concluded `work-review-41`; there is no replacement
task, second project, waiver, or ownership transfer. Review-41 rejected PR #406
head `2eb3150244a42f2d58bd4f7ddc36433fc5f65fd9` despite all required checks
being green because the proposed dependency direction could form an import
cycle, provider-route tool replay was cwd-sensitive, and the audit did not
record `canonicalValidation` as `complete`/`TERMINAL`.

The three repairs are causal and scoped:

1. The extraction map now prohibits `internal/strict` from calling
   `replay/wire.NewService`. It defines the narrow injected
   `replay.CaptureAdmission` interface, keeps `internal/plan.Service` as its
   sole implementation, and assigns the runtime replay Wire source/generated
   pair ownership of `NewStrictService`. The dependency order and binds are
   explicit, so `replay/wire -> internal/strict -> public replay/plan` is
   acyclic and strict never imports its own Wire package.
2. The provider route now names the actual capability/factory symbols and
   distinguishes admission from tool execution. It records the relative
   `evidence/runs/exec-invocations-v4.log` fixture path, the historical
   evidence-root cwd precondition, the reviewer-worktree failure, and the
   required explicit WorkDir/allow-path control. This is classified as
   conditional historical software evidence and a portability residual, not a
   new provider pass or strict-route failure.
3. The provenance section records the exact C13 validation Work ID,
   `workTypeName=validation`, board state `complete`/`TERMINAL`, and the
   assessment's `canonicalValidation` object. This is scoped prerequisite
   evidence only and does not claim C14 or project completion.

The focused source/evidence regressions below recheck these documentation
boundaries and preserve review-36's corrected baseline and review-39's
interruption/`Prepared.Audio` repairs. No runtime implementation, generated
Wire file, fixture, predecessor worktree, or policy was changed.

## Review-41 bounded validation

After the Review-41 documentation repair, the focused causal and accumulated
regressions passed without a product build, provider/device invocation, broad
CI suite, or CI polling:

- From `agent-cli`,
  `rtk go test ./internal/services/internal/replay ./internal/services/replay ./internal/transport/cli -count=1`
  exited `0`: `719` tests in `3` packages.
- From `go-agent-runtime`, `rtk go test ./services/replay/... -count=1`
  exited `0`: `43` tests in `3` packages.
- From `go-agent-runtime`, `rtk go test ./services/session/internal/live -count=1`
  exited `0`: `68` tests in `1` package.
- From `agent-cli`, the accumulated C13 controls
  `rtk go test ./test/integration -count=1 -run
  '^(TestSessionRecordedPCMIntegrity|TestSessionCommand_OpenAIRealtimeReplayPositiveMaxDurationPreservesCompletedArtifact)$'`
  exited `0`: `5` tests in `1` package.
- `rtk proxy git diff --check`, nonempty audit, `canonical-rejection-feedback.json`
  JSON length `17`, assessment `canonicalValidation`=`complete`/`TERMINAL`,
  startup/source ancestry, and all named Wire/provider/cwd source markers
  passed.

These checks validate the audit evidence and pinned baseline behavior only. The
future extraction, external-module consumer, explicit provider WorkDir replay,
and fresh exact-artifact vertical probe remain proposed or unrun.

## Review-42 clock-boundary reconciliation

The live canonical board was re-read for the same admitted task before this
repair. It retains `work-task-34` as the sole C14 executor row and adds
concluded `work-review-42` in `fin`/`FAILED`; no replacement task, second
project, waiver, or ownership transfer exists. Review-42 reports that PR #406
head `025b8f48a93c059563c7eb505af0061e18fe69d7` had the required checks and
independent exact-binary replay evidence, but found one remaining audit gap:
the strict production factory does not wire `Prepared.Clock` into AgentLoop.

The finding is causal in the pinned source. `service.go:98-122` creates a fresh
`clock.Deterministic` from `timelineOrigin` and stores it in both
`Prepared.Clock` and the trace reader. However,
`agent-cli/internal/services/internal/replay/runtime.go:56-62` passes mode,
session inferencer, tool executor, tools, and buffer capacity to
`agentloop.New` without `agentloop.WithClock(prepared.Clock)`. The option is
available at `go-agent-loop/pkg/agentloop/options.go:69-72`; the constructor
passes `cfg.Clock` to `engine.NewEngine` at
`go-agent-loop/pkg/agentloop/agent_loop.go:191`, and
`go-agent-loop/pkg/engine/engine.go:87-98` initializes `clock.Real{}` before
overriding it only when an option was supplied. Thus the current strict route
has deterministic trace-evidence timing but a reachable wall-clock default for
AgentLoop hot-loop pacing. This is a real current limitation, not a claim that
C14 should edit runtime source.

The future extraction repair is now exact: in
`go-agent-runtime/services/replay/internal/strict/runtime.go`, reject a nil
`Prepared.Clock` with `ErrDeterministicClockRequired` before constructing the
loop, and add `agentloop.WithClock(prepared.Clock)` to the same production
option list as `WithMode`, `WithSessionInferencer`, `WithToolExecutor`,
`WithTools`, and `WithBufferCapacity`. Keep the `ClockFactory` origin at
`timelineOrigin`; do not construct `clock.Real` or attach the scheduler only to
`recording.Replay`. The runtime Wire constructor must provide the same
per-preparation scheduler and must not add a second clock source.

The required causal control is also explicit. Future
`go-agent-runtime/services/replay/internal/strict/runtime_test.go` must inject
an observable `clock.Scheduler`, exercise a non-zero hot-loop pacing case (or an
equivalent strict runtime timing seam), and assert that its `Now`/timer path is
used; a wall-clock default must fail the test. A nil-clock constructor control
must return `ErrDeterministicClockRequired`, and a source-level control must
retain `agentloop.WithClock(prepared.Clock)` in the production option list.
The public `tests/embedding/replay_test.go` must retain the same timing
assertion through the public Wire constructor. Until that future implementation
and control exist, clock parity is **OPEN / Proposed-unrun**, even though the
trace reader itself is deterministic.

### Review-42 bounded validation

After this documentation repair, the focused baseline and accumulated controls
were rerun without changing runtime source, building the product executable,
using a provider/device, profiling, duplicating broad CI, or polling CI:

- `agent-cli`: `rtk go test ./internal/services/internal/replay ./internal/services/replay ./internal/transport/cli -count=1` exited `0` with `719` tests passed in `3` packages.
- `go-agent-runtime`: `rtk go test ./services/replay/... -count=1` exited `0` with `43` tests passed in `3` packages.
- `go-agent-runtime`: `rtk go test ./services/session/internal/live -count=1` exited `0` with `68` tests passed in `1` package.
- `agent-cli`: the accumulated C13 controls `rtk go test ./test/integration -count=1 -run '^(TestSessionRecordedPCMIntegrity|TestSessionCommand_OpenAIRealtimeReplayPositiveMaxDurationPreservesCompletedArtifact)$'` exited `0` with `5` tests passed in `1` package.

The exact admission command returned the admitted single-project identity again;
the branch matched `prd.branchName`; at this earlier Review-42 checkpoint
`origin/main` was still
`c3bb663e118de9e73ea3eb211b381e8f86c4f480`. The later recovery fetch recorded
current `origin/main` as
`f8e0863222da1bdbf296e2220fcbc081461cc877`; baseline, startup, and source
objects existed; startup/source ancestry checks passed; and `git diff --check`
passed. The current canonical re-read included the latest task feedback and
`work-review-42`; the owned rejection extraction validates as JSON with `17`
rows. The pinned runtime source check confirmed `agentloop.New` at
`runtime.go:56` and no `agentloop.WithClock` in that current implementation;
the audit contains the future wiring and non-wall-clock regression requirement.
The worktree remained limited to C14 evidence changes. These checks close the
Review-42 audit finding as documentation, not as a runtime repair or a project
acceptance claim.

## Review-44 stale-evidence repair

The preserved Review-44 rejection for PR #406 at candidate
`d91bea8337a17aae9a1a88fed8491e533268465b` identified three evidence defects:
the rendered PCM token was malformed, the recorded rejection-inbox digest was
stale, and the handoff described an older unpushed candidate and an old main
revision as current. The finding does not reopen the already reconciled
Prepared.Audio/Prepared.Clock, Wire-cycle, interruption ownership, provider
cwd, or route-integrity findings.

The referenced artifacts were independently checked before this repair:

- C13's read-only rendered artifact
  `$FACTORY_ROOT/docs/temp/probes/audio-runtime-c13-recorded-pcm-integrity-vertical-probe/evidence/runs/bundle-replay.nmWrwW/rendered-output.pcm`
  is 3,200 bytes and hashes to
  `7d2d8221eb8ec0be3e1da4a3ed518e1e183aa56e4ac0140ca0cf761068555805`, matching
  the accepted C13 report.
- The owned `canonical-rejection-feedback.json` hashes to
  `6b8f590fac6790e0fbbaa3d64b906a0e2ff45787c7e547a3def8546a397481f9` and
  parses as exactly 17 rows. The refreshed raw board is separately recorded
  above; Review-44 remains preserved in
  `$FACTORY_ROOT/docs/temp/projects/audio-runtime/stage4-restart/board-before.json`
  and `c14-prd-recovery/predecessor-feedback.json` rather than being silently
  dropped or appended to the 17-row historical inbox.
- The recovery fetch verified current `origin/main` at
  `f8e0863222da1bdbf296e2220fcbc081461cc877`; the pinned audit source remains
  `c3bb663e118de9e73ea3eb211b381e8f86c4f480`, and the PR base ref was still
  `main` at `c3bb663e`. Required startup/source ancestry and the admitted
  branch identity remain intact. No runtime, generated Wire, predecessor,
  factory, or host-checkout file was changed.

The prior nine successful checks on `d91bea83` are historical candidate
evidence only. This evidence correction creates a new same-task candidate;
the new head must be pushed and sent through script CI and independent review
again. No CI result for the repaired candidate, merge, vertical acceptance, or
project acceptance is claimed here.

## Current CI coverage rejection on `71a0e491`

The current executor admission re-read the complete live board with the
required `--session "~default" --max-results 500 --all` command. Its raw stream
hash was
`0bd7c84d3538310833255529da113b7bc0148171c60926c563e403d8b3ed7ba1`.
The exact C14 row is `work-task-4` in `init`/`PROCESSING`; its full
`_rejection_feedback` names PR #406 at head
`71a0e4914f63e4a2a19a09b511ca644e0937fe44`, with only `CI (coverage)` failed.
The saved recovery board and the 17-row historical rejection inbox remain
unchanged so Review-44 provenance is not silently rewritten or dropped.

The complete GitHub run/job metadata was inspected with
`rtk proxy gh run view 34307899376 --json databaseId,headSha,status,conclusion,event,workflowName,url,jobs`
and the complete failed-step log with
`rtk proxy gh run view 34307899376 --job 102328306832 --log-failed`, for run
`34307899376` and job `102328306832`
([run](https://github.com/portpowered/go-agent-harness/actions/runs/34307899376),
[job](https://github.com/portpowered/go-agent-harness/actions/runs/34307899376/job/102328306832)).
The run was a `pull_request` for the exact head
`71a0e4914f63e4a2a19a09b511ca644e0937fe44`, and the job's only failing step
was `Run coverage profiles and coverage gate` (`make coverage`). The other
eight required jobs were terminal-successful: integration, macOS audio
release, race, static, WebMCP Chrome, unit, hermetic, and Windows audio
portable. The complete failed-job log identifies the sole test failure:

```text
--- FAIL: TestRunnerRetainsTypedSilentTerminalAndIsolatesPeer (0.00s)
    runner_test.go:226: room run error = silent_provider_empty_response: provider response produced no observable output
FAIL github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/internal/lifecycle
make: *** [Makefile:303: coverage] Error 1
```

The failure is outside the C14 lease. The candidate diff against fetched
`origin/main` contains only C14 evidence files, and the failing package has no
candidate diff or changed caller. The exact test was run locally with the same
coverage instrumentation ten times and passed every time:

```text
rtk go test ./services/rooms/internal/lifecycle -cover -run '^TestRunnerRetainsTypedSilentTerminalAndIsolatesPeer$' -count=10 -timeout=60s
Go test: 10 passed in 1 packages
```

The complete affected package was then run once under coverage and also passed:

```text
rtk go test ./services/rooms/internal/lifecycle -cover -count=1 -timeout=60s
Go test: 20 passed in 1 packages
```

After this evidence-only edit, the accumulated C14 and predecessor regressions
were rerun with the following bounded results: the three CLI replay packages
passed `719` tests; runtime replay passed `43` tests; runtime live passed `68`
tests; the selected C13 integrity controls passed `5` tests; and the affected
room lifecycle coverage package passed `20` tests. No source package was
changed by this checkpoint.

This is a bounded non-reproduction of an ownership-isolated CI timing/test
failure, not a waiver, a claim of green CI, or a reason to edit room lifecycle
code outside C14. The evidence update is a changed same-task checkpoint; after
it is committed and pushed, PR #406 must be updated and returned to the
script-owned CI gate. If the same out-of-lease failure recurs, report the exact
repeat as an ownership prerequisite for meta inspection rather than modifying
unowned runtime code. No merge, independent review, vertical acceptance, or
project acceptance is claimed.

## Current CI integration rejection and baseline integration

The next script-owned result returned the same task after PR #406 head
`654dae862841574a22feab33ace99bfa774bf30a` was rejected by run
`34309378555`, job `102332669048`. The raw job log and run metadata are
preserved in the owned evidence files named above. The only failing required
check was `CI (integration)`; static, unit, coverage, race, hermetic, WebMCP
Chrome, macOS audio release, and Windows audio portable were successful on that
head. The complete integration log names one failing subtest:

```text
--- FAIL: TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio (0.00s)
    --- FAIL: TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio/test46/provider_burst (30.03s)
        session_tool_audio_remote_e2e_test.go:182: remote playback did not reach final PCM marker before the scenario deadline
```

This is the previously characterized remote-tool/audio timing signature. The
pre-merge candidate diff against fetched `origin/main` contained only C14
evidence files, and the failed test is in an unowned integration path. The
required current-main integration was then performed in this isolated
worktree: `origin/main=f8e0863222da1bdbf296e2220fcbc081461cc877` was merged by
`8e5546fdab0702726de33724aa58245576f085db`. No C14 runtime source was edited,
and no host checkout or predecessor worktree was changed.

The exact rejected subtest was rerun once after that merge with the same
bounded local test path:

```text
cd agent-cli
rtk go test ./test/integration -count=1 -timeout=90s \
  -run '^TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio$/test46/provider_burst$' -v
=== RUN   TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio/test46/provider_burst
--- PASS: TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio/test46/provider_burst (12.38s)
PASS
ok   github.com/portpowered/go-agent-harness/agent-cli/test/integration 21.787s
```

This is a bounded post-integration pass and not a claim that the historical
timing failure is fixed across all remote-tool/audio scenarios. The prior CI
rejection remains recorded as historical evidence; no unchanged implementation
was resubmitted and no CI result for the new candidate is claimed.

## Audio, clock, buffer, and device boundary audit

The replay graphs cross the following existing boundaries; none is a reason to
claim AUDIO or DEVICE completion.

1. `go-agent-loop/pkg/subsystems/audio/audio.go` defines memory-only
   `BufferPort`, nonblocking `CommandPort`, and `Ports`. `Subsystem.Execute`
   only snapshots capture/playback `BufferStats` and submits an interrupt
   command with a playback epoch. Its package comment and implementation do
   not perform provider calls, device I/O, PCM parsing, or DSP on a reasoning
   tick. This supports the intended buffer/core-loop separation.
2. `go-audio/pkg/audio/buffer.go:61-98,193-223` provides bounded
   `TrySubmit`, cancellation-aware `Submit`, epoch invalidation, and
   `Snapshot`. `ErrBufferFull`, `RejectedFrames`, `ConsumedSamples`, and
   `DiscardedSamples` distinguish admission from consumption; there is no
   implicit drop. A replay test that only observes `TrySubmit`/`Snapshot` still
   cannot claim the downstream device consumed PCM.
3. `go-agent-loop/pkg/agentloop/execute_input.go:8-99` represents raw audio as
   `Audio{Samples,SampleRate,Channels}` and delegates PCM16 encoding to
   `go-audio/pkg/codec.EncodePCM16`. The replay/live path separately encodes
   `int16` chunks in `services/session/internal/live/replay.go:247-260` and
   decodes bounded PCM chunks in `services/replay/internal/plan/audio.go`.
   Strict directory replay's production runtime instead decodes provider-wire
   `input_audio_buffer.append` records in
   `agent-cli/internal/services/internal/replay/runtime.go:241-315`; it does
   not read the `Prepared.Audio` trace reader opened by `service.go:75-96`.
   The codec implementation is centralized in go-audio, but callers still
   choose when to encode; this is a migration observation, not proof that one
   independently testable audio subsystem owns all formats/sample timing/DSP.
4. `go-agent-loop/pkg/engine/tick.go:10-31` injects a
   `clock.TimerSource` and waits through `clock.Wait`; the engine hot loop calls
   `Tick` then `waitForNextTick` in `engine.go:220-234`. `go-audio/pkg/clock/clock.go`
   separates `Source`, `TimerSource`, and `Scheduler`, and
   `clock.Deterministic` advances virtual elapsed time independently of logical
   ticks. `Prepare` creates this scheduler, but the current strict
   `runtime.go:56-62` fails to pass it to `agentloop.New`, so the engine's
   `clock.Real{}` default remains reachable. The future strict constructor must
   reject a missing scheduler and call `agentloop.WithClock(prepared.Clock)`;
   its timing regression must observe a deterministic scheduler during pacing
   and fail if wall time is substituted. Neither route may silently substitute
   wall time for trace timing.
5. `go-agent-runtime/services/devices/contract.go:31-180` keeps `Service.Open`,
   `Capture.Pump`, `Playback.Pump`, `PlaybackControllerProvider`, and
   `MediaPorts` behind a public device boundary. The live route binds the
   actual device playback controller when present; strict replay binds a
   virtual controller and drains inbound media. Device worker pacing,
   conversion, interruption, and lifecycle stay with the device service. A
   provider acknowledgement, queue admission, file sink, or
   `speaker_enqueued` trace is not physical consumption.

## Canonical rejection reconciliation

The initial C14 snapshot had no rejection. On the executor re-admission, the
current canonical board showed `work-task-34` in `init`/`PROCESSING` and
`work-review-36` in `fin`/`FAILED`, both carrying the same actionable finding
for PR #406 at head `96c11e8ecb440bdf03e0a958560eeb9525d9e50f`: `audit.md:105`
recorded the non-existent baseline
`3194edd97aed588f7df2f8c58a69ac21da4c9ad`, while the admitted manifest, PRD,
and source plan specify
`3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`. This repair changes only that
evidence token; no runtime implementation or predecessor evidence is changed.
The prior findings that materially touch this audit are accounted for as
follows:

- `work-review-26` reported that
  `agent-cli/internal/services/internal/replay/recording_directory.go:19-24`
  treated a dangling root manifest symlink as absent. The pinned c3bb source
  uses `os.Lstat`, rejects symlinks/nonregular manifests, and preserves only
  truly absent-manifest legacy trace compatibility. C13's matrix covers the
  dangling control on both routes. Resolved in the admitted predecessor; no
  C14 code repair is required.
- `work-review-28` reported that
  `go-agent-runtime/services/replay/internal/plan/directory.go:92-96`
  followed a valid external root manifest symlink through the provider-only
  route. The pinned c3bb source has the Lstat/nonregular root boundary at
  `plan/directory.go:92-118`; C13's matrix rejects dangling, external-valid,
  and nonregular roots. Resolved in the admitted predecessor and preserved in
  this audit's matrix.
- `work-task-23` recorded the old C13 CI coverage/hermetic rejection at a
  superseded head. The accepted C13 vertical and its current build/report are
  tied to c3bb and preserve the 24 declared-artifact/root controls. No C14
  runtime assertion is inferred from that old failure row.
- The remaining C11 and C12 feedback is retained in the full evidence file and
  is outside C14's owned paths. C11 owns `scripts/hermetic-profile/` and its
  evidence; C12 owns live/session interruption repairs. C14 neither edits
  those paths nor reassigns their findings.

No finding was hidden, waived, or self-reviewed. The two C13 undeclared-trace
failures are listed as residuals, not converted to passes.

## Review-36 provenance repair validation

The current executor admission re-ran the exact board and
`verify-work --type task --name audio-runtime-c14-replay-boundary-audit`
commands. It retained the sole C14 owner and identified the same `work-task-34`
and `work-review-36` finding described above. The repair is limited to the
baseline token in this audit; no runtime, generated Wire, source-plan,
manifest, predecessor, or unrelated worktree file changed.

After the token correction, the focused and accumulated regressions were run
without a product build, provider/device invocation, broad CI suite, or CI
polling:

- From `agent-cli`, `rtk go test ./internal/services/internal/replay ./internal/services/replay ./internal/transport/cli -count=1` exited `0` with `Go test: 719 passed in 3 packages`.
- From `go-agent-runtime`, `rtk go test ./services/replay/... -count=1` exited `0` with `Go test: 43 passed in 3 packages`.
- From `agent-cli`, `rtk go test ./test/integration -count=1 -run '^(TestSessionRecordedPCMIntegrity|TestSessionCommand_OpenAIRealtimeReplayPositiveMaxDurationPreservesCompletedArtifact)$'` exited `0` with `Go test: 5 passed in 1 packages`.

The provenance checks were also rerun on the corrected evidence tree. They
reported admission `{"status": "admitted", "project": "audio-runtime", "name": "audio-runtime-c14-replay-boundary-audit"}`;
branch `codex/audio-runtime-c14-replay-boundary-audit`; PRD branch and current
branch equal; PRD/manifest/source-plan baseline
`3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`; startup pin
`8bdafc7f947a3a2c9856220abdc539437035bd21`; source and fetched main
`c3bb663e118de9e73ea3eb211b381e8f86c4f480`; corrected baseline and source
`git cat-file -e` checks PASS; baseline, startup, and source
`git merge-base --is-ancestor` checks PASS; and `git diff --check` PASS. The
changed-path check remained limited to the three pre-existing C14 evidence
paths: `audit.md`, `canonical-board.json`, and
`canonical-rejection-feedback.json`.

This corrected audit was checkpointed in commit
`afd4015cb04972f068091bf80427b83a7ba8287d` (`docs: repair C14 baseline
provenance`). Post-commit checks at that exact local HEAD reported a clean
worktree, `git diff --check` PASS, the same three-file candidate diff against
`origin/main`, a one-file commit diff (`audit.md`), corrected baseline/source
Git-object and ancestry checks PASS, `origin/main`
`c3bb663e118de9e73ea3eb211b381e8f86c4f480`, current branch
`codex/audio-runtime-c14-replay-boundary-audit`, and a non-empty audit file.
The local branch was one commit ahead of the remote PR branch pending push.

## CI coverage rejection reconciliation

The live task row returned this same admitted Work after PR #406 head
`24c0710224d90a840caab8202882fb59b978e027` was rejected by the script-owned
current-head CI gate. The exact completed run was `34293404249`; only
`CI (coverage)` failed, in job `102284678555`. Its `make coverage` log names
one test failure:

```text
agent-cli/internal/transport/cli/internal/events:
TestRoomUsesLiveLivenessForPeerFilteredEventsAndEvidence
room_live_liveness_test.go:41: read SSE payload: context deadline exceeded
```

The other eight required checks on that exact head passed: static, unit,
integration, race, hermetic, WebMCP Chrome, macOS audio release, and Windows
audio portable. The failing package and test are outside the C14 evidence
lease. `git diff --name-only origin/main...HEAD` contains only the three
owned evidence files, and the failed package has no candidate diff; C14 has no
runtime implementation change that could cause or repair this room/SSE timing
failure.

One bounded local reproduction of the exact reported test passed:

```text
rtk go test ./internal/transport/cli/internal/events -tags=nomicrophone \\
  -run '^TestRoomUsesLiveLivenessForPeerFilteredEventsAndEvidence$' \\
  -count=1 -timeout=30s
Go test: 1 passed in 1 packages
```

The C14-focused and accumulated review regressions also passed after the
rejection: the three replay/CLI packages reported `719` tests, runtime replay
reported `43` tests, and the selected C13 integrity controls reported `5`
tests. This is recorded as a bounded non-reproduction of an unrelated CI
timing failure, not as a runtime repair or an acceptance waiver. The next
action is an evidence-only checkpoint and same-task resubmission; if the
coverage test fails again, inspect the new exact log rather than changing
out-of-lease room code.

## Immutable acceptance criteria and later gates

All nine criteria remain `OPEN`. C14 is an audit/extraction decision and cannot
close a project rubric. The exact immutable rubrics and the scoped judgment
for this source revision are:

| ID | immutable rubric | C14 scoped judgment | named later gate |
| --- | --- | --- | --- |
| AUDIO | One independently testable audio subsystem owns packet parsing, formats, clocks, sample timing, DSP and buffer operations; the core loop uses buffers without direct device IO. | **OPEN.** Source shows memory-only loop ports and canonical go-audio buffer/codec/clock edges (`go-agent-loop/pkg/subsystems/audio/audio.go`, `go-audio/pkg/{audio,codec,clock}`), but replay callers and device/audio ownership are not a complete independent subsystem proof; the current strict factory also leaves AgentLoop pacing on its `clock.Real{}` default. | Canonical audio subsystem extraction plus independent clocks/codec/DSP/sample/buffer validation and explicit runtime clock injection. |
| DEVICE | The adjacent device gateway owns physical device abstractions and lifecycle; playback evidence distinguishes actual consumption from queue admission. | **OPEN.** `go-agent-runtime/services/devices/contract.go` is a thin boundary and strict replay is explicitly headless; C13 file/speaker-enqueued evidence cannot prove physical consumption. | Physical device lifecycle and consumed-output tracing with actual device evidence. |
| EMBED | A separate Go module constructs and exercises the runtime without CLI imports, flags, terminal state or hidden global initialization. | **OPEN.** `tests/embedding` is separate and imports runtime Wire, but it has no strict replay consumer because the current strict contract is CLI-internal. | Implement extraction and add `tests/embedding/replay_test.go` with `GOWORK=off`. |
| SERVICE | Services expose thin services/X contracts with private services/X/internal implementations and per-service Wire construction; CLI transport delegates business behavior. | **OPEN.** Admission/session/device services satisfy parts of this shape, but strict replay business logic remains under `agent-cli/internal` and CLI Wire constructs it. | Move strict contract/private implementation and add runtime replay Wire; keep CLI presentation-only. |
| TRACE | Correlated device capture/playback, provider send/receive, tool lifecycle, cancellation, queues/drops and terminal evidence use explicit timing domains and bounded recording resources. | **OPEN.** C13 proves bounded software traces for named cases; provider-only undeclared timeline/WAV acceptance and physical/render limits remain explicit residuals. | Complete correlated four-plane traces, timing domains, bounded resources, and undeclared-trace semantics. |
| REPLAY | Credential-free replay preserves recorded audio packets, ordering, tool events, interruption and termination; hardware and external nondeterminism limits are explicit. | **OPEN.** C13 is accepted scoped software evidence for valid tool/audio and interruption fixtures; the reusable strict workflow is not externally accessible and hardware is excluded. | Fresh exact-artifact strict/provider replay, order, tools, interruption, termination, and hardware/nondeterminism limits. |
| FAILURES | Truncated/no playback, barge-in failure, long-conversation slowdown and tool-continuation collisions have reproducible characterization, fixes where demonstrated, and exact residual limitations without false completion claims. | **OPEN.** C12 interruption repair is historical predecessor evidence; long-conversation, physical/no-playback, tool/duplex intermittency and other broader failures remain open. | Causal characterization/repair and accumulated regressions for each named failure. |
| QUALITY | Architecture boundaries, package/file/function size, complexity, mutable globals, generated Wire consistency and relevant compile/static/race checks pass without growing migration baselines or weakening assertions. | **OPEN.** C14 performs source/diff checks only and makes no implementation or generated-Wire change; no whole-project quality gate is claimed. | Current-head script CI plus architecture/size/complexity/global/generation/static/race gates. |
| PARITY | Supported CLI behavior is preserved and final integrated behavior has fresh independent customer and engineering evidence, with authorized bounded Realtime proof and explicit physical-device evidence limits. | **OPEN.** Both route graphs and historical CLI behavior are mapped; C14 has no fresh runtime run, independent review, merge, post-merge vertical, or project reports. | Fresh post-merge vertical, then same-artifact independent customer/engineering reports, qualitative review, authorized live/physical proof, and verify-completion. |

The matrix is a scoped source judgment, not a replacement acceptance file.
Missing proof never sets `passes:true`.

## Verification performed and handoff state

The following bounded source and local checks were performed without building
the product executable, using a provider or device, profiling, duplicating
broad local CI, or polling a new CI run, as required by the C14 audit execution
policy. The prior gate rejection was inspected only after the script reported
the completed failure; its exact reconciliation is recorded above.

- Read `factory/docs/operating-policy.md`,
  `factory/docs/implementation-handoff.md`,
  `factory/docs/meta-planner-handoff.md`, the audio-runtime request,
  acceptance, source plan, `prd.json`, `progress.txt`, current meta-status, and
  the current canonical board plus the full prior canonical rejection
  extraction before choosing this action.
- Refreshed `origin/main` and verified the source commit, branch/worktree
  identity, startup/bootstrap ancestry, and planning-main ancestry above.
- Inspected the complete bodies of the public replay/session/device contracts,
  generated and source Wire providers, strict service/runtime/media/directory
  implementation, provider session/replay composition, livehost request/run,
  live session start/replay/control/observation/lifecycle, legacy replay
  helpers, loop/audio input, clock, buffers, and embedding module.
- Ran the requested source-level causal navigation at the pinned revision with
  `rtk git grep` and `rtk git show`; these are recorded as source evidence, not
  as test passes.
- Preserved C13's exact historical positive/negative matrix and C13 review
  repairs without re-running the optional executable. The PRD explicitly
  permits omitting the optional one-replay-per-route observation when
  historical/source evidence is sufficient and prohibits audit-executor builds.
- Focused causal and accumulated review-regression tests passed without
  changing runtime source: from `agent-cli`,
  `rtk go test ./internal/services/internal/replay ./internal/services/replay ./internal/transport/cli -count=1`
  reported `Go test: 719 passed in 3 packages`; from `go-agent-runtime`,
  `rtk go test ./services/replay/...` reported `Go test: 43 passed in 3
  packages`; the selected replay/interruption/continuation controls in
  `go-agent-runtime/services/session/internal/live` reported `Go test: 14
  passed in 1 packages`; and the accumulated C13 controls
  `TestSessionRecordedPCMIntegrity` plus
  `TestSessionCommand_OpenAIRealtimeReplayPositiveMaxDurationPreservesCompletedArtifact`
  reported `Go test: 5 passed in 1 packages` from `agent-cli/test/integration`.
- After integrating fetched current main, the exact rejected integration
  subtest `TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio/test46/provider_burst`
  passed once in `12.38s` with the bounded `-timeout=90s` command recorded in
  the CI-rejection section below.
- No product executable build, provider invocation, device invocation,
  profiling run, broad regression suite, or runtime vertical replay was run by
  C14. There is no implementation diff whose causal behavior could be newly
  changed relative to fetched current main; the focused tests validate the
  pinned source behavior and the exact C13 review controls. The future
  extraction test inventory above remains proposed/unrun.

Before delivery, the candidate-level checks are:

```text
rtk git diff --check
rtk git diff --name-only origin/main...HEAD
rtk git status --short
rtk git merge-base --is-ancestor 8bdafc7f947a3a2c9856220abdc539437035bd21 HEAD
rtk git merge-base --is-ancestor c3bb663e118de9e73ea3eb211b381e8f86c4f480 HEAD
rtk git merge-base --is-ancestor f8e0863222da1bdbf296e2220fcbc081461cc877 HEAD
rtk proxy test -s docs/temp/projects/audio-runtime/c14-replay-boundary-audit/audit.md
```

The expected changed-path set is only:

```text
docs/temp/projects/audio-runtime/c14-replay-boundary-audit/audit.md
docs/temp/projects/audio-runtime/c14-replay-boundary-audit/canonical-board.json
docs/temp/projects/audio-runtime/c14-replay-boundary-audit/canonical-rejection-feedback.json
docs/temp/projects/audio-runtime/c14-replay-boundary-audit/canonical-board-after-ci-34309378555.json
docs/temp/projects/audio-runtime/c14-replay-boundary-audit/canonical-feedback-after-ci-34309378555.json
docs/temp/projects/audio-runtime/c14-replay-boundary-audit/ci-rejection-34309378555.json
docs/temp/projects/audio-runtime/c14-replay-boundary-audit/ci-rejection-34309378555-job102332669048.log.gz
```

Delivery record:

- Initial evidence commit: `8c3d34f4cc317c506e28d15b1fa8f8f05930790b`;
  focused-validation handoff commit: `d6ebbcaa010b34c286cc0b0a9b492df4333bf074`.
- Review-36 repair commit: `afd4015cb04972f068091bf80427b83a7ba8287d`.
- Final pre-push handoff ledger commit: `7fe1eabad755c6548e067cf092ba6335c61ab3e6`.
- Pushed-head record commit: `7184993fd76acec1e432179bc942ac5890cec392`
  (`docs: record C14 pushed head`).
- Pushed branch: `codex/audio-runtime-c14-replay-boundary-audit`.
- Pull request: [#406](https://github.com/portpowered/go-agent-harness/pull/406),
  base ref `main` at `c3bb663e118de9e73ea3eb211b381e8f86c4f480`, state OPEN at
  the historical PR read. The later fetched `origin/main` is
  `f8e0863222da1bdbf296e2220fcbc081461cc877`; that current-main advance is
  recorded separately from the pinned C14 source and does not change the
  historical C13 evidence. The post-push metadata check after
  `7184993fd76acec1e432179bc942ac5890cec392` reported that exact old head and
  no merge SHA. The later audit-only record commit
  `215973992d4f3a7f147afbd7fd4f4073aad86714` was pushed on the same branch.
- Review-39 repair commit `2fc38169` (`docs: repair C14 replay boundary audit`)
  was pushed after the latest finding. The subsequent handoff-ledger commit is
  on the same branch and PR; the PR body is updated with the exact latest head,
  finding map, and bounded validation evidence. No merge SHA exists and the new
  script-owned CI result has not been polled or claimed.
- Review-41 repair commit `887198b0d82950c5fedc55348d06e6204664bbd7`
  (`docs: repair C14 Wire and provider boundary audit`) is pushed on the same
  branch and PR. It updates the audit and the full C14 rejection-inbox
  extraction; PR #406 is OPEN at this exact head. The PR body was updated with
  the finding map and bounded validation evidence. No new CI result is being
  polled or claimed.
- Review-42 repair commit `0246e05fd705ed8547558f8bb4184fa8a70d75b4`
  (`docs: reconcile C14 clock boundary finding`) records the current limitation
  and exact future `Prepared.Clock` -> `agentloop.WithClock` wiring. It was
  superseded by the pushed checkpoint `d91bea8337a17aae9a1a88fed8491e533268465b`,
  which was the PR head rejected by Review-44 for stale evidence, not an
  unpushed current candidate. The Review-44 repair is the new evidence commit
  from that exact head; its final PR SHA is checked after commit/push and is
  the candidate handed to script CI.
- The prior script-owned gate on `d91bea83` had nine successful checks; no
  check result for the Review-44 repaired candidate was claimed green.
- The current same-task candidate is the unpushed merge/evidence checkpoint
  `8e5546fdab0702726de33724aa58245576f085db` plus the audit/evidence edits
  recorded in this delivery. It integrates fetched `origin/main` at
  `f8e0863222da1bdbf296e2220fcbc081461cc877`, preserves the pinned C14 source
  and required startup ancestry, and leaves only the owned evidence paths in
  `git diff origin/main...HEAD`. PR #406 remains the same open PR; no CI result
  exists for this new candidate.
- The next factory action is commit the audit/evidence checkpoint, push the
  same branch, update PR #406 against `main`, and submit it to the
  script-owned current-head CI gate. Independent review follows only after
  successful script CI.

After this current-CI evidence checkpoint, push this same branch and update
PR #406 against `main`, then return `ACCEPTED` to the script-owned current-head
CI gate.
`ACCEPTED` means submitted to CI; it does not mean CI is green. Do not poll
CI. If the script returns an exact rejection, retain this task, inspect the
full same-head logs/feedback, repair only the actionable issue within the C14
evidence lease, rerun the bounded evidence check, and resubmit the same task.
Independent review must follow successful script CI, and meta must later run
the fresh exact-artifact vertical probe and read its report plus canonical
outcome. No audit merge or historical C13 software evidence closes any of the
nine project gates.

## Evidence index

Owned C14 evidence:

- `audit.md` — this decision, call graphs, ownership map, extraction paths,
  parity controls, residuals, immutable gates, and handoff checks.
- `canonical-board.json` — raw `~default` board snapshot captured with
  `--all --max-results 500`.
- `canonical-rejection-feedback.json` — full untruncated feedback extracted
  from every task/review row with prior rejection feedback, including terminal
  rows.
- `canonical-board-after-ci-34309378555.json` and
  `canonical-feedback-after-ci-34309378555.json` — raw current-board and exact
  current task-row rejection snapshots after the latest CI return.
- `ci-rejection-34309378555.json` and
  `ci-rejection-34309378555-job102332669048.log.gz` — raw run metadata and the
  full failed integration-job log (compressed without changing its bytes).

Read-only predecessor evidence:

- `$FACTORY_ROOT/docs/temp/projects/audio-runtime/c13-probe-result-reconciliation/assessment.json`
  — C13 route matrix, 24 declared-artifact/root controls, two undeclared-trace
  residuals, and scoped validation outcome.
- `$FACTORY_ROOT/docs/temp/projects/audio-runtime/audio-runtime-c13-recorded-pcm-integrity-vertical-probe.json`
  — exact staged executable, fixture hashes, 18-wire/1-tool, 4,800-byte
  provider PCM, 3,200-byte rendered PCM, and 2,400-byte healthy interruption
  tail historical proof.
- `$FACTORY_ROOT/docs/temp/projects/audio-runtime/c13-merged-artifact/build.json`
  — source/build/fixture provenance.
- `$FACTORY_ROOT/docs/temp/projects/audio-runtime/c14-replay-boundary-audit/source-inspection.json`
  — parent source-inspection manifest; not overwritten by this branch.
- `factory/docs/{operating-policy.md,implementation-handoff.md,meta-planner-handoff.md}`
  and `factory/projects/audio-runtime/{request.md,acceptance.md,source-plan.md}`
  — governing policy and immutable scope.
