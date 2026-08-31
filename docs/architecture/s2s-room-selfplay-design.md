# Two-agent self-play validation harness — design

## Grounding: what already exists

- `agent-cli/test/integration/session_duplex_overlap_test.go` (s2s-v8) already
  builds ~70% of the mechanical plumbing needed: a `v8PCMBridge`/`v8PCMWriter`/
  `v8PCMReader` pattern that cross-wires one CLI's raw `--audio-out -` stream
  into a peer CLI's raw `--audio-in -` stream via in-process Go channels, plus
  per-side recording views that snapshot every crossing to JSON+WAV artifacts.
  **But** it is built for CI hermeticity, not live self-play: both sides run
  `--replay <scripted-capture>` (not live), share a `*clock.Deterministic` via
  `wire.InitializeMockAgentCLIWithPorts(wire.NewPortSwap(wire.PortClock, ...))`,
  and the "conversation" content is a single canned instruction+response baked
  into the replay fixture ahead of time — there is no real inference. It is a
  transport/timing proof, not two reasoning agents talking. Reuse its bridge
  *pattern*, not its replay/clock machinery.
- `agent-cli/internal/services/session_options.go`'s `SessionRunOptions`
  already carries every seam a self-play driver needs, confirmed by reading
  the struct directly (not the doc comments alone):
  - `Diagnostics SessionDiagnosticSink` (line ~162) — wired to
    `session_diagnostics.go`, which emits one canonical record per completed
    turn (`session_turn_completed`: per-turn input/output byte accounting),
    per unexecutable tool call (`session_tool_call_unexecutable`), per
    terminal failure (`session_failure`), and one final metrics record
    (`session_metrics`: token usage). This is a **documented, stable operator
    contract** (see `docs/architecture/s2s-session-diagnostic-contract.md`) —
    not something to invent.
  - `StreamObserver SessionStreamObserver` (line ~167) — a `func(messages.StreamMessage)`
    called for *every* stream delta the session loop consumes, including
    normalized tool-result deltas. This is the raw execution-loop trace.
  - `ToolExecutor messages.ToolExecutor` + `ToolDefinitions []messages.ToolDefinition`
    (lines ~132-140) — **already both present on this struct as of this
    reading**, with a comment stating `ToolDefinitions` is "the config-filtered
    tool surface advertised to the session provider and the duplex agent
    loop." This is further along than the two in-flight bug-fix worktrees
    suggested when this task was filed — re-verify current merge state before
    building on it, but the field-level plumbing for advertising real tools
    live already exists in the struct today.
  - `AudioInputs []ScheduledAudioInput` + `WaitForClose bool` — used by the
    live-broken `--record-dir`/`--audio-in-turn` path (separate in-flight bug,
    not blocking here since self-play needs continuous streamed audio, not
    scheduled finite files).
- `agent-cli/internal/cli/session.go`'s real CLI flags: `--audio-in -` reads
  raw PCM16 from stdin incrementally; `--audio-out -` writes raw PCM16 to
  stdout. `--system-prompt` and `--prompt` already exist for persona/seed text
  — no new CLI surface is needed for personas.
- Live VAD is server-side by default (confirmed empirically in this session's
  own live audio-in probe: a single `--audio-in <file>` against a live OpenAI
  Realtime connection produced a correct turn boundary and response with zero
  client-side commit signaling). This matters architecturally — see below.

## 1. Wiring recommendation: an in-process Go driver, not two OS processes

Two options were considered:

- **Two full OS subprocesses cross-wired via named pipes (FIFOs).** Maximally
  "real" (exact customer binary, zero new code in the hot path). Rejected as
  the primary mechanism because `Diagnostics`/`StreamObserver` are Go-level
  seams on `SessionRunOptions` with no CLI flag exposing them — an OS-process
  design would need a new `--diagnostics-out <file>` flag added to `session.go`
  before it could capture "the execution loop," and even then would be
  reduced to parsing structured log lines rather than consuming the typed
  `SessionDiagnosticRecord`/`messages.StreamMessage` values directly.
- **One small Go driver program that composes N `SessionRunOptions` directly
  (recommended).** Reuses v8's `io.Pipe`-based bridge pattern (drop the
  deterministic-clock and replay-capture parts; keep the writer/reader/view
  types) to cross-wire **live** sessions' raw PCM, and wires real
  `Diagnostics`/`StreamObserver` sinks per side that write JSONL directly —
  no new CLI flag needed, no log-scraping. This still drives the *same*
  `services.RunSessionWithInstructionsAndAudioInputAndOutputAndTextSeedAndMaxDuration`
  (`agent-cli/internal/services/session_audio_in.go:152`) and duplex-loop code
  a real customer's `agent session` invocation uses — it is not a mock, it
  bypasses only the `cobra`/flag-parsing layer, not the session runtime.

No shared clock is needed for the live case (unlike v8): real WebSocket
timing and server-side VAD naturally pace both directions, so the bridge can
drop v8's `logicalClock`/tick-window machinery entirely and just forward
frames as they arrive.

## 2. Distinct personas

Trivial with existing flags — no new capability required:
- Agent A: `--system-prompt "You are a customer who wants <goal>. Speak naturally, ask follow-up questions, and end the call once satisfied."` `--prompt` seeds the opening line.
- Agent B: `--system-prompt "You are a support assistant with tools. Use them when needed and speak your results back to the customer."`, real `ToolExecutor`/`ToolDefinitions` from `agent-cli/internal/tools/registry.go` wired in (the exec tool alone is enough for a first pass — real shell commands with a real, observable side effect the "customer" can ask about).

Vary the goal/persona pair per run (config-driven, not hardcoded) so repeated
self-play runs build a corpus rather than replaying the same conversation.

## 3. Execution-loop capture — concrete seam, file:line

- `opts.Diagnostics = <jsonlSink>` where `jsonlSink.RecordSessionDiagnostic(r SessionDiagnosticRecord)` appends `{event, fields}` as one JSON line per record — this alone gives per-turn byte accounting, tool-call outcomes, and the terminal failure/success record, per agent, for free (`session_diagnostics.go:88`).
- `opts.StreamObserver = <jsonlSink>` for the raw `messages.StreamMessage` delta stream — gives full fidelity (every delta, in order) beyond the diagnostic summary, per agent (`session_options.go:167`, `session_diagnostics.go:96`).
- Together these two seams **are** "the individual agent's execution loop" — no new instrumentation needs to be built, only wired and written to disk (and now, per §10, also fanned out live). This is a materially smaller lift than it might first sound.

## 4. Output artifacts and replay-fixture path

Per run, per participant:
- `agent-{id}.wav` — that agent's own emitted audio (mirrors v8's `writeV8ViewArtifacts` WAV output).
- `agent-{id}.diagnostics.jsonl` — `SessionDiagnosticRecord` stream.
- `agent-{id}.deltas.jsonl` — raw `messages.StreamMessage` stream.
- One joined `run-manifest.json` — persona/goal pair per participant, start/end wall-clock, turn counts per side, pass/fail if an assertion layer is added later.

This does **not** directly become a `--replay` fixture in the existing
`gwtesting.SessionCapture` format (that format captures one side's
provider-facing wire protocol, not a cross-agent PCM/diagnostic bundle) — a
follow-up conversion step would need to translate one agent's *observed*
inbound audio into a synthetic capture the existing v3a/v3c/v8 test
conventions expect, if the goal is turning a self-play run into a committed
CI fixture. Treat that as a distinct, later phase; do not conflate "record a
self-play run" with "produce a replay fixture" in the first build.

## 5. Phased build plan (revised — N-participant + live streaming pulled into MVP)

**Scope change, stated plainly: MVP is now bigger than originally submitted.**
The user pulled two things forward that were previously "later phases":
N-participant mesh (not just N=2) and live event/transcript streaming for a
visualizer. Both are real, non-trivial additions — see §9 and §10 for what's
actually new engineering, not just "the shape generalizes." Do not understate
this to make Phase 1 look smaller than it is.

- **Phase 0 (blocking, already in flight):** confirm `s2s-live-session-no-tool-definitions`
  merges — a self-play run is pointless if a participant can't actually
  discover tools live. Re-check `SessionRunOptions.ToolDefinitions` wiring at
  build time; per this reading it may already be closer to done than expected.
- **Phase 1 — the actual MVP (supersedes the narrower N=2-only ticket already
  submitted to the board as `s2s-two-agent-selfplay-harness-phase1`; that
  ticket should be revised or replaced, not left as-is, once this scope is
  approved):**
  - The `Room` registry from the transport comparison doc, built N-general
    from the start (an agent-ID-keyed registry hands out one loopback
    `Signaling`+`rtc.Peer` pair per OTHER participant on join — see §9 for
    why this part *does* generalize for free).
  - The PCM mixing layer each participant needs to fold N-1 peers' audio into
    the single audio-in stream their own provider session expects — see §9;
    this is the genuinely new piece N-participant surfaces.
  - Per-participant manifest config from §7 (provider/model/tools/voice per
    participant) — needed now, not deferred, since a same-provider-only room
    is a weaker MVP than what was already designed in §7. `voice` is the one
    piece of this that requires a small upstream fix first: neither
    `SessionRunOptions` nor `agent session` expose voice selection at all
    today, even though the provider layer already supports it (§7) — thread
    that plumbing through as part of this phase, not as a separate blocker.
  - The live event stream from §10 (SSE endpoint, `session_turn_completed`-
    style diagnostic events + filtered transcript deltas, per participant and
    room-wide) — no raw audio over this stream, WAV stays a post-hoc artifact
    (see §10 for the reasoning).
  - No tools required to prove the above; tools (real `exec` calls) layer in
    once Phase 0 lands, using the config already designed in §7.
  - Acceptance bar unchanged in spirit from the original ticket: a real,
    documented live smoke run (now with 3 participants, not 2, to actually
    exercise the mesh) proving clean multi-turn completion with no
    double-talk/wedge, plus a visualizer (even a minimal terminal one)
    genuinely consuming the live stream end to end. Still not CI-gated —
    real billed multi-connection live cost, same reasoning as before.
- **Phase 2 (separate, later):** corpus-to-fixture conversion — turn a
  self-play recording into a committed replay fixture in the existing
  `agent-cli/test/integration/testdata` convention, feeding back into CI
  rather than only living as an on-demand artifact.
- **Phase 3 (separate, later, NOT part of MVP):** human/browser join via real
  network signaling — see §8. Explicitly gated behind Phase 1 landing; the
  NAT/auth/safety concerns in §8 are substantial enough to stay out of MVP
  even though N-participant and live streaming are now in.

## 6. Real risks / unknowns — stated honestly

- **Cost and flakiness:** every run is multiple simultaneous live Realtime
  connections billed to the same account; a CI-gated version of this is
  expensive and non-deterministic (independently-reasoning live agents will
  not produce byte-identical conversations run to run) — this argues for an
  **on-demand tool**, not a CI-blocking test, at least through Phase 2.
- **Turn-taking correctness under a live bidirectional bridge is unverified.**
  Server-side VAD worked cleanly for one single-shot `--audio-in <file>` probe
  in this session; whether it correctly handles a *continuously open* live
  PCM stream carrying alternating real speech and silence from peer agents
  (rather than one finite utterance) has not been tested and should be the
  first thing Phase 1 proves or disproves, before any tool complexity is
  layered on. At N>2 this gets strictly harder — see §9.
- **Double-talk / echo risk:** if agent A's own emitted audio is still
  arriving at agent B while B starts speaking (the exact live analog of the
  `s2s-v3a`/`s2s-validate-interrupt-during-active-speech-live-shaped` scenario
  already filed), both sides' server-side VAD may fire simultaneously and
  produce a race condition never exercised by the v8 harness's strictly
  alternating scripted crossings. Do not assume this "just works" — instrument
  it and expect to find a real bug, consistent with what today's live-probe
  session already found twice elsewhere in this exact runtime.
- **No provider guarantee that multiple independent live WebSocket
  connections from the same API key behave identically to one** (rate
  limits, concurrent connection caps) — verify with a real multi-session
  smoke test before designing anything more elaborate on top. This matters
  more now that N-participant is in MVP (N connections, not 2).

## 7. Per-participant provider/model/tools/voice configuration

The CLI sketch floated in conversation (`agent room run --participant
name:persona --provider ... --model ... --tools <name>`) has one global
provider/model and a coarse per-name tools toggle. That's fine for the
trivial same-provider case, but the user wants genuinely independent
per-participant provider/model/tools. Cramming `provider`, `model`,
`api_key`, and a `tools` list into one `--participant` flag value is already
unreadable at 3 fields, so: **keep `--participant name:persona` for the
trivial ad-hoc case only (single shared `--provider`/`--model`/`--api-key`
for the whole room, tools off), and make manifest mode (`agent room run
--manifest room.json`) the only supported way to configure a non-trivial
room.** This mirrors how `agent-cli/internal/probe/fleet/manifest.go`
already draws the same line for scenario/transport/repeat/concurrency
combinations — inline flags do not attempt to express that cross-product,
only the manifest does.

Manifest shape, following this repo's existing conventions (`schema_version`,
snake_case JSON keys, top-level entries array, per-`agent-cli/internal/cli/session.go`
field names for provider/model/api-key so this maps 1:1 onto
`SessionRunOptions` without inventing new terminology):

```json
{
  "schema_version": 1,
  "room": { "max_turns": 8, "max_duration": "5m" },
  "participants": [
    {
      "id": "customer",
      "system_prompt": "You are a customer who wants a refund on order #4821.",
      "provider": "openai",
      "model": "gpt-realtime",
      "api_key_env": "OPENAI_API_KEY",
      "voice": "cedar",
      "tools": []
    },
    {
      "id": "assistant",
      "system_prompt": "You are a support agent with tools. Use them when needed.",
      "provider": "openai",
      "model": "gpt-realtime",
      "api_key_env": "OPENAI_API_KEY",
      "voice": "ash",
      "tools": ["exec"]
    }
  ]
}
```

`api_key_env` (an env var name), not a raw key, matches this repo's existing
posture of never taking secrets as plain CLI flags/manifest values (see how
`agent session --api-key` is the one exception, always meant for a single
live invocation, not a checked-in config). `tools` names entries from the
existing `agent-cli/internal/tools/registry.go` registry by name (e.g.
`"exec"`); an empty list means the participant advertises no tools, matching
`ToolDefinitions` being nil/empty on that participant's `SessionRunOptions`.

**`voice` — a genuinely new field, not a documentation gap, confirmed by
reading the code:** `models.SessionConfig.Voice string` (`go-llm-gateway/pkg/models/session.go:51`)
already exists and is already correctly serialized into the outbound
`audio.output.voice` field by `buildRealtimeAudioConfig` (`go-llm-gateway/pkg/providers/openai/session_config.go`)
— the provider layer has always supported this. But **neither
`SessionRunOptions` (`session_options.go`) nor `agent session`'s CLI flags
expose it anywhere today** — grepped both, no `Voice` field, no `--voice`
flag. Every live `agent session` invocation today gets whatever voice the
provider defaults to server-side, with zero client-side control. This is a
real gap in the base product, not something specific to the room design —
worth flagging as its own small fix (`SessionRunOptions.Voice string` +
plumbing it into the `models.SessionConfig` construction in
`session_options.go`'s provider-building code, plus optionally a `--voice`
flag on `agent session` itself) independent of whether the room feature ships,
since the room's per-participant `voice` field is just this same missing
plumbing, threaded once per participant instead of once per CLI invocation.

Distinguishable voices matter specifically for self-play and its
visualizer: with two AI participants sharing one default voice, a recorded
conversation (and a human skimming the live `--stream` transcript) has no
audio cue for who is speaking — matching each participant to a distinct
`voice` is what makes a 2+-party self-play recording actually parseable by
ear, not just by the `participant_id` tag in the transcript stream.

Supported values (OpenAI Realtime, as of this design): `alloy`, `ash`,
`ballad`, `cedar`, `coral`, `echo`, `fable`, `marin`, `nova`, `onyx`, `sage`,
`shimmer`, `verse`. Manifest validation should reject an unrecognized value
at load time with a typed error (following the same fail-fast posture as
`agent-cli/internal/probe/fleet/manifest.go`'s existing `ErrUnknownTransport`-
style validation — a new `ErrUnknownVoice` fits the same pattern), not defer
the failure to a live provider rejection mid-run. `voice` is optional per
participant; omitted, the provider's own default applies, same as today's
unconditional behavior — this field only ever narrows/specifies, it does not
change default behavior for a manifest that doesn't set it.

## 8. Human/browser join and customer WebRTC availability — deferred

**Customer-boundary finding (2026-08-28): the active WebRTC audio path is
not customer-usable yet.** The CLI deliberately rejects an otherwise valid
`agent session --transport webrtc` selection before config loading, provider
connection, signaling resolution, peer/media setup, or audio-device
acquisition. The rejection is the honest current contract: the repository's
only concrete `rtc.Signaling` implementation is an in-process loopback pair,
production CLI composition has no customer-reachable network signaling
resolver, and spoken-audio inputs (`--audio-in`, stdin, and microphone/device
speech) are not wired to the WebRTC runtime. Customers who need file, stdin,
or microphone speech should use the supported WebSocket path instead:
`--transport ws` with `--audio-in` or `--audio-in-device`.

This is specifically a deferred **active audio-participant** capability. It
must not be inferred from the receive-only external media ingestion path
(`go2rtc://`/`rtsp://` `--media-source`), which pulls a camera/source feed and
does not provide bidirectional offer/answer/ICE signaling or customer speech
input. It is also separate from the passive visualization/event-streaming
path (`--stream`), which observes events without audio, signaling, or room
participation. The CLI guard may be removed only after a customer-reachable
network signaling implementation and at least one supported spoken-audio
source complete a real end-to-end WebRTC session that a customer can use;
loopback-only tests and receive-only media ingestion do not satisfy that bar.
The future phase must still address the NAT/ICE traversal, authentication,
and safety/tool-boundary concerns documented below.

**Single most important finding: the abstraction fork the directive worried
about does not need to happen — it already exists in production code, one
layer up from `Signaling` itself.** `agent-cli/internal/services/session_runtime_rtc.go`
already defines:

```go
type SessionRTCSignalingResolver func(context.Context, string) (rtc.Signaling, error)
```

`sessionComposedRTCRuntime.Start` (session_runtime_rtc.go:269-374) calls
`r.components.ResolveSignaling(runCtx, r.selection.SignalingEndpoint)` to get
a concrete `rtc.Signaling` from an opaque endpoint string. This is *already*
the exact seam needed: a room-aware resolver can return a
`NewLoopbackSignalingPair()`-backed `Signaling` for a local AI participant
and a real network-backed `Signaling` for an externally-dialed endpoint,
through the identical function type the production runtime already consumes.
No fork in the `Room` design is needed at the interface level — `rtc.Signaling`
(signaling.go's `SendOffer`/`ReceiveOffer`/`SendAnswer`/`ReceiveAnswer`/
`SendCandidate`/`ReceiveCandidate`/`CompleteCandidateGathering`/
`WaitCandidateGathering`/`Done`/`Close`) is already implementation-agnostic
by design (confirmed by reading it directly, not inferring from doc comments).

**What's genuinely new, stated honestly:** a real network `Signaling`
implementation does not exist anywhere in this repo today. Grepped every
`.go` file for something satisfying the interface: the only implementations
are `LoopbackEndpoint` (signaling_loopback.go) and test doubles in
`session_runtime_rtc_test.go`/`agent-cli/internal/probe/fault/rtc_test.go`.
`SessionRTCSignalingResolver`/`ResolveSignaling` is wired into the production
`wire`/`cli` graph through `provideSessionRTCRuntimeFactory` and the default
`SessionRTCComponents`. That default resolver currently supports only the
in-process loopback endpoint; a customer-reachable network resolver is not
wired into the composition, so the production `--transport webrtc` CLI path
has the runtime seam and loopback implementation but no live network
signaling capability yet. This is consistent with
`s2s-b4-rtc-cli-surface` and `s2s-prereq-session-rtc-runtime-consumer` still
being open/in-review lanes on the board — **a real signaling implementation
is not a self-play-only gap, it is the same missing piece the core product's
own WebRTC transport feature needs.** Building it for room-join should likely
be the *same* work as whatever `s2s-b4-rtc-cli-surface` eventually needs, not
a parallel self-play-only implementation — flag this dependency explicitly
when this phase is ever filed as real work, rather than duplicating a
signaling server.

Also checked and ruled out as a shortcut: `go-llm-gateway/pkg/transport/rtc/media_source.go`'s
`ParseMediaSource` (go2rtc://, rtsp://) is a *receive-only* external media
ingestion pattern (pulling a camera feed in for v10), not a bidirectional
offer/answer/ICE signaling channel — not reusable for room-join.

**CLI verb shape**, reusing real existing device conventions
(`agent-cli/internal/cli/session.go`'s `--audio-in-device`/`--audio-out-device`
flag pattern — already referenced defensively via `cmd.Flags().Lookup(...)`,
consistent with device support being partially built — and the real
`agent devices list` command backed by `audio.DeviceRegistry`):

```
agent room run --manifest room.json --listen 127.0.0.1:8420
agent room join 127.0.0.1:8420 --name human \
  --audio-in-device "MacBook Pro Microphone" \
  --audio-out-device "MacBook Pro Speakers"
```

`--listen` is opt-in on the room host: omitted, the room stays exactly as
simple as Phase 1 (loopback-only, no open ports, AI-to-AI only). Set, it
stands up the (not-yet-existing) real signaling endpoint so `agent room
join` can dial in from a separate process — same machine or remote.

**Relationship to §10's `--stream` visualizer endpoint:** these are different
consumers and stay different mechanisms. `--listen` admits an *active audio
participant* into the mesh (needs real signaling, ICE, a mic/speaker) —
`--stream` (§10) is a *passive observer* reading events (no audio, no
signaling, no participation). They could reasonably be hosted by the same
room-server process on different routes/ports later, but nothing here
requires unifying them, and the directive's instruction not to conflate them
stands: do not make the visualizer dial in as a fake "silent participant"
through the `--listen` path just to reuse one mechanism — that would force
every visualizer to pay the ICE/signaling cost SSE doesn't need.

**Risks, stated honestly, not hand-waved:**

- **NAT/ICE traversal is an entirely new failure class.** Loopback signaling
  has no network path and therefore no ICE failure modes; a real remote
  join needs STUN (and likely TURN for anything not on the same LAN), none
  of which exists in this repo today. This is real, non-trivial networking
  work, not a config flag.
- **No auth concept exists anywhere in this room design yet.** `--listen`
  as sketched above is an open port with no access control — before this
  phase is real, "who is allowed to join" needs an actual answer (a shared
  join token at minimum), not just a TODO.
- **Safety framing for a human joining a room with live-billed, tool-using
  AI participants.** If a participant has `exec` (or any side-effecting
  tool) enabled and a human joins the conversation, the human can now
  indirectly steer real tool execution through natural conversation with no
  distinct trust boundary from the AI-only case. Whether that's acceptable
  depends entirely on what tools are enabled — this needs an explicit policy
  (e.g. tools force-disabled in any room with `--listen` set, or a separate
  allowlist), not silent inheritance of Phase 1's tool-enablement rules.
- **Billing/cost exposure to an external joiner.** A human joining triggers
  real live provider spend on whichever AI participants are in the room;
  `--listen` should probably require the host to have already bounded
  `max_turns`/`max_duration` rather than leaving a human able to keep an
  expensive room open indefinitely.

## 9. N-participant mesh — what generalizes for free vs. what's genuinely new

**The `Room` registry topology generalizes for free; per-participant audio
mixing does not. These are two different claims and the transport comparison
doc only made the first one.**

- **Signaling/connection topology (generalizes for free):** the mesh design
  from `selfplay-webrtc-transport-comparison.md` was already N-general by
  construction — a `Room` registry keyed by participant ID hands out one
  `NewLoopbackSignalingPair()` + `rtc.Peer` per OTHER participant on join.
  Pulling N-participant into MVP does not change this design at all; it just
  means building the registry as originally specified now, instead of
  hardcoding a bare pair first and generalizing later. Nothing structural
  changes here.
- **Audio mixing (genuinely new, not designed anywhere yet):** in the N=2
  case, each participant has exactly one peer, so "agent B's audio-in" is
  simply "agent A's audio-out" — a direct 1:1 relay, exactly what v8's bridge
  pattern already does. At N≥3, each participant has N-1 peers, but a
  realtime provider session (`SessionRunOptions`/`--audio-in`) still expects
  exactly **one** incoming PCM stream representing "what the user is saying."
  Two peers' simultaneous speech cannot both be forwarded raw into one
  `--audio-in` stream without becoming garbled, overlapping audio — real
  human multi-party calls solve this either by each client mixing all remote
  tracks in its own audio stack (what a browser does), or by an SFU/mixer
  combining streams server-side. **This repo has neither today** — grepped
  for any PCM mixing utility anywhere in `agent-cli`/`go-agent-loop`/
  `go-llm-gateway`: none exists. A real-time PCM sample-mixing function
  (sum + clip/normalize N int16 PCM streams into one, on the same clock/frame
  cadence) is new code this design must account for, sitting inside each
  participant's inbound leg of the `Room` before it reaches that
  participant's own `--audio-in` feed. This is a small, well-understood DSP
  operation (not a research problem), but it is real, non-trivial-to-forget
  scope that "the mesh generalizes" would otherwise hide.
- **Turn-taking gets harder, not just "more of the same," at N≥3.** §6
  already flags double-talk risk at N=2; at N≥3 a participant's provider-side
  VAD is now listening to a *mixed* stream of potentially multiple
  simultaneously-speaking peers, which is a strictly harder signal to segment
  correctly than one peer's clean audio. Phase 1's live smoke-test acceptance
  bar (§5) should explicitly include at least one 3-participant run for this
  reason — a 2-participant smoke test would not exercise this at all.
- **Clean PCM mixing is a hard MVP requirement, not a nice-to-have (operator
  confirmation, 2026-08-27).** Restated plainly since it's easy to let this
  drift into "good enough": the mixing function must correctly sum + clip/
  normalize N-1 int16 PCM streams on a shared frame cadence with no dropped
  samples, no clock drift between peers, and no audible artifact from the
  summing itself (see C2/C3 in §13 for the exact correctness bar). This is
  not optional scope inside Phase 1 — a room that "mostly" mixes correctly
  undermines every other N≥3 test in §13, since C1-C3/B2's mixing-dependent
  assertions assume the mixer itself is not the source of a failure.

## 9a. Room lifecycle and termination — resolved (operator decision, 2026-08-27)

**The room is a long-lived container process, decoupled from any single
participant's own conversational "ending."** Operator decision, stated
directly: "the room should only shut down if a stop signal sent to the CLI
or something, or the system fails." This resolves both E1 and G3 from §13
(previously flagged as undesigned) as real decisions, not open questions:

- **A room terminates only on:** (1) an explicit external stop signal —
  process-level (SIGINT/SIGTERM to the running `agent room run` process) or
  a dedicated command (e.g. `agent room stop <run-id>`; which mechanism is
  primary is an implementation detail for whoever builds this, not a design
  fork worth blocking on here), or (2) an unrecoverable system failure (see
  the taxonomy below).
- **One participant's own persona-driven end does NOT terminate the room.**
  A persona reaching its own natural "goodbye" (A1's original completion
  signal) ends *that participant's* turn-taking/session and is recorded as
  a per-participant terminal event — the room and its remaining participants
  continue exactly as they would with one fewer live peer. This is
  mechanically the same event as E1's dropped-connection case (the room
  loses one active participant either way) — they differ only in
  *termination reason*, not in what the room/mixer does about it. Revise
  A1's own framing accordingly: two-participant "natural completion" in a
  literal sense (the room itself stopping) does not happen from persona
  behavior alone at N=2 either, unless the manifest's own `max_turns`/
  `max_duration` are also configured to close the room out — which a real
  Phase 1 self-play run should generally do, for corpus-building purposes,
  even though the room's lifecycle model doesn't strictly require it.
- **`room.max_turns`/`room.max_duration` (§7) remain real, configured
  bounds** — a safety cap on an otherwise open-ended room, not a redefinition
  of "done." Silence (no one currently speaking) is never itself a
  termination trigger.
- **Termination-reason taxonomy — new, was undefined anywhere before this
  (A1/E1/G3 in §13 all independently surfaced the same gap).**
  `run-manifest.json` (§4) must record, room-level, exactly one of:
  `stopped` (explicit external signal), `max_turns_reached`,
  `max_duration_reached`, or `failed` (unrecoverable — e.g. every
  participant's connection failed, or the mixer itself faulted); PLUS, per
  participant, one of `ended` (its own persona concluded), `disconnected`
  (E1's dropped-connection case), or `error` (a participant-scoped failure
  that didn't take the whole room down). A room can legitimately show
  `max_turns_reached` at the room level while one participant's own record
  shows `ended` and another shows `disconnected` — these are independent
  axes, not one shared status.

## 10. Live event streaming — schema, transport, CLI verb

**Two distinct streams, not one, per the directive's own framing — keep them
distinct in the implementation, not just in prose:**

- **Execution stream** = `SessionDiagnosticRecord` events (§3) — structured,
  low-volume, semantic (`session_turn_completed`, `session_tool_call_unexecutable`,
  `session_failure`, `session_metrics`), already flat `{Event string, Fields
  map[string]string}` (`session_diagnostics.go:78`). This is what a
  monitoring/debugging view of "what is each agent's execution loop doing"
  wants.
- **User stream** = a filtered subset of the raw `StreamObserver` delta feed
  (§3), specifically `messages.TranscriptDeltaValue`/`TranscriptEndValue`
  (`go-agent-loop/pkg/messages/audio_values.go:82-105`, confirmed these
  already exist with clean `Text`/`FullText` fields) — the human-readable
  "what is being said" view a person watching a dashboard actually wants,
  not the full unfiltered delta firehose (which also includes every raw
  audio-delta chunk, session-lifecycle events, etc. — far too noisy for a
  transcript view). Do not conflate this with the execution stream or with
  raw `StreamObserver` output; it is a deliberately filtered projection of
  it.

**Transport: Server-Sent Events (SSE), not WebSocket.** The visualizer is a
pure observer — it never needs to send anything back to the room (no pause/
kill/inject control implied by "mini visualizer over the stream" anywhere in
what was asked). SSE is the simpler mechanism for a read-only fan-out: plain
HTTP, `text/event-stream`, works from a bare browser `EventSource` with zero
client library and built-in auto-reconnect, and needs no new dependency (the
stdlib `net/http` `ResponseWriter` + manual flush is sufficient — no framing
protocol to hand-roll, unlike raw websocket messages). Note for completeness:
`gorilla/websocket` is already a dependency in this repo and there is already
one precedent for a local Go process serving a websocket endpoint
(`go-llm-gateway/pkg/testing/localai/endpoint.go`, a test double) — so a
websocket server is not a dependency blocker either way, but it would be
solving a bidirectional problem this use case does not have. Standing up an
`http.Server` for SSE at all is a new pattern for this repo outside the
`rtc` package and outside test infrastructure specifically (grepped:
production code has no existing `http.Server`/`ServeHTTP` outside `rtc`'s
websocket signaling and the test-only `localai` double) — small, but real,
first instance of the room driver acting as an HTTP server.

**Audio is explicitly out of scope for the live stream in this phase.**
Streaming raw PCM/Opus to a browser for waveform display or playback is a
meaningfully bigger lift (chunked encoding, a client-side audio player,
timing/buffering) than forwarding small JSON event lines, and nothing in
"expose the agent execution streams, user stream" names audio specifically —
it names streams of *events*. The WAV files from §4 remain the audio
artifact, written post-hoc as today; a visualizer can link to/offer them for
playback after a turn completes, or after the run ends, without needing live
audio transport. Revisit only if a real need for live waveform display
surfaces later — do not build it speculatively now.

**CLI verb**, a new flag on the same `agent room run`, separate from `--listen`
(§8) since it is a different consumer with different needs:

```
agent room run --manifest room.json --stream 127.0.0.1:8422
```

Omitted, no HTTP server starts at all — the room stays exactly as simple as
a pure `--manifest`-only run with no live consumers. Set, it opens a local
SSE endpoint at that address with (at minimum) these routes:

- `GET /events` — one combined SSE stream, every event tagged with
  `participant_id` (or `"room"` for room-wide events like a new participant
  joining or the run terminating) so a visualizer can filter client-side
  without needing N separate connections.
- `GET /events?participant=<id>` — optional server-side filter to just one
  participant's events, for a per-agent view.

**Event contract** (SSE `data:` payload is one JSON object per line — this is
the minimum a visualizer author needs, without reading Go source):

```json
{"type": "diagnostic", "participant_id": "assistant", "event": "session_turn_completed", "fields": {"turn_index": "3", "output_audio_bytes": "48200"}, "ts": "2026-08-27T00:00:00Z"}
{"type": "transcript_delta", "participant_id": "customer", "text": "so when can I expect", "ts": "2026-08-27T00:00:01Z"}
{"type": "transcript_end", "participant_id": "customer", "full_text": "so when can I expect the refund to post?", "ts": "2026-08-27T00:00:02Z"}
{"type": "room", "event": "participant_joined", "participant_id": "assistant", "ts": "2026-08-27T00:00:00Z"}
{"type": "room", "event": "participant_failed", "participant_id": "assistant", "reason": "transport disconnected", "ts": "2026-08-27T00:00:03Z"}
{"type": "room", "event": "run_terminated", "reason": "max_turns_reached", "ts": "2026-08-27T00:05:00Z"}
```

`type` discriminates the three payload shapes (`diagnostic` mirrors
`SessionDiagnosticRecord` verbatim — same `event`/`fields` names, no
translation layer to maintain; `transcript_delta`/`transcript_end` are the
filtered user-stream projection; `room` covers lifecycle events the driver
itself emits, not any one participant's session). A minimal visualizer (a
static HTML page with `new EventSource(url)`, or a terminal `curl -N` reader)
can render a live transcript view from `transcript_*` events alone and a
debug/execution view from `diagnostic` events, without needing to distinguish
provider or parse `messages.StreamMessage` Go types at all — the SSE payload
is the entire contract surface.

**Resolved (operator confirmed): forward-only, no replay, not needed yet.**
A visualizer that connects after the room has already been running sees only
events from that point forward — nothing is buffered/replayed server-side.
This matches `EventSource`'s own default reconnect semantics and needed no
new design. If "join a run already in progress and see what already
happened" becomes a real need later, that requires a server-side ring buffer
of recent events keyed per room — explicitly not designed here, and not
needed for Phase 1.

## 11. Expected CLI usage — end-to-end walkthrough

Everything above is specified piecemeal by section; this is the one place to
read for "how does a person actually use this," start to finish, with no
detail left implicit.

1. **Author a manifest** (§7 shape) — `room.json`, one entry per participant
   with its own `provider`/`model`/`api_key_env`/`system_prompt`/`tools`.
2. **Run it:**
   ```
   agent room run --manifest room.json \
     --out ./runs/2026-08-27-refund/ \
     --stream 127.0.0.1:8422
   ```
   - The driver builds the `Room` registry (§9): every participant gets one
     loopback `Signaling`+`rtc.Peer` per other participant (mesh), and — at
     N≥3 — the PCM mixing layer (§9) folds each participant's N-1 incoming
     peer streams into the single audio-in feed their own live provider
     session expects.
   - Each participant's real `SessionRunOptions` starts against its
     configured provider/model, seeded by its `system_prompt`, with
     `ToolDefinitions` wired per its `tools` list (§7).
   - `--stream` is optional (§10): omitted, no HTTP server starts at all and
     the run is otherwise identical. `--out` is not optional — artifacts
     always land on disk regardless of whether anyone watches live.
   - Terminal output while running stays terse, matching `agent session`'s
     existing progress-line convention — one line per turn/tool-call per
     participant (e.g. `[assistant] turn 3 complete, 1 tool call`), not a
     firehose; the firehose lives at `--stream`'s `/events`, not stdout.
3. **Watch live (only if `--stream` was set):** point a browser or the
   reference visualizer (§12) at `http://127.0.0.1:8422/events`, optionally
   `?participant=<id>` to filter to one side. Nothing to install — a bare
   `EventSource` or `curl -N http://127.0.0.1:8422/events` both work per
   §10's contract.
4. **The run ends** — `max_turns`/`max_duration` from the manifest's `room`
   block is reached, or (later, not Phase 1) an assertion layer flags
   completion. Artifacts (§4) are written to `--out`: `agent-{id}.wav`,
   `agent-{id}.diagnostics.jsonl`, `agent-{id}.deltas.jsonl` per participant,
   plus one joined `run-manifest.json`. The `--stream` server closes; a
   connected visualizer sees the `{"type":"room","event":"run_terminated",...}`
   event (§10) as its signal to stop expecting more.
5. **Inspect after the fact**, mirroring the existing `agent session
   list`/`show` pattern:
   ```
   agent room list
   agent room show <run-id>
   ```
6. **Not part of this walkthrough — Phase 3, not MVP (§8):** a human joining
   live via `agent room run --listen <addr>` + `agent room join <addr>
   --audio-in-device ... --audio-out-device ...`. Mentioned only so the full
   lifecycle's shape is visible; not available in the Phase 1 build this
   walkthrough otherwise describes.

## 12. Visualization expectations — what "done" looks like for Phase 1

**Phase 1 ships one minimal REFERENCE visualizer, not a product.** Its only
job is to prove the §10 SSE contract genuinely works end to end — that a
process outside this Go codebase can consume it with zero special tooling —
not to be a good or complete UI. Treat any polish beyond the bar below as
explicitly out of scope, not an oversight.

**"Done" is concretely:**
- One static HTML file (a `<script>` block calling `new EventSource(...)`,
  no build step, no framework) *or* one small terminal script (`curl -N` +
  a line-oriented formatter) — either satisfies the acceptance bar; do not
  require both.
- It renders a **live-updating transcript panel**, one line/row per
  participant, built only from `transcript_delta`/`transcript_end` events —
  this is the direct proof that the "user stream" (§10) is real and usable
  on its own, without needing the diagnostic stream too.
- It renders a **separate raw event/diagnostic panel** (or a distinct log
  view) built only from `diagnostic` events — proof the "execution stream"
  (§10) is genuinely separable from the transcript view, not bolted together.
- It is exercised as part of Phase 1's acceptance run (§5): the 3-participant
  live smoke test must have this visualizer actually running and showing
  both panels updating in real time during that run, not just described.

**Explicitly NOT required for Phase 1** (do not silently expand scope to
cover these; file them as later work if they turn out to matter):
- Audio playback or waveform display (§10 already excludes live audio from
  the stream entirely — a visualizer can link to the post-hoc `.wav` file
  from §4, nothing more).
- Authentication or access control on the `--stream` endpoint — this is a
  local-only, opt-in-by-flag surface, same trust posture as every other
  local-only piece of this design; do not add auth speculatively.
- A polished, packaged, or framework-based UI — a bare static HTML file is
  sufficient and correct for this phase.
- Historical replay/backfill for a late-joining visualizer — see the open
  question immediately above §11; forward-only is the working assumption
  unless that's explicitly revisited.
- Multi-room support in one visualizer instance — one `--stream` endpoint,
  one room, one visualizer instance; do not design for watching multiple
  concurrent rooms in Phase 1.

## 13. Test-scenario hardening matrix

Every scenario below names a precise mechanism and a precise pass/fail
contract, matching the bar already set by the three validation-replay ideas
submitted to the board this session (`s2s-validate-async-tool-result-interrupts-speech`,
`s2s-validate-interrupt-during-active-speech-live-shaped`,
`s2s-validate-multiturn-duplex-multiplex`) — "test interrupts" is not a
scenario; a named collision with a stated correct outcome is. Each entry
cites the design section/risk it guards and whether it's provable via
Phase 1's live-smoke posture (§6: not CI-gated, real billed multi-connection
cost) or is a candidate Phase 2 replay fixture (§4/§5) once a live run
confirms the behavior once. **Where a scenario tests something the design
doc has not actually specified a correct answer for, that is stated
explicitly as an open design question, not papered over with an assumed
contract.**

### A. Happy path

1. **A1 — Two-participant conversation reaches natural completion.**
   Mechanism: two participants (distinct personas, distinct `voice` per §7)
   converse until one persona's own instructions end its side, and the
   manifest's `room.max_turns`/`max_duration` (set for this run, per §9a's
   lifecycle model — a persona ending its own side does not by itself end
   the room) close the room out shortly after. Correct: `run-manifest.json`
   records `max_turns_reached` or `max_duration_reached` at the room level
   (per §9a's taxonomy, resolved from what A1 originally surfaced as
   undefined) and `ended` for the participant whose persona concluded, both
   participants' `.wav`/`.diagnostics.jsonl`/`.deltas.jsonl` are non-empty
   and turn-count-consistent with each other, and every `--stream` event
   (§10) that fired during the run is accounted for in the final artifact
   (no event emitted live that isn't also reconstructable from the JSONL
   files after the fact — live and post-hoc views must agree). Negative
   control: assert this *fails* if either side's turn count diverges from
   the other's transcript-implied turn count by more than the expected
   off-by-one at a natural end (whoever ends their side doesn't wait for a
   reply). Live-smoke now;
   Phase 2 replay-fixture candidate once proven once.
2. **A2 — Three-participant conversation reaches natural/bounded completion.**
   Same as A1 but N=3, `max_turns`-bounded rather than persona-initiated
   end (isolates "does bounded termination work correctly at N≥3" from A1's
   "does persona-initiated end work at all"). Correct: all 3 participants'
   artifacts are consistent with each other under the mixed-audio path
   (§9) specifically, not just individually well-formed — e.g. participant
   C's transcript should never contain speech attributable only to A and B
   talking *to each other* if C's own mixed-in audio incorrectly leaked
   into what C's session treated as directed at it (this is the first real
   test that per-participant audio isolation — "everyone hears everyone,
   but each side's *own outgoing* stream is what gets transcribed as its
   diagnostic record" — actually holds; nothing in §3/§9 tests this today).
   This is the required N≥3 case §9 already flags as mandatory for Phase 1's
   acceptance bar — do not substitute a second N=2 run for it.

### B. Interrupts/barge-in in a mesh, not 1:1

3. **B1 — A interrupts B while C is independently mid-turn: does cancelling
   B affect C?** Mechanism: in a 3-participant room, A's speech triggers
   provider-side cancellation of B's in-flight response (the live analog of
   `s2s-v3a`, now inside a mesh) at the same wall-clock moment C is
   producing or about to produce its own unrelated turn. Room/mesh-specific
   risk (not a restatement of v3a): v3a proves cancellation works for ONE
   pair; this asks whether the `Room`'s per-participant isolation (§9) is
   actually leak-proof — does B's cancellation correctly stay scoped to the
   A↔B relationship, or does something in the shared PCM-mixing/event-fan-
   out path (§9, §10) cause C's own turn to also glitch, restart, or emit a
   spurious `diagnostic`/`transcript` event it shouldn't have. Correct: C's
   `.diagnostics.jsonl` shows zero events correlated with B's cancellation
   (no `session_turn_completed` gap, no unexplained gap in C's transcript
   timeline). Negative control: fails if C's artifacts show ANY event whose
   timing correlates with B's cancellation rather than C's own turn
   boundaries. Live-smoke only — this needs 3 real live connections timed
   precisely, not easily replayable without first proving the live shape.
4. **B2 — Two participants barge in on the same third participant nearly
   simultaneously.** Mechanism: A and B both start speaking (interrupting C)
   within the same short window. **Resolved (operator decision): this is not
   engineered mesh-level arbitration — participants carry an explicit
   priority/precedence instruction in their own `system_prompt`, and the
   room mixer/transport makes no attempt to pick a "winner."** Concretely:
   the PCM mixer (§9) still sums both A's and B's inbound audio into C's
   feed unconditionally (mixing has no concept of priority — it is a signal-
   processing layer, not a conversational one); *social* resolution of
   "who actually gets to keep talking" is the personas' own job, the same
   way it would be for two humans in a real chat room who've been told
   "defer to whoever raised the topic" — a persona whose `system_prompt`
   says e.g. "you are lower priority than the customer; if interrupted while
   they are also interrupting, yield" is expected to yield via its own
   turn-ending behavior, not via a mesh-level mute/precedence mechanism.
   This scenario's real job in Phase 1 narrows accordingly: prove the mixer
   correctly sums both without crashing/wedging (a mechanical mixing
   correctness check, same bar as C2/C3), and prove that a persona given an
   explicit priority instruction actually behaves accordingly at least
   probabilistically across repeated runs (this is inherently a soft/
   behavioral check, not a hard deterministic pass/fail — document it as
   such, don't force a false-precision negative control onto model
   behavior the design has deliberately delegated to the prompt layer).

### C. VAD under mixed/multi-peer audio (§9's core new risk)

5. **C1 — Precise, automatable definition of "VAD wedged."** Before writing
   a VAD scenario, the matrix needs a mechanical (not "seemed off to a human
   skimming the transcript") definition: a participant is **wedged** if a
   `session_turn_completed` or `session_failure` diagnostic record has not
   been emitted for that participant within some bounded multiple (e.g. 3x)
   of the room's configured turn-silence expectation while the room is still
   otherwise active — i.e. detectable purely from the existing `Diagnostics`
   stream (§3) plus wall-clock, no new instrumentation required. This
   definition itself is new (nothing in §3/§6/§9 defines "wedged"
   mechanically) and should be treated as part of what this scenario
   delivers, not just a precondition for it.
6. **C2 — VAD segmentation correctness with 2 simultaneous mixed peers
   (N=3).** Mechanism: B and C speak at overlapping times; A's inbound feed
   is their PCM-mixed sum (§9). Correct: A's provider-side VAD produces
   turn boundaries attributable to real speech content from the mix, not a
   spurious extra turn from the mixing artifact itself (e.g. constructive/
   destructive interference at the sample level creating an energy profile
   neither B's nor C's clean audio would have produced alone). Negative
   control per C1's definition: fails if A wedges, or if A's turn count
   diverges from "one turn per actual distinct utterance in the mix" by
   more than a documented tolerance. This is the single scenario most
   likely to reveal a real bug — see closing summary.
7. **C3 — A peer's silence doesn't falsely trigger the mixer/VAD.**
   Mechanism: at N=3, B is actively speaking while C is silent (not just
   "C exists but isn't talking" — verify the mixer correctly treats a silent
   peer as a zero/near-zero contribution, not as a hidden noise floor that
   could nudge A's VAD). Room-specific: a 1:1 session never had a "silent
   third party" concept at all. Correct: A's turn segmentation with a
   silent C present is byte-for-byte/statistically equivalent to the same
   scenario with C entirely absent (N=2 A/B baseline) — a real regression
   test comparing an N=3-with-silent-peer run against an N=2 control run of
   the same A/B content.

### D. Tool calls in a room, multi-party

8. **D1 — Async tool result races audio from a *different* peer than the
   caller.** Builds directly on `s2s-validate-async-tool-result-interrupts-speech`
   but names what's actually new at N≥3: that lane proves a tool result
   colliding with *its own participant's* unrelated audio; this asks what
   happens when participant B issues a tool call, and while it's outstanding,
   a *different* participant C (not B) is the one actively speaking when
   B's result arrives. Correct: C's turn is completely unaffected (the tool
   result is B-scoped, must never touch C's session/diagnostics at all) —
   this is really an isolation test like B1, applied to tool calls instead
   of barge-in. Negative control: any event in C's `.diagnostics.jsonl`/
   `.deltas.jsonl` correlated with B's tool-result arrival is a failure.
9. **D2 — Two participants both have tools enabled and both call
   concurrently.** Mechanism: B and C (both `tools: ["exec"]` per §7) each
   independently issue a tool call in roughly the same window. Room-specific:
   confirms each participant's `ToolExecutor`/`ToolDefinitions` (§3, §7)
   stay genuinely per-participant-scoped under real concurrency, not just
   per-participant-configured — i.e. B never executes a call meant for C's
   session or vice versa. Correct: B's and C's diagnostic/tool-call records
   pair 1:1 with their own issued calls only, verified by correlating call
   IDs end to end per participant.
10. **D3 — A's speech (no tools) is unaffected by B's tool execution
    latency.** Mechanism: B's tool call takes measurably long (e.g. a
    deliberately slow `exec` command) while A, who has no tools at all
    (`tools: []`), continues an independent turn. Correct: A's turn timing/
    completion is not gated on B's tool executor in any way — proves the
    per-participant `ToolExecutionTimeout` (§3's cited field) doesn't leak
    cross-participant blocking into the shared mesh/mixer path.

### E. Failure modes specific to a room

11. **E1 — One participant's live connection drops mid-conversation; do the
    others degrade gracefully?** Mechanism: kill/disconnect one participant's
    underlying live session (simulated network failure, not a graceful end)
    while the room is otherwise active. Room-specific, not a restatement of
    any single-session error vertical: this is about the *other*
    participants' and the *mixer's* behavior, not the failed one's own error
    handling (which v6a-v6d already cover for a single session). **Contract
    now resolved by §9a** (was undesigned when this scenario was first
    written): the room does NOT terminate from this alone — the dropped
    participant's own record shows `disconnected`, the room continues with
    the remaining N-1 participants exactly as if that peer had ended
    normally, and the room only reaches a `failed` room-level reason if
    every participant is lost or the mixer itself faults. The mixer must
    correctly sum over N-1 remaining streams with no stale/closed-track
    fault (§9's "clean PCM mixing is a hard requirement" restatement).
    Negative control: the room must not hang waiting on the dropped
    participant, must not spuriously terminate the whole room from one
    participant's disconnect alone, and surviving participants' own
    artifacts must remain internally consistent (no corruption bleeding from
    the dropped peer's mixed-in audio at the moment of disconnect).
12. **E2 — A participant's provider session returns an auth/rate-limit
    error at room start, before any conversation happens.** Mechanism:
    misconfigure one participant's `api_key_env` (§7) or trigger a real
    provider rate-limit. Correct: fail the whole room construction with a
    clear, attributable error (which participant, which cause) rather than
    partially starting a room with fewer live connections than the manifest
    specified — a room silently running with 2 of 3 configured participants
    is a worse failure mode than refusing to start. This is a design
    decision this scenario should force, not assume.
13. **E3 — N simultaneous live connections from one API key actually
    succeed (§6's flagged risk, now testable precisely).** Mechanism: start
    a room at Phase 1's target N (3, per §5/§9's acceptance bar) and confirm
    all N live WebSocket connections establish cleanly with the same API
    key at the same time — §6 states plainly "no provider guarantee" this
    works, so this is a real unknown, not a formality. Correct: all N
    connections reach `session.created` within a reasonable bound with no
    connection-count rejection; if the provider does reject/throttle beyond
    some N, that ceiling needs to be documented as a real product constraint
    on room size, not discovered later by a confused operator.

### F. Vision — resolved: a new file-read-style tool, not live image sharing

**Operator resolution (2026-08-27): vision means a vision-capable *tool*,
"basically a file read or something" — not participant-to-participant live
image sharing.** This confirms the second of the two interpretations §F
originally distinguished, and rules out the first: no mechanism is needed in
the mesh/PCM-mixing/`rtc` transport layer for this, since it never carries a
non-audio payload between participants at all.

Concrete shape, grounded in what already exists rather than invented: a new
tool (name TBD, e.g. `read_image` or `view`) in
`agent-cli/internal/tools/registry.go`, modeled directly on the existing
`ReadFileTool` (`tool_filesystem.go:222`) but for image paths — takes a file
path argument, and instead of returning raw bytes/text, attaches the image
to the calling participant's own turn context via the *same* machinery the
session-level `--image` flag already uses: `PrepareSessionImageParts`
(`session_image.go:263`) and `resolveSessionImageCapabilities`
(`session_image.go:326`). This is genuinely new (no such tool exists in the
registry today), but it is new *wiring*, not new vision capability — the
provider-facing image-attachment path this would call is the same one
already proven live in this session's own probe (a real photo, correctly
and accurately described, per the live-probe session's vision check). A
participant with `"tools": ["read_image"]` per §7's manifest can now
independently decide to "look at" a committed file mid-conversation, same
as it can independently decide to `exec` a shell command today.

**F1 — vision-tool test scenario, now writable with a real mechanism to
test:** a participant with `read_image` enabled is asked (via the other
participant's speech) about the contents of a specific committed image; it
must call `read_image` with that path and its subsequent spoken response
must be grounded in the image's actual content (reuse the existing vision
verticals' grounding-check style — compare the response against known pixel
content or a real photo's known subject, not just "did it call the tool").
Negative control: a participant without `read_image` enabled, asked the same
question, must not fabricate a plausible-sounding description — matches the
existing `s2s-e2e-vision-describe`/`session_image.go` verticals' own
no-tool-no-hallucination posture, now exercised through the multi-party
conversational path instead of a direct CLI `--image` flag.

### G. Other mesh-specific risks not in the user's own checklist

14. **G1 — Live-stream event ordering under real concurrency.** Mechanism:
    with N participants each emitting `Diagnostics`/`StreamObserver` events
    concurrently, confirm the `--stream` SSE fan-out (§10) never drops,
    duplicates, or misorders events *within* one participant's own stream
    (cross-participant ordering is not claimed as meaningful anywhere in
    §10 and doesn't need to be — only per-participant ordering is a real
    contract). This is a concurrency-correctness question about the fan-out
    implementation itself, orthogonal to whether the conversation content is
    correct, and easy to miss because it's about the *observability* layer,
    not the session runtime. Negative control: replay a `.deltas.jsonl`
    file's true order against the `--stream`-captured order for the same
    participant/run and diff them — any divergence is a failure, and this
    one genuinely is Phase-2-replay-fixture-friendly today (no live cost to
    re-verify once one real run's dual capture exists).
15. **G2 — Distinct `voice` values are actually audible/distinguishable in
    the recorded output, not silently ignored.** Mechanism: a room with 2+
    participants configured with different `voice` values (§7) — confirm
    each participant's `.wav` (§4) is produced using its OWN configured
    voice, not a shared default (the exact gap §7 identified: neither
    `SessionRunOptions` nor `agent session` currently plumb `voice`
    anywhere, so this scenario is also the acceptance proof that the fix
    described in §7 actually landed, not just a room-feature nicety).
    Correct: a basic audio-fingerprint/spectral comparison (not a human
    listening test) between two participants' `.wav` output shows they are
    NOT the same voice when configured differently, and ARE consistent
    (same voice used throughout, not drifting mid-run) within one
    participant's own output. Negative control: fails if two differently-
    configured participants produce acoustically indistinguishable output,
    or if `voice` is silently dropped and both fall back to one default.
16. **G3 — Room termination coordination: one persona "hangs up," do the
    others keep running correctly?** Mechanism: at N≥3, one participant's
    persona instructions end its own side (matches A1's mechanism) while the
    other 2 have no such instruction and the room's `max_turns`/
    `max_duration` have not yet been reached. **Contract now resolved by
    §9a** (was undesigned when this scenario was first written): per the
    operator's explicit lifecycle decision, one participant ending does
    *not* mean the room should end — the room is only ever stopped by an
    external stop signal or a `failed` system state, never by inference
    from one participant's own terminal event. Correct: the ending
    participant's record shows `ended`, its `Peer` connection closes
    cleanly, the PCM mixer correctly drops to summing over the remaining
    N-1 streams (same mechanical bar as E1), and the *other* participants
    continue conversing/being bounded by their own `max_turns`/
    `max_duration` exactly as if that participant had never left. Negative
    control: fails if the room terminates early from one participant's
    `ended` event alone (that would contradict the resolved lifecycle
    model), or if the remaining participants wedge/hang once the mixer's
    input count changes from N-1 peers to N-2.

**Provability summary:** scenarios A1/A2/G1/G2 are the most Phase-2-replay-
fixture-ready once proven live once (deterministic content, no live-only
timing dependency in what's being asserted). B1/B2/C2/C3/D1/E1/E3/G3
genuinely require the live-smoke posture (§6) — several of them (B2, E1,
G3) are as much "discover and document the actual/intended behavior" as
"test a known contract," because the design doc does not yet specify a
correct answer for them.
