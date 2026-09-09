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
`audio-runtime-c14-replay-boundary-audit`; the live board has its task row
`work-task-34` in `init`/`PROCESSING`, its plan row `work-plan-33` is complete,
and the idea row is the same named Work. At the initial C14 evidence capture,
the task and review rows had no `_rejection_feedback`; that historical board
snapshot and the complete extracted rejection inbox are preserved in
`canonical-board.json` and `canonical-rejection-feedback.json`. The initial
extraction contains 12 rows from prior C11/C12/C13 work, with no C14 finding at
that snapshot. The later executor re-admission and review finding are recorded
in the current reconciliation below.

The admission command, run from the admitted FACTORY_ROOT, returned:

```text
rtk proxy python3 "$FACTORY_ROOT/factory/scripts/project-control.py" verify-work --type task --name audio-runtime-c14-replay-boundary-audit
{"status": "admitted", "project": "audio-runtime", "name": "audio-runtime-c14-replay-boundary-audit"}
```

The authoritative board was captured with the exact handoff command:

```text
rtk proxy you --server "$FACTORY_SERVER_URL" --json work list --session "~default" --max-results 500 --all
```

The raw board is `canonical-board.json`, SHA-256
`dd635d054164e644fc6c8d6a502394c597f3f49392f6c501b976be151b5ce7c7`; the
full extracted rejection inbox is `canonical-rejection-feedback.json`,
SHA-256 `09c5ead3b238e6f9685f0eff5fa1b6201c04690a38471eafeb2f21bdc4c6b672`.
The board was saved as raw JSON even though one historical feedback string
contains unescaped control characters; the extraction used a permissive JSON
reader solely to preserve that full feedback verbatim rather than clipping or
discarding it.

The task packet is `prd.json` at the worktree root. Its authority hashes are:

| authority | path | SHA-256 |
| --- | --- | --- |
| execution plan | `factory/projects/audio-runtime/source-plan.md` | `f715163fb20f46a18837d4a4d19ff6d880aaadf8dbf40acfff88a0a6c5800d37` |
| request | `factory/projects/audio-runtime/request.md` | `4d53be6795ea189d5ae3aac727a76ea5dc5ac3f07c3a5e6280a2bc1e9ddcfeb0` |
| immutable acceptance | `factory/projects/audio-runtime/acceptance.md` | `e08b64af98d5c6ded9deac36b7bc33d7af09c47de15e80821852f8e5553148d5` |

The worktree and branch checks were:

```text
worktree: /Users/abdifamily/.codex/worktrees/af44/go-agent-harness/.claude/worktrees/audio-runtime-c14-replay-boundary-audit
branch:   codex/audio-runtime-c14-replay-boundary-audit
HEAD:     c3bb663e118de9e73ea3eb211b381e8f86c4f480
origin/main after fetch: c3bb663e118de9e73ea3eb211b381e8f86c4f480
git diff origin/main...HEAD: empty before C14 evidence
prd.branchName: codex/audio-runtime-c14-replay-boundary-audit
```

`rtk git fetch origin main` was run in this isolated worktree. No merge or
reset was performed, and the running host checkout was not touched. The
required ancestry checks passed:

```text
rtk git cat-file -e c3bb663e118de9e73ea3eb211b381e8f86c4f480^{commit}: PASS
rtk git merge-base --is-ancestor 8bdafc7f947a3a2c9856220abdc539437035bd21 HEAD: PASS
rtk git merge-base --is-ancestor c3bb663e118de9e73ea3eb211b381e8f86c4f480 HEAD: PASS
```

The startup/bootstrap integration pin is
`8bdafc7f947a3a2c9856220abdc539437035bd21`. The original refactor baseline
is `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`. The inspected source and
fetched main are both `c3bb663e118de9e73ea3eb211b381e8f86c4f480`. The C13
accepted vertical is a read-only predecessor dependency; C11/task4 and its
scripts/evidence are disjoint and untouched.

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
       -> go-audio/pkg/recording.OpenReplay
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
       -> deriveInputActions
       -> coreRuntime.Run
            -> wait for SessionOpen
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
edges are `replay.go:waitReplayReady`, `observation.go:observeOpeningPolicies`,
and `interruption.go`/capture planning. Terminal cleanup is
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
recorded tool executor, a deterministic clock, an audio replay, and a runtime
factory. Moving only `service.go` would leave the external caller without the
actual `RuntimeFactory`/media/completion graph. Moving only the provider
factory would expose credentials/device/CLI composition. The cohesive unit is
the entire strict `Prepare`/`Run`/validation boundary.

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
    Audio      *recording.Replay
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
(`go-agent-loop/pkg/messages`, `go-audio/pkg/{clock,recording}`, and
`go-llm-gateway/pkg/{testing,transport}`); no CLI package, config, flags,
terminal, credential vault, device registry, or executable tool factory is
part of this public contract. The existing CLI-internal `Request`, `Prepared`,
`EvidenceScope`, `Runtime`, `RuntimeFactory`, and `Result` are the source shape
to migrate, with names made explicit because runtime `Service` already means
admission.

### Private implementation files

Move the business implementation as one cohesive package under the runtime
service boundary:

| current symbol/file | future exact path and owner | required behavior |
| --- | --- | --- |
| `internal/replay.Service`, `Dependencies`, `ClockFactory`, `Run`, `Prepare` | `go-agent-runtime/services/replay/internal/strict/service.go`, private `strict.Service` | Own bundle admission handoff, origin/deterministic clock, `recording.OpenReplay`, strict prepared state, `Run` then `ValidateComplete`. |
| `recording_directory.go:validateRecordingBundle`, `prepareTraceDirectory`, `resolveTraceDirectory` | `go-agent-runtime/services/replay/internal/strict/directory.go` | Keep root-manifest Lstat/nonregular/symlink rejection, absent-manifest legacy trace compatibility, trace path selection, and runtime admission dependency. |
| `readTimeline`, `timelineOrigin`, event decoding and `deriveEvidence` | `go-agent-runtime/services/replay/internal/strict/evidence.go` | Keep bounded JSONL parsing, exact sequence/elapsed checks, initial handshake/model/terminal validation, provider send/receive projection, and tool shape validation. |
| `trackingDialer`, `trackingConn`, replay state | `go-agent-runtime/services/replay/internal/strict/transport.go` | Keep one-connection, ordered message type/count/write/read validation and bounded divergence errors. |
| `recordedToolExecutor` and tool decoders | `go-agent-runtime/services/replay/internal/strict/tools.go` | Keep exact call ID/name/arguments, result matching, exact-once consumption, and unconsumed/missing-result failures. |
| `NewOpenAIRuntimeFactory`, `openAIRuntimeFactory`, `coreRuntime`, `deriveInputActions`, initial-update wrapper | `go-agent-runtime/services/replay/internal/strict/runtime.go` | Keep offline OpenAI gateway + AgentLoop construction, explicit input action boundaries, provider terminal counting, cancellation drain, and no live credentials. |
| `replayMediaInferencer`, virtual playback controller, inbound drain | `go-agent-runtime/services/replay/internal/strict/media.go` | Keep headless media claim needed for provider-owned truncate/interruption and explicitly report no device execution. |
| `NewReplayClockFactory` behavior | `go-agent-runtime/services/replay/internal/strict/clock.go` or a constructor in `strict/service.go` | Build one fresh `clock.Deterministic` from the trace origin per preparation; never fall back to wall time. |

The package may use `go-agent-loop/pkg/{agentloop,engine,messages}`,
`go-audio/pkg/{audio,clock,codec,recording}` and
`go-llm-gateway/pkg/{gateway,inference,providers/openai,testing,transport}`.
It must not import `agent-cli`, `agent-cli/internal`, provider credential
configuration, device backends, or the CLI tool/browser registry. It may call
the existing public replay admission Wire constructor to reuse manifest/path
validation, but it must not create a second project-specific admission rule.

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

1. Add `NewStrictService` to
   `go-agent-runtime/services/replay/wire/wire.go`, with generated output in
   `go-agent-runtime/services/replay/wire/wire_gen.go`. Its private provider
   set is `strict.New`, a deterministic clock factory, and the strict OpenAI
   runtime factory; the return type is the public `replay.StrictService`.
   Keep the existing `NewService` admission constructor unchanged. A test
   seam may pass a `StrictRuntimeFactory` through a second explicit constructor
   in the same Wire package, but the production constructor must remain
   deterministic and credential-free.
2. Replace the strict business construction in
   `agent-cli/internal/services/wire/replay.go` with a thin call to
   `runtimeReplayWire.NewStrictService` (or a CLI adapter whose only job is
   supplying the runtime Wire dependencies). It must no longer import
   `agent-cli/internal/services/internal/replay`.
3. Regenerate `agent-cli/internal/wire/wire_gen.go` from
   `agent-cli/internal/wire/wire.go`. The generated graph still constructs the
   CLI router, but the strict service dependency comes from runtime replay Wire.
   No hand-edited generated output is acceptable.
4. Keep `agent-cli/internal/transport/cli/session_replay.go` as presentation:
   it receives the public strict service, maps `Request`/result to Cobra output,
   and retains the success-only verification text. It must not parse timeline,
   validate manifests, construct gateways, execute tools, or select devices.
5. Keep `session_observability.go` and `livehost/request.go` on the existing
   runtime admission `replay.Service`. `session --replay` provider/live route
   is a different contract and must continue to use `InspectCapture` and
   `LiveReplayPlan`.
6. Keep `services/session/wire/live.go`, `agent-cli/internal/wire/live_service.go`,
   and the provider Wire graph as the live/session owner. Strict replay is
   headless and does not get inserted into `LiveDependencies`.

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
PCM evidence, while asserting `DeviceExecution == false`. It must also cover
the negative controls below through the public Wire constructor, not through a
private implementation. This is the proof needed to close EMBED/SERVICE for a
future slice; C14 does not claim it.

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
| Provider PCM and rendered PCM | C13 artifact-2 provider PCM is 4,800 bytes, SHA-256 `0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502`; rendered output is 3,200 bytes, SHA-256 `7d2d8221eb8ec0be3da4a3ed518e1e183aa56e4ac0140ca0cf761068555805` | **Historical software proof.** Future extraction must keep sample-rate conversion and byte hashes; rendered/file output is not physical consumption. |
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
   The codec implementation is centralized in go-audio, but callers still
   choose when to encode; this is a migration observation, not proof that one
   independently testable audio subsystem owns all formats/sample timing/DSP.
4. `go-agent-loop/pkg/engine/tick.go:10-31` injects a
   `clock.TimerSource` and waits through `clock.Wait`; the engine hot loop calls
   `Tick` then `waitForNextTick` in `engine.go:220-234`. `go-audio/pkg/clock/clock.go`
   separates `Source`, `TimerSource`, and `Scheduler`, and
   `clock.Deterministic` advances virtual elapsed time independently of logical
   ticks. Strict replay's future `ClockFactory` must inject this scheduler;
   neither route may silently substitute wall time for trace timing.
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

## Immutable acceptance criteria and later gates

All nine criteria remain `OPEN`. C14 is an audit/extraction decision and cannot
close a project rubric. The exact immutable rubrics and the scoped judgment
for this source revision are:

| ID | immutable rubric | C14 scoped judgment | named later gate |
| --- | --- | --- | --- |
| AUDIO | One independently testable audio subsystem owns packet parsing, formats, clocks, sample timing, DSP and buffer operations; the core loop uses buffers without direct device IO. | **OPEN.** Source shows memory-only loop ports and canonical go-audio buffer/codec/clock edges (`go-agent-loop/pkg/subsystems/audio/audio.go`, `go-audio/pkg/{audio,codec,clock}`), but replay callers and device/audio ownership are not a complete independent subsystem proof. | Canonical audio subsystem extraction plus independent clocks/codec/DSP/sample/buffer validation. |
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

The following bounded checks were performed without building the product
executable, using a provider or device, profiling, duplicating broad local CI,
or polling CI, as required by the C14 audit execution policy:

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
- No product executable build, provider invocation, device invocation,
  profiling run, broad regression suite, or runtime vertical replay was run by
  C14. There is no implementation diff whose causal behavior could be newly
  changed; the focused tests validate the pinned baseline and the exact C13
  review controls. The future extraction test inventory above remains
  proposed/unrun.

Before delivery, the candidate-level checks are:

```text
rtk git diff --check
rtk git diff --name-only origin/main...HEAD
rtk git status --short
rtk git merge-base --is-ancestor 8bdafc7f947a3a2c9856220abdc539437035bd21 HEAD
rtk git merge-base --is-ancestor c3bb663e118de9e73ea3eb211b381e8f86c4f480 HEAD
rtk proxy test -s docs/temp/projects/audio-runtime/c14-replay-boundary-audit/audit.md
```

The expected changed-path set is only:

```text
docs/temp/projects/audio-runtime/c14-replay-boundary-audit/audit.md
docs/temp/projects/audio-runtime/c14-replay-boundary-audit/canonical-board.json
docs/temp/projects/audio-runtime/c14-replay-boundary-audit/canonical-rejection-feedback.json
```

Delivery record:

- Initial evidence commit: `8c3d34f4cc317c506e28d15b1fa8f8f05930790b`;
  focused-validation handoff commit: `d6ebbcaa010b34c286cc0b0a9b492df4333bf074`.
- Review-36 repair commit: `afd4015cb04972f068091bf80427b83a7ba8287d`.
- Final pre-push handoff ledger commit: `7fe1eabad755c6548e067cf092ba6335c61ab3e6`.
- Pushed branch: `codex/audio-runtime-c14-replay-boundary-audit`.
- Pull request: [#406](https://github.com/portpowered/go-agent-harness/pull/406),
  base `main` at `c3bb663e118de9e73ea3eb211b381e8f86c4f480`, state OPEN, current
  head `7fe1eabad755c6548e067cf092ba6335c61ab3e6`. The pushed head has no merge
  SHA at this executor handoff.
- CI was not polled and no CI result is claimed. The next factory action is
  the script-owned current-head CI gate, followed by independent review if the
  gate succeeds.

After the review repair checkpoint, push this same branch and update its PR
against `main`, then return `ACCEPTED` to the script-owned current-head CI gate.
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
