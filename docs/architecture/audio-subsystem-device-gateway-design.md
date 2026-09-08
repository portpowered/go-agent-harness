# Audio subsystem, device gateway, and reproducible session diagnostics

Status: implementation and validation in progress. Design date: 2026-09-04; evidence updated 2026-09-05.

Follow-up: [Embeddable runtime and service-boundary refactor plan](embeddable-agent-runtime-refactor-plan.md) specifies extraction from the CLI, service-local `internal/` and `wire/` packages, and architecture/size/complexity enforcement. Its service-local layout supersedes the shared implementation layout here. That follow-up also records the current extraction checkpoint and remaining CLI ownership; older validation snapshots below are not a claim that the current aggregate checks pass.

This document is the decision and acceptance record for consolidating audio handling and device access. It is based on inspection of this checkout; it does not claim that the reported customer failure has been reproduced. This change first establishes ownership and evidence, then fixes regressions through those boundaries.

## 1. Outcome and scope

A developer should be able to open one session bundle, listen to each recorded boundary, locate the first missing or delayed sample range, and replay the harness with recorded provider events, device callbacks, and tool outcomes without credentials, hardware, or external side effects.

All audio payload parsing, DSP, framing, buffering, pacing, mixing and general audio operations belong to a new `go-audio` module. It also provides the canonical clock/scheduling package shared by session components. Device enumeration, selection, native callbacks, and remote device connections belong to an adjacent `go-device-gateway` module. A thin audio subsystem in `go-agent-loop` exchanges audio and control through buffers only; it never reads or writes a device. CLI commands consume thin service interfaces; private service implementations are composed through Go Wire.

The architectural consolidation and useful trace/replay evidence are required outcomes. Investigate all reported issue classes and implement the best supported fixes, but complete resolution of every customer symptom is not a release prerequisite. Report unresolved symptoms with the evidence, attempted reproduction and next diagnostic step; do not imply that adding observability fixes them.

Scope includes session, room, microphone, file, WebSocket, RTC, probes, and audio-bearing ask/chat paths. Consolidating only the primary session command would leave competing implementations. The user explicitly permits destructive restructuring provided overall functionality is mostly preserved. Internal APIs, directories, constructors and obsolete abstractions may be replaced outright. Preserve core user workflows, truthful terminal outcomes and disabled-recording behavior; document deliberate command/schema changes rather than retaining architectural debt solely for exact compatibility.

Non-goals: replacing the realtime client, changing models, rewriting unrelated tools or browser automation, building a support upload service, or promising deterministic reproduction of OpenAI internals or physical speaker behavior.

## 2. Current implementation and evidence

Paths below are relative to the repository root and identify the migration sources.

| Current owner | Observed responsibility | Destination |
| --- | --- | --- |
| `agent-cli/internal/audio` | PCM source/sink contracts, VAD, normalization, tones, device registries/backends, playback queue, device server, simulated devices | Split sample processing into `go-audio`, platform/device code into `go-device-gateway` |
| `agent-cli/internal/services/session_audio_in.go`, `session_audio_out.go`, `session_audio_format.go` | WAV/PCM parsing, format selection, audio pumping, output recording intertwined with session orchestration | Processing into audio; orchestration into agent-session/runtime |
| `agent-cli/internal/services/rtc_device*.go` | Device binding, resampling, feedback and session wrappers | Device access into device gateway; processing into audio; construction into runtime |
| `agent-cli/internal/services/session_room_audio_bundle*.go`, `session_self_play_evidence.go`, `session_duration_artifacts.go` | Additional WAV/PCM readers/writers and audio reconstruction | Shared audio implementation, with legacy bundle adapters |
| `agent-cli/internal/cli/device_probe_runtime.go`, `probe_replay.go`, `probe_customer_simulation.go` | PCM conversion, resampling and runtime logic in commands | Probe service calling the shared engine |
| `go-llm-gateway/pkg/wavio` | WAV and resampling implementations, including streaming resampling | Move implementation and tests into audio; update all imports and delete old package |
| `go-llm-gateway/pkg/providers/openai/session_media.go`, `session_events.go`, `stream.go`, `models.go` | Multiple audio base64/PCM decoding paths | Decode audio once through the shared audio API; retain provider envelopes here |
| `go-llm-gateway/pkg/transport/rtc/{codec,track_in,track_out,session_media}.go` | Opus, framing, resampling, media queues and playback identity | Codec/framing/media processing into audio; retain network transport in gateway |
| `go-llm-gateway/pkg/testing/session_*` | Protected capture, strict outbound replay, fixture validation | Preserve wire contract; add production-facing capture/replay entry points |
| `go-agent-loop/pkg/subsystems`, `pkg/participants/model_runner_session*.go` | Tick ordering, interruption, tool forwarding, continuation coordination | Preserve loop model; introduce explicit audio and response-control ports |
| `agent-cli/internal/services/session_runtime_{plan,live,openai,grok,rtc}.go` | Provider selection, factories, capture modes, device construction and presentation strings | Runtime service, with presentation moved to transports |

Existing device playback already has bounded buffering, overflow/underflow counters and `PlaybackRenderObserver`. Its observer documents **non-underflow samples consumed by a callback**. Extend this evidence to include callback timing, silence insertion, identities and offsets; do not replace it with an enqueue-only tap.

Existing room bundles and provider captures have integrity and strict replay contracts. Preserve those guarantees. `agent-cli/docs/session-timing-analysis.md` explicitly calls playback timing an estimate from provider bytes. A device receipt must be reported separately from that estimate.

`go-llm-gateway/pkg/providers/openai/session.go` currently implements `RequestResponse` by queueing a response-create message. The inspected method does not itself reserve an active-response slot. This is a relevant seam for the supplied error, not proof of the full race or its root cause.

## 3. Decisions

| ID | Decision | Reason / consequence |
| --- | --- | --- |
| D1 | One audio module owns payload codecs, WAV, PCM conversion, resampling, framing, VAD, mixing, normalization and playback processing | A sampling defect has one implementation and one package test suite |
| D2 | Device gateway owns hardware and remote device adapters | Libraries and headless tests never import CLI internals |
| D3 | Audio engine is independent of the agent loop; the loop imports a small adapter | Avoid cycles and avoid tying callbacks to model tick frequency |
| D4 | Record at actual boundaries, using sample lineage and monotonic time | Distinguish provider silence, local loss, queue delay and device starvation |
| D5 | One correlated session bundle combines media, wire, tools and scheduling evidence | Customer diagnosis does not require manually aligning unrelated logs |
| D6 | Replay external inputs and compare recomputed outputs | Playing a WAV alone cannot validate the harness behavior |
| D7 | One response-admission controller per provider session | Tool completion, VAD and user turns cannot independently initiate competing responses |
| D8 | Bounded queues and streaming evidence persistence | Recording and retained session history must not progressively slow playback |
| D9 | Replace internal boundaries directly; preserve behavior through contract tests | No internal compatibility wrappers, duplicate engines, or permanent legacy paths; separate structural changes from behavioral fixes where useful for review |
| D10 | Public service packages expose small contracts; implementations live under `services/internal`; Go Wire constructs their dependencies | Callers cannot depend on implementation details; one explicit generated dependency graph |
| D11 | The core loop exchanges audio, controls and receipts exclusively through bounded buffers | Device workers consume/produce independently of ticks; no device access hidden behind loop-injected interfaces |
| D12 | Consolidate clock sources, timers, deadlines and audio timing arithmetic as well as parsing/DSP | One injected time domain per session drives live timing and deterministic replay |

D1 partially supersedes ADR-0001's assignment of audio decode/RMS and audio plumbing to the LLM gateway. Provider protocol lifecycle stays there; shared audio processing moves out. Update that ADR with a supersession link when implementation lands, rather than silently contradicting it.

## 4. Package structure and dependency direction

```text
go-audio/                              # new go.work module
  pkg/audio/                           # Format, Packet, Frame, Engine, ports
  pkg/clock/                           # canonical clocks, timers and scheduling contracts
  internal/codec/                      # PCM, base64 audio, Opus implementation
  internal/dsp/                        # stateful resampling, gain, mixing, VAD
  internal/playback/                   # bounded queue, pacing, generations
  pkg/wavio/                           # canonical WAV reader/writer
  pkg/audiotest/                        # fixtures/assertions using the canonical fake clock

go-device-gateway/                     # new adjacent go.work module
  pkg/devices/                         # registry, capabilities, stream handles
  adapters/{darwin,linux,windows}/      # existing platform code/build tags
  adapters/{remote,virtual,simulated}/ # device-server client and test adapters
  server/                              # reusable device-server handler

go-agent-loop/
  pkg/subsystems/audio/                # thin Subsystem adapter over audio.Engine
  pkg/recording/                       # versioned bundle writer/reader, event index
  pkg/replay/                          # clock-driven replay coordinator and ports

go-llm-gateway/
  pkg/providers/...                   # provider envelope translation only
  pkg/transport/rtc/                  # signaling, RTP transport, RTCP statistics
  pkg/sessioncontrol/                 # serialized response-admission state machine
  pkg/capture/                        # production wire record/replay adapter
  pkg/testing/                        # fixture helpers, using production capture APIs

agent-cli/internal/
  app/bootstrap.go                    # calls generated service/transport composition
  transport/cli/core_router.go         # injected command registration only
  transport/cli/{session,runtime,devices,tools,recordings}/
  services/
    agentsession/{interface,request,result}.go
    agentruntime/{interface,spec,result}.go
    devices/{interface,request,result}.go
    tools/{interface,request,result}.go
    recordings/{interface,request,result}.go
    rooms/                            # thin room contracts
    probes/                           # thin diagnostics contracts
    internal/
      agentsession/{service,lifecycle,continuation}/
      agentruntime/{service,planning,assembly,workers}/
      devices/{service,selection}/
      tools/{service,execution}/
      recordings/{service,inspection,replay}/
      rooms/
      probes/
    wire/
      wire.go                         # wireinject entry points, provider sets, bindings
      wire_gen.go                     # checked-in generated constructor graph
```

Directory/package identifiers use normal Go spelling (`agentsession`), corresponding to the requested agent-session service boundary. Public contracts contain only use-case interfaces and boundary value types, not concrete engines, mutable runtime plans or dependency bags. The real Go `services/internal` boundary prevents CLI transports from importing implementation packages. Keep implementation subpackages only where they have a coherent responsibility. Service implementations integrate through other services' public contracts, never each other's private implementation. Enforce this latter rule with an import gate because Go's internal visibility alone allows sibling implementations to import one another.

Dependency arrows mean “imports”: device gateway → audio; agent loop → audio; LLM gateway → audio and existing agent-loop contracts; private CLI service implementations → these public modules and service contracts; CLI transports → service contracts. Audio imports neither loop, device gateway, provider SDKs nor CLI. The loop adapter receives buffer endpoints only, with no device-compatible read/write, registry or factory capability. Recording accepts typed observations and opaque wire payloads through interfaces and never imports the LLM gateway; the gateway capture adapter can therefore use it without a cycle.

`services/wire/wire.go` owns provider sets and interface-to-implementation bindings for all service dependencies, including planners, workers, clocks, buffers, recorders, tool executors and provider/device factories. The repository already uses `github.com/google/wire v0.7.0` in `agent-cli/internal/wire/wire.go`; migrate that graph rather than introducing a parallel DI mechanism. The bootstrap calls generated composition; any remaining app-level graph only composes transports around service interfaces. Public contracts import neither Wire nor private implementations. Services contain no Cobra objects, terminal printing, `os.Exit`, or implicit global live factories.

`agentruntime.Service.Build(ctx, spec)` returns an owned runtime interface with `Run` and idempotent `Close`; private planning/assembly state stays hidden. `agentsession.Service.Run(ctx, request, eventSink)` owns use-case policy and returns a typed result. Wire injects internal collaborators through constructors, rather than service methods constructing their own providers or resolving from a container. Plain values and private per-call state can be allocated normally. Dynamic session selection uses injected typed factories built by Wire; it cannot fall back to global production constructors. Provide session-scoped construction so concurrent sessions never share mutable clocks, queues, cancellation epochs or recorders. Constructors do not open microphones or dial providers merely to list CLI commands. Define cleanup ownership and roll back partially acquired resources in reverse order.

Production and hermetic composition use explicit provider sets for the same interfaces. Check in generated code, regenerate using the repository-pinned tool, and fail CI on generation drift or an invalid graph. Unit tests can call constructors with fakes directly; they need not invoke a runtime DI framework. CLI adapters translate typed results to existing text/JSON and exit codes.

## 5. Audio and device contracts

The public audio API carries an explicit `Format` (encoding, sample rate, channels, layout, sample width), `Packet` (payload plus codec/transport timing metadata), and `Frame` (PCM plus sample range and lineage). A sample range is measured in **frames per channel**; byte and interleaved sample counts have distinct names. Durations derive from frames/rate, never a global 16 kHz default.

Each packet/frame carries stream ID, stream epoch, per-stream sequence, response/item/content identity when applicable, and parent packet/range references. Epochs change on reconnect, format reset, or playback invalidation. Define ownership explicitly: immutable reference-counted buffers or a copied buffer when retained beyond a callback; no observer may retain a reused native slice.

Audio owns base64 audio payload validation, PCM alignment, codec decode/encode, packet-to-frame assembly, partial-frame flush, and stateful resampling. Provider JSON envelopes remain in provider adapters; RTP headers and packet arrival remain in transport adapters. Both call the same audio engine for the payload. Image base64 handling remains unrelated. Decode once and fan out the resulting frame to consumers; do not decode separately for diagnostics, playback and recording.

Resamplers persist phase/filter state across chunks, record input/output sample mapping, and flush once on normal end. Explicit cancellation discards the remaining tail with a reason. Legacy zero-padding is represented as inserted silence, never original captured samples. Format changes create a new segment and reset DSP state intentionally.

Device ports, available only to runtime/device workers outside the loop, expose capabilities, negotiated format, capture frames, playback submission, callback consumption receipts, interrupt/discard and drain. A receipt contains requested/consumed/zero-filled frames, device frame position, timestamp quality, and backend error. Exact platform APIs may differ; unsupported observations are marked unavailable, not fabricated. Keep physical callback consumption in the device adapter while the shared audio engine owns queue and pacing algorithms.

### Buffer-only loop boundary

```text
capture device → capture worker → capture ingress buffer
  → audio processing/subsystem → provider egress buffer → provider writer

provider reader → provider ingress buffer → audio processing/subsystem
  → playback egress buffer → playback worker → device callback

device receipts/errors → observation ingress buffer → loop observations
loop interrupt/drain commands → priority control buffer → media/device workers
```

These are typed, bounded, directional endpoints assembled by the runtime; “buffer” is not a wrapper whose read/write method calls a device synchronously. The core loop and its audio subsystem possess only producer/consumer endpoints, not device handles or callbacks into device I/O. File, virtual, remote and native workers implement the same boundary. Media processing may run on independent bounded workers owned by the audio subsystem runtime; buffer availability triggers work/tick notifications, so PCM throughput does not depend on reasoning ticks. Provider output cannot bypass the canonical processing/buffer route to reach a device.

Specify a single consumer per work queue; diagnostic fan-out uses explicit taps, not competing readers that steal playback frames. Endpoint contracts define admission, cancellation, close, overflow outcomes, buffer ownership and sequence preservation. Closing one endpoint cannot implicitly close a shared device. Device workers publish consumption/discard receipts; the loop does not wait for device drain during a tick. Priority control must remain deliverable when media buffers are full: use reserved capacity and coalesced monotonic interrupt epochs. Workers reject stale frames before submission, and receipts identify any already submitted audio that cannot be recalled.

### Consolidated clocks and operations

Move the reusable clock abstractions and implementations from `go-agent-loop/pkg/platform/clock` into dependency-free `go-audio/pkg/clock`, and migrate session-local duplicate timing helpers to it. This package has no DSP dependencies, so tools and session control can share it without importing the audio engine. Maintain one authoritative implementation of real and deterministic clocks, timer scheduling, cancellation-aware waits and clock-based deadline helpers. Reuse and extend the current `Source`/`TimerSource` behavior rather than adding another unrelated fake clock.

Wire provides one session time domain to audio pumps, VAD windows, pacing, buffer age accounting, recorder timestamps, loop scheduling, tool deadlines, response admission and replay. Application-owned deadlines use this scheduler; `context.WithTimeout`, `time.After`, sleeps and independent `time.Now` calls must not quietly introduce host time into deterministic paths. Preserve parent-context cancellation. Network/OS timeouts required by external libraries remain explicit adapter-level safety deadlines and are excluded from virtual-time claims.

Keep sample clocks distinct from host time: derive duration from sample-frame counts and rate using centralized arithmetic; persist hardware/RTP clock mappings and drift/uncertainty. A shared software clock cannot synchronize hardware or a remote process by itself. The replay scheduler advances to the next scheduled event/timer independently of loop ticks; tick count is an observation, not the sole driver of elapsed time. Wall time is a display/correlation anchor, never the basis for pacing.

The consolidation inventory must include parsing/validation, codec state, format negotiation checks, resampling, VAD, gain, mixing, tones, framing/tail handling, silence insertion, pacing, queue accounting, stream reset/drain/cancel, sample/time conversion and analysis/export. Each operation has one owner and shared tests. Gateway/device adapters only translate their external protocol or platform representation and call these operations.

Preserve current queue defaults during extraction (250 ms capacity target, 120/180 ms watermarks), then tune only against evidence. Every queue declares byte/frame/time capacity and an explicit overflow action. Emit dropped ranges and reason; no silent clipping. Separate control and media admission so audio backpressure cannot indefinitely block cancel, tool result, or close.

The engine uses bounded asynchronous pumps for media; `subsystems/audio.Execute` drains a bounded control-event batch and publishes loop observations. It never waits for a device read, network write, playback drain, or recording disk flush. Interruption is admitted into the priority control buffer independently of media pressure; the external playback worker applies epoch invalidation before admitting more audio and publishes a receipt. The loop never calls device cancellation directly. Preserve the current interrupt → tool-result-forwarder → coordinator ordering, with audio observation after interruption and before coordination where needed.

Shutdown closes input admission, resolves/cancels active work, flushes eligible DSP tails, drains playback under a deadline, closes resources, and finalizes evidence. Forced shutdown records unplayed sample ranges. Provider response completion and playback completion are separate states.

## 6. Unified evidence bundle

Proposed additional bundle contract (version 1 of this unified format, independent of existing wire format versions):

```text
manifest.json                  # versions, configuration, capabilities, integrity
timeline.jsonl                 # correlated media, control and tool events
wire/                          # existing protected provider capture + transport events
audio/<stream>/<segment>.pcm    # exact boundary samples, explicit format in manifest
audio/<stream>/index.jsonl      # chunks, offsets, sample lineage and clocks
tools/events.jsonl             # call/result/admission lifecycle; payload references
payloads/                      # optional tool inputs/results required for replay
reports/                       # derived summaries and WAV exports, not source evidence
```

Required audio boundaries when supported: `device.capture` (first application-visible capture, with hardware processing status), `provider.input` (encoded payload successfully written), `provider.output` (received payload and decoded samples), `playback.enqueue` (after mixing/gain/conversion), and `device.render` (callback-consumed samples including zero fill). Optional `device.loopback` verifies OS output or physical playback with its own clock and processing metadata. Preserve hold tones as labeled sources in the mixed-output lineage.

Writing to a socket proves local transport acceptance, not receipt inside OpenAI. A callback receipt proves device API consumption, not acoustic output from a speaker. Reports state the boundary and evidence quality. A capture after hardware voice processing cannot reconstruct the pre-processing microphone signal.

Every event includes session/participant/stream/epoch IDs, event sequence, producer sequence, monotonic offset, wall-clock anchor, kind, correlation IDs and relevant sample ranges. Sequence is observed order, not proof that unrelated concurrent events are causally ordered. Use parent references for causality. Cross-process device timestamps include clock ID, synchronization samples and uncertainty; do not subtract unrelated clocks as exact latency.

Record enqueue/dequeue/write-completion times, callbacks, underruns/overruns, inserted silence, resampling boundaries, VAD transitions, cancel/truncate actions and acknowledgements, stale-generation drops, disconnects and drain results. Keep requested, accepted, consumed and discarded counts separate.

Tool events record call identity/name, argument completion, dispatch, start, timeout/cancellation, result readiness, result send, continuation intent, admission and response association. Record relevant scheduler decisions, tick duration, queue age/depth, context size/token usage when available, history serialization duration and tool-advertisement size. Missing provider metrics remain unavailable. These separate growing local work from network/model latency in long sessions.

Use bounded asynchronous recording with preallocated callback handoff buffers. No JSON, hashing, disk I/O or blocking observer work in a native callback. A full evidence queue increments reserved loss counters and marks the bundle partial; recording failure must not cancel a healthy conversation. Persist incrementally, rotate large PCM segments, checksum finalized artifacts, checkpoint manifests, and recover readable prefixes after a crash. If even status persistence fails, return recording degradation through the typed runtime result.

Full capture is explicitly enabled through existing recording options/new unified bundle option; metadata-only mode cannot claim hermetic replay. Never include credentials or authorization headers. Customer audio and tool payloads remain private local artifacts, excluded from commits; synthetic fixtures replace sensitive content. Payload omission/redaction and missing ranges are declared. Lossy or incomplete bundles can be inspected, but cannot pass strict replay as complete evidence. This extends existing recording policy rather than silently enabling microphone recording.

Keep existing `.session.json` integrity and replay semantics where useful, and migrate room/session formats explicitly when the unified format replaces them. Prefer a bounded legacy reader or offline converter over parallel production writers. Validate original integrity before conversion, retain source provenance and declare unavailable observations. Existing files without device evidence are labeled provider-only. Update repository fixtures and consumers together; no silent reinterpretation and no two authoritative writers for the same samples.

## 7. Response admission and the reported failure

Audio consolidation alone will not fix “Conversation already has an active response.” Introduce one controller at the gateway session boundary that sees **every** request path: tool continuation, explicit user input, scheduled audio, raw response-create events and provider auto-response events.

Controller states are idle, create-pending, active(response ID), cancel-pending, and terminal. Reserve create-pending before queue insertion; successful queue insertion is not provider acknowledgement. Match created/done/error events by response/event identity. Definitive unsent failures release the reservation; ambiguous transport delivery requires reconciliation or explicit failure, not blind resend.

The loop supplies continuation intents keyed by call IDs and receives accepted/deferred/rejected outcomes. A tool timeout produces one tool result. While a response is active, hold continuation intent and release it only when eligible, coalescing compatible ready results without losing individual call attribution. Late tool completions are diagnosed and cannot produce a second result. Tool continuation completion requires its configured response terminal condition, not merely queue acceptance or any later audio delta.

Automatic provider response creation must not race locally initiated creation. Make ownership explicit in negotiated turn policy: for paths requiring local continuations, prefer provider VAD detection with client-owned response creation where supported. If auto-response mode is retained, reconcile server-created responses in the same controller; any residual remote race must have typed conflict recovery and a bounded timeout. A local mutex alone cannot prevent an unsolicited server response. Validate negotiated behavior in live tests before switching defaults.

Barge-in increments the playback epoch and clears old queued audio immediately. Record response cancel and conversation truncate independently; truncate uses the consumed cursor mapped back to provider samples, excluding hold tones and inserted silence. Hardware-buffered audio that cannot be revoked is recorded as such. A terminal event for an old response must never free a new response's slot. Failure recovery must not indiscriminately hide provider errors.

Reproduce the supplied topology synthetically: active audio response; `youtube_resume` times out after 20 seconds; user interruption; another response begins; timeout result arrives before that response ends. Assert one timeout result, no conflicting create, bounded interruption handling, correct response association, and explicit final playback/continuation outcome. The supplied transcript alone cannot establish the exact event interleaving; a future customer bundle supplies that evidence.

## 8. Replay and diagnosis

Provide three clearly labeled operations through the recordings service:

1. Inspect/export: verify artifacts, report gaps and boundary latency, export/listen to selected streams, including partial captures.
2. Strict replay: inject recorded capture input, provider events, device callback schedule and tool outcomes into the real engine/runtime using a virtual clock. Compare newly produced packets, sample ranges, playback output and control events. Fail on first divergence with expected/actual event, sample offset and correlation ID.
3. Scenario replay: change one timing/loss/format parameter or run corrected behavior against recorded external inputs. Report expected behavioral differences; never call this byte-identical replay of the original run.

All timers affecting behavior use injected clocks. Seed nondeterministic IDs and preserve causal ordering; do not rely on goroutine scheduling or wall-clock sleeps. Replay reconstructs from session start by default; seeking requires replaying the prefix, or a future explicit snapshot of codec/resampler/queue/control state. A midstream PCM file is not a complete state snapshot.

Hermetic constructors install failing live-network, credential, hardware and tool-execution adapters. Recorded tools validate name/arguments/call mapping and return recorded outcomes; replay never casts to a TV or reruns a shell command. RTP replay injects ordered packet arrivals, loss/reordering and callback schedules below the real media processor. Compare lossless PCM byte-for-byte; lossy codec results require pinned codec/build provenance or explicit tolerances. Replaying a corrected client against old provider output validates local behavior, not hypothetical provider reactions to changed requests.

Examples of diagnosis: provider output present but enqueue absent points before playback admission; enqueue present but consumed absent points at queue/device handling; consumed PCM with zero-filled gaps identifies callback starvation; uninterrupted consumed PCM with bad loopback points beyond the callback boundary. Missing evidence produces “unknown,” not a confident attribution.

## 9. Requirements and test traceability

| Requirement | User story / acceptance | Tests and owning package |
| --- | --- | --- |
| FR1 / US1 | As a maintainer, change packet parsing in one place; no production PCM/WAV/Opus/resampling implementation remains in CLI or provider adapters | `go-audio`: golden samples, malformed/oversized payloads, odd PCM bytes, channel/rate validation, WAV chunks, codec errors; import/AST ownership gate with reviewed test-fixture exceptions |
| FR2 / US2 | As a device integrator, implement one gateway contract and run without CLI | device gateway: migrate registry conformance, disappearance/open failures, callback receipts, remote disconnects, idempotent close; native platform smoke lanes |
| FR3 / US3 | As a library customer, inject audio into the tick loop without a device/provider | loop/audio: fake ports and virtual clock; blocked sinks never block ticks; cancellation and shutdown release all pumps |
| FR4 / US4 | As support, locate missing output samples and listen at each available boundary | recording/audio: exact lineage through gain/resample/mix; partial final frames; overflow/underrun/silence; callbacks after cancellation; byte/sample reconciliation |
| FR5 / US5 | As a customer, interrupt playback despite provider bursts or pending tools | audio + sessioncontrol: immediate epoch invalidation, stale frames rejected, consumed-cursor truncation, no deadlock under control/media pressure |
| FR6 / US6 | As support, distinguish long-session slowdown by stage | benchmarks/soak: 10/100/1000-turn fixed-size workload; queue age, tick cost, history cost, recording backlog, heap/goroutine retention; separate virtual-time and real scheduling tests |
| FR7 / US7 | As a customer, receive tool continuation without an active-response conflict | sessioncontrol + integration: timeout topology above, parallel tools, pending-create interval, duplicate/late done, canceled response, provider auto-create, send failure and disconnect |
| FR8 / US8 | As a developer, reproduce a captured issue offline | replay: record→strict replay using real engine and fake external boundaries; identical outputs/decisions; forbidden live seams; mismatch diagnostics; race schedule variants |
| FR9 / US9 | As a CLI consumer, keep commands stable while services become independent | transport tests: flag/request mapping, stdout PCM isolation, result/exit rendering; direct service tests with no terminal and no global factories |
| FR10 / US10 | As support, know whether a bundle is complete | disk-full/slow writer/crash prefix, checksum corruption, absent sidecars, evidence overflow, redacted payloads, legacy loaders; incomplete replay rejected |
| FR11 / US11 | As a service consumer, depend only on a thin interface and obtain fully injected implementations | Wire generation/build check, generated-code drift check, direct constructor tests, import gates for public/private/service boundaries, two simultaneous sessions proving scope isolation, partial-construction cleanup |
| FR12 / US12 | As a loop integrator, exchange audio solely through buffers | Import/API gate forbids device handles and read/write capabilities in loop/subsystem; stop the device worker and verify only bounded buffers change while ticks continue; restart and verify exact consumption receipts; full media queue cannot starve interrupt/control |
| FR13 / US13 | As a replay author, advance one clock to drive every application-owned timer | Shared-clock identity through Wire; virtual-time tests covering tool timeout, VAD, pacing, drain, response deadline and replay together; clock advances without ticks; no host-time sleeps; static gate for direct time calls outside designated adapters/clock implementation |

Resampling tests require the same input split into arbitrary chunk boundaries to match continuous processing, including 16/24/48 kHz paths and non-integer ratios. Test partial tails, multiple channels, discontinuities and drift over a long stream. Fuzz packet/WAV parsers with bounded allocations and no panics.

Sample accounting asserts, per epoch, admitted source frames = consumed source frames + queued source frames + explicitly discarded source frames, with a separate ledger for resampled mappings, generated tones and zero fill. Completion must not discard a valid tail merely because the provider ended its response.

Preserve existing device playback adversarial, microphone overflow, self-hearing, audio-in end-of-turn, replay sample-rate, room record/replay, duplex overlap and tool lifecycle suites. Move assertions with their owner; retain a small number of compiled CLI integration proofs. Each implementation slice runs its module tests, appropriate race tests, lint/build checks and existing coverage/fixture gates. New modules must be added to workspace, CI module enumeration and coverage configuration.

Proposed performance acceptance budgets, to be baselined in phase 0: no unbounded growth in media queues, recording queues or per-turn audio state; no retained PCM proportional to total session duration after drain; terminal response/call state retired; controlled fixed-context 1000-turn p95 local audio processing and interruption delay within 20% of the 100-turn baseline on the same runner. Virtual callback tests require silence/stale-audio accounting and stop by the next eligible callback after invalidation; real hardware latency is reported against negotiated callback/buffer latency. Full conversation history may legitimately grow, but its cost is measured separately and cannot execute on an audio callback path.

## 10. Live validation

The user has authorized the local test credential at `~/.you-agent-factory/secrets/OPENAPI_API_KEY`. During implementation, load it directly into the child process environment without printing it, embedding it in argv, committing it, or recording headers. No key is needed for this design review.

Reuse the existing `TestGPTRealtime21BinaryAudioAndToolRoundTrip` E2E entry point described in `agent-cli/docs/session-timing-analysis.md`, gated by `OPENAI_REALTIME_21_LIVE=1` and `AGENT_MODEL__OPENAI__API_KEY`. Inspect its current model/config and deadlines before execution; record the actual model returned and build provenance. This is the repository's existing test target, not a claim about current model availability.

Extend live evidence to cover spoken input/output with the unified bundle, tool continuation, barge-in, and a bounded multi-turn session. Run a separate device loopback/native test where supported: WAV egress alone does not prove physical output. Verify capture completeness and replay the resulting supported topology offline. Use broad provider latency ceilings and exact local sample invariants. When live nondeterminism reveals a failure, add a sanitized deterministic regression before changing behavior.

Live tests are supplemental and opt-in; ordinary CI remains offline. No live API or hardware tests were run for this design-only change.

## 11. Implementation sequence and exit gates

| Phase | Deliverable | Exit gate |
| --- | --- | --- |
| 0 | Inventory every production audio/timing path, baseline existing tests/performance, attempt synthetic active-response reproduction | Coverage map names an owner for every parser, operation, clock/timer, pump, callback, response-create source and recorder |
| 1 | Introduce `go-audio`, explicit types and canonical clocks/codecs/WAV/DSP/operations; move implementation, update callers and delete old processing packages | Golden/streaming/fuzz/shared-clock tests pass; no alternate production processing or session clock implementation |
| 2 | Extract `go-device-gateway`, existing backends/server and receipt instrumentation | Conformance and virtual callback tests pass; device code has no CLI import |
| 3 | Introduce engine, buffer-only tick boundary and external device/provider workers; migrate all media routes | Existing duplex/room/audio regressions pass; blocked devices never block ticks; control delivery, bounded pumps and final-tail handling verified |
| 4 | Add unified recording and strict/scenario replay, retaining legacy capture adapters | Generated bundle round-trips hermetically; incomplete/corrupt evidence fails honestly |
| 5 | Consolidate response admission and attempt evidence-backed playback/barge-in fixes | Controller invariants and reproduced regressions pass under race detector; unresolved customer symptoms documented with evidence and next steps |
| 6 | Split thin service contracts from `services/internal` implementations; compose all dependencies in `services/wire/wire.go` and inject CLI router | Direct service tests, Wire generation and import gates pass; commands have no audio parsing, pumps or business construction |
| 7 | Migrate probes/ask/chat/room stragglers, delete obsolete packages, run soak/native/live validation | Ownership gate, workflow/fixture migration gates, performance budget and live evidence complete |

Use cohesive changes that can update all callers together; a large move is acceptable when it eliminates the old boundary. Roll back with version control, not a second production implementation. Delete obsolete wrappers, constructor variants and superseded tests that only assert removed implementation details. Preserve or rewrite behavioral regressions at their new owner. Update package docs and this decision record alongside any deliberate contract change.

## 12. Definition of done and open design constraints

Done means a single audio implementation handles production parsing, DSP and general audio operations with consolidated clocks/scheduling; device abstractions are independently importable from the adjacent gateway; the loop accepts an isolated, independently tested audio subsystem with exclusively buffered I/O; thin service contracts hide private implementations composed through Go Wire; CLI transports call these services; and one complete bundle can diagnose and hermetically replay the local audio/control/tool timeline.

Additionally, investigate each of the four reported issue classes, add deterministic regressions wherever reproduced, and attempt fixes without compromising the required architectural work. Complete resolution of all four is best effort, as explicitly accepted by the user. Every relevant loss/latency boundary is either observed or explicitly unavailable, legacy captures remain usable directly or through declared migration, and the authorized realtime validation has recorded outcomes rather than assumed success. Delivery lists remaining issues and reproduction limits separately from completed consolidation; a failing known regression is not reported as passing.

Implementation must resolve backend timestamp/loopback availability, the exact current response ownership paths, and measured queue/recording budgets during phase 0. These are bounded engineering investigations, not reasons to delay the proposed structure. Any unavailable physical evidence limits the claim made by the report; it does not get substituted with a provider-derived estimate.

## 13. Unreal Engine comparison and high-level closure

This comparison uses Epic's public architecture documentation, not an audit of Unreal source. The resulting harness requirements are our engineering decisions, not claims that the two runtimes are equivalent.

Unreal separates audio control, DSP rendering, and lightweight hardware callbacks. Its mixer has a common implementation over a platform layer and prepares buffers ahead of device consumption. That supports our separation of loop control, independent audio workers and device adapters. [Epic: Audio Mixer Overview](https://dev.epicgames.com/documentation/en-us/unreal-engine/audio-mixer-overview-in-unreal-engine).

Unreal subsystems have managed lifetimes tied to their owning engine objects, and explicit initialization dependencies. Our equivalent needs runtime lifecycle ownership as well as constructor DI. [Epic: Programming Subsystems](https://dev.epicgames.com/documentation/en-us/unreal-engine/programming-subsystems-in-unreal-engine), [Epic: InitializeDependency](https://dev.epicgames.com/documentation/unreal-engine/API/Runtime/Engine/FSubsystemCollectionBase/InitializeDependency).

Quartz schedules audio independently of variable game-thread timing, including positions within an audio buffer. A shared timestamp source alone is therefore insufficient for our scheduling contract. [Epic: Quartz Overview](https://dev.epicgames.com/documentation/unreal-engine/overview-of-quartz-in-unreal-engine?lang=en-US).

Unreal audio buses combine sources into explicit signal paths. Our smaller equivalent is a declared routing plan with named sources and recording taps. [Epic: Audio Bus Overview](https://dev.epicgames.com/documentation/en-us/unreal-engine/audio-bus-overview).

### Required refinements before implementation is considered architecturally complete

| ID | Gap in the earlier design | Required contract and verification |
| --- | --- | --- |
| UE1 | Wire defines construction, but not asynchronous readiness and lifetime | Declare process scope (immutable factories, discovery), session scope (controller, clock domain, workers, recorder), stream scope (codec/DSP state), and device-lease scope (opened backend). The private runtime supervisor owns prepare → start → ready → quiesce → drain/abort → close, dependency order and reverse cleanup. No input admission before required workers are ready. Test partial startup, double close, shutdown during startup and concurrent sessions. |
| UE2 | Bounded asynchronous workers alone do not define a render deadline | Playback preparation follows device consumption and low-water demand; provider arrival fills a separate bounded staging queue. Capture follows device production. Neither is paced by loop ticks or provider bursts. Declare render quantum, target lookahead, total queued milliseconds across every stage and underflow policy. Heavy decoding happens ahead of rendering; callbacks only consume prepared data. Test stalls independently in provider, loop, decoder and recorder, verifying attribution and bounded end-to-end latency. |
| UE3 | Queue admission can be mistaken for a command taking effect | Controls carry command ID, stream epoch and optional effective sample position. Workers emit accepted/applied/rejected receipts with actual sample position. Interrupt is immediate at the next controllable boundary; timed changes define late-arrival behavior. Drain/close uses an asynchronous applied-command watermark before resource release; never wait on it inside a tick or callback. Test delayed/duplicate receipts, saturated media queues and close racing an interrupt. |
| UE4 | Device loss is observable but recovery ownership is underspecified | Device worker reports loss/suspend/format change; runtime supervisor owns policy: fail, pause, or reopen the explicitly selected device. Default is no silent switch to another device. Reopen negotiates format, increments epoch, resets affected DSP and establishes new clock mapping; old buffers/callbacks cannot enter the new stream. Test unplug, recover, format change and loss during drain. Backends can report unsupported recovery without pretending success. |
| UE5 | Mixing is centralized, but the topology and configuration owner need naming | Runtime assembles an immutable, versioned routing plan: microphone/provider input, assistant speech/tones/room mix, playback, optional echo-reference and diagnostic taps. Audio owns execution. Changes apply at declared frame boundaries and record plan version. Prevent accidental self-routing and double resampling. Test routing identity, source-isolated interruption, tap fidelity and plan changes during playback. A fixed typed plan is sufficient; a general graph editor is not required. |

These refinements fit the existing modules. They do not require another service for each row. UE1/UE4 belong to private runtime lifecycle code; UE2/UE3 to audio buffer/worker contracts with device receipts; UE5 to private runtime planning and the shared audio engine. Add these checks to phases 2–4 and 6 of the implementation sequence.

For UE2, moving work into goroutines is not proof of callback safety or deadline reliability. Specify bounded per-quantum work, preallocated callback buffers, and no callback locks shared with network/disk writers; measure allocation, scheduling delay and render-deadline misses. Do not promise hard realtime behavior from ordinary Go scheduling. Platform-specific callback bridges remain device-gateway internals. Observe cumulative queue latency: several individually bounded buffers can still create an unacceptable total delay.

For UE3, command application and device consumption remain separate evidence. A stop-applied receipt cannot prove already submitted hardware samples were inaudible. Keep software monotonic scheduling, sample-frame scheduling and hardware/RTP clock mapping distinct within the consolidated timing implementation.

### What remains appropriate to defer

Detailed subsystem state decomposition, snapshots for fast replay seeking, a dynamic DSP graph, generalized task scheduling, a visual debugger, sophisticated device failover and adaptive buffer tuning can follow later. Define state ownership now (one worker owns mutable stream state; other components use messages or snapshots), without requiring the full future state model in this migration.

Do not copy Unreal's object lookup patterns as a service locator, its reflection/garbage-collection object model, or its game-specific sound asset/spatialization systems. Use the lifetime and execution-boundary lessons with the requested explicit Go Wire composition. Our provider/tool replay requirements also need their own contracts; similarity to a game engine does not establish hermetic replay.

Assessment: the proposed package structure is sufficient. With UE1–UE5 explicit, the high-level design is coherent enough to implement without another architectural expansion. It is still a proposed design; cleanliness of the resulting implementation must be demonstrated by ownership, lifecycle, buffer and replay tests.

## 14. Implementation and validation evidence

The decisions and exit gates above remain authoritative. This section describes
implemented behavior; proposed directories in section 4 are not an assertion
that every service has already moved.

| Area | Implemented behavior | Validation and limits |
| --- | --- | --- |
| Audio ownership | `go-audio` owns audio payload codecs, WAV, continuous DSP/framing, clocks, playback queues, control ports, recording and media replay. Import tests reject CLI audio parsing and reverse module dependencies. | Ownership audit and standalone audio reverse-dependency tests pass. |
| Device boundary | `go-device-gateway/pkg/devices` owns platform/remote devices; `pkg/runtime` owns capture/playback workers. Core ticks access bounded memory ports and atomic cached snapshots. | Native macOS operation, bounded cleanup, and focused worker race checks pass; other platforms have compilation coverage. |
| Service composition | CLI lives under `internal/transport/cli`; generated Wire injects device, tool, session and runtime contracts. Session requests no longer carry runtime implementation dependencies. | Session, room and self-play implementations live in `services/internal/agentruntime`; probe metrics use an injected collector. Generated Wire and constructor tests pass. |
| Trace | Directory recording automatically includes audio, tools/control and completed provider wire operations on the shared clock. Render taps retain the complete callback, including underflow silence. Unsupported taps emit explicit unavailable evidence. | Recorded-session core replay, CLI command tests, and full suite checks pass. |
| Bounded recording | Directory events stream through a 256-event/16 MiB spool with one additional in-flight event. Trace admission also has a retained-byte budget. Overflow marks incomplete evidence; finalization drains accepted work. | Focused race and retention checks pass. Explicit legacy provider capture and conversation metadata retention remain separate memory concerns. |
| Response control | Bounded FIFO response admission coordinates default-conversation responses, tool continuations and cancellation generations. Admission and actual wire writes are separate events. | Tool replacement, interruption ordering, fresh-turn preservation, cancellation and live continuation checks pass. Deferred audio response requests are coalesced only when the tool-result continuation covers their already-committed input. |
| Offline replay | Validates audio hashes and wire/tool evidence, with an injected deterministic clock and strict recorded transport/tool execution. Scope distinguishes protocol/tool verification, recorded PCM/render evidence and device execution. | Production replay constructs the actual core loop and recorded transport/tools; `session replay` calls the public service. Recorded-session-to-core replay and prepared-factory stress each pass 20 repeated race runs; cancellation/overlap shapes outside the supported replay subset are rejected explicitly. |

### Important behavior and evidence distinctions

- Continuous resampling retains phase across packets, flushes exact normal tails,
  and resets on interruption/identity changes. Device capture emits available
  output without waiting for a full provider quantum; finite input preserves
  framing. Reference tests compare the full continuous waveform, including
  legitimate interior zeros.
- Playback snapshots cannot call remote device HTTP stats or wait for device
  worker locks. Workers refresh atomic snapshots; detailed freshness/state
  quality remains later work. A regression holds both worker locks while a
  loop-facing snapshot completes.
- Render recordings include callback zero-fill, preserving audible gaps in
  playback evidence. Source-consumption counters exclude that inserted silence.
  A stop-applied receipt still cannot establish that hardware-submitted audio
  was inaudible.
- Wire observations occur after the underlying transport read/write returns.
  A successful write is local transport evidence, not a remote processing
  acknowledgement. The recorder stores payloads without connection headers.
- Failed sessions retain trace staging at a reported path. Successful directory
  publication attaches the trace without overwriting an existing bundle.
- Offline media playback is not a rerun of hardware scheduling. Protocol/tool
  replay must not claim physical device execution or reproduce model internals.

- Room hold-tone DSP uses the injected clock. Session teardown preserves the
  canonical quiet-period timer and adds an explicit independent host safety bound
  when a stopped deterministic clock would otherwise strand resources. External
  device/network I/O safety bounds are distinct from media scheduling.
- Public session audio and control helpers share one bounded FIFO. Admission
  returns immediately with an explicit full-queue error; a later audio packet
  cannot overtake its preceding end-of-turn marker. Legacy direct channel
  writers do not receive this cross-channel ordering guarantee.
- Input admission and provider-session acceptance have separate accounting.
  Client commit evidence accumulates only accepted provider-bound PCM, so an
  early admission from the next turn cannot contaminate the preceding commit.
  Rejected sends are excluded. Completed wire-write evidence remains a separate
  transport tap; session acceptance alone is not a remote acknowledgement.
- Legacy session-capture replay validates client messages with a separate
  cursor from chronological server publication. At most 64 client admissions
  may be pending; server publication applies backpressure without dropping
  records or blocking the caller from subsequently draining its receive buffer.
  Payload/order mismatches remain synchronous failures. This scheduling fix
  does not expand the supported canonical offline-replay subset.
- Fatal provider-bound audio sends publish a terminal delta with the original
  error before the model runner exits. The outer agent loop therefore observes
  failure without waiting for its parent deadline; prior output remains marked
  partial when applicable.
- Tool-constructor diagnostics go to stderr. The shipped duplex regression
  asserts exact PCM-only stdout and cancellation ordering, preventing setup text
  from corrupting audio or prematurely releasing an output-gated customer turn.

### Validation already observed

- Focused normal/race checks have passed for continuous DSP, audio/control ports,
  recording budgets/spooling, provider response ordering/cancellation, and loop
  delta publication/cancellation barriers.
- The CLI transport suite and generated application wiring tests pass after
  private runtime extraction and explicit clock/factory composition. Shared
  library normal tests and coverage runs pass across all four libraries.
  The complete five-module coverage run passes, including the private runtime
  and integration suites; all 112 package registrations and floors pass.
- The six-crossing V8 duplex regression passes 20 single-CPU runs and ten
  normal runs with exact per-turn PCM attribution. Regression checks also cover
  bounded session ingress, replay publication beyond receive-buffer capacity,
  immediate replay divergence, and fatal audio errors waking the outer loop.
- Natural-close, device-WAV and serial-tool process checks passed against exact
  continuous-resampling references. A 61-second input-history case passed with
  explicit capture selection and a lookahead callback for its open stream.
- Live `gpt-realtime-2.1` audio/tool roundtrips passed with exact input/output PCM
  comparisons and one tool continuation. A directory-recording run measured
  452 ms to first audio and 603 ms from tool result to continuation audio.
  This rerun also passed assertions for completed wire send/receive events.
  These single-run timings are not percentile guarantees.
- A native macOS roundtrip passed with the built-in microphone/speaker, capturing
  15 frames with nonzero signal energy. This validates backend operation, not
  subjective audibility or reproduction of every customer symptom.
- Standalone checkout builds resolve adjacent modules without `go.work`.
  Linux/Windows shared-module cross-builds passed with CGO disabled. These are
  coordinated checkout and compilation checks, not independent published module
  release validation or native execution on those platforms.
- Coverage registration includes all five workspace modules and the new service
  packages. Generated Wire drift, formatting, vet, lint, static analysis, and builds pass.

Remaining customer symptoms, unsupported evidence and deferred Unreal-inspired
state/recovery work must be reported explicitly rather than inferred from module
consolidation. These checks do not establish hardware scheduling equivalence or
a universal fix for long-conversation latency.
