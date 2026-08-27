# Self-play transport comparison: 1:1 loopback vs. mesh vs. real SFU

## What's actually in this repo today (verified by reading the code, not docs)

`go-llm-gateway/pkg/transport/rtc/` (1856 lines across peer.go, signaling.go,
signaling_loopback.go, track_in.go, track_out.go, codec.go) is a real,
already-hardened WebRTC package, but it is **strictly 1:1** at every layer:

- `Peer` (peer.go) wraps exactly one `Conn` (one PeerConnection-equivalent)
  with reconnect/backoff. No registry of peers, no concept of "room."
- `Signaling` (signaling.go) is an offer/answer interface for exactly two
  parties. `NewLoopbackSignalingPair()` (signaling_loopback.go) is hard-coded
  to 2 endpoints: `messages [2][]any`, `read [2]int`, index 0/1 only. It
  cannot be generalized to N by passing a bigger number — the whole type is
  built around a pair.
- `InboundTrack`/`OutboundTrack` (track_in.go/track_out.go) are each exactly
  one RTP stream, one SSRC (`"SSRC changed within one audio track"` is a hard
  error). No multi-track demux, no per-participant fan-out.
- Confirmed via grep: `agent-cli/internal/cli/device_probe_runtime.go`
  (the v9 webrtc-device-roundtrip lane) uses two raw
  `*webrtc.PeerConnection` fields named `sender`/`receiver` directly — the
  product's real WebRTC path today is 1:1, not room-based, anywhere.
- Confirmed via grep across `go.work.sum` and every `go.mod`: neither LiveKit
  nor gortc is a dependency anywhere in this repo. Only `pion/webrtc/v4`.

So "reuse the existing rtc package for a room" is not a small extension —
the room concept does not exist in this codebase at all yet, at any layer.

## Option A — 1:1 loopback peer (my original recommendation)

Wire two agent sessions via `rtc.Peer` + `NewLoopbackSignalingPair()`.

- **Buildable today, minimal new code.** The signaling, connection lifecycle,
  and single in/out track are all already written and tested.
- **Recording:** tap `InboundTrack`/`OutboundTrack` directly per side, or
  simpler, keep recording at the PCM level above the codec (same as today's
  `--audio-out`/Diagnostics seams) — either works, no new recording code.
- **Plugs directly into the existing v9/v10/b4-rtc-* transport code** — same
  package, same primitives, most faithful to the real customer WebRTC path.
- **Hard ceiling: N=2, permanently.** `LoopbackEndpoint`'s `[2]any` arrays
  and the whole `Signaling` interface's offer/answer shape are pair-specific.
  A 3rd participant is not "add one more" — it's building a second, different
  signaling/routing abstraction from scratch, later, on top of code that
  actively assumes two parties. If N-party is a real near-term goal, Option A
  is throwaway work, not a stepping stone.

## Option B — self-hosted mesh or SFU on pion, in this repo

Two shapes, genuinely different:

- **Mesh (every agent peers directly with every other agent).** Each agent
  gets one `rtc.Peer` per OTHER participant. No new server-side routing code
  — every connection is still 1:1, so `Peer`/`InboundTrack`/`OutboundTrack`
  are reused unmodified, N times over per agent. What's new: a participant
  registry and N*(N-1)/2 signaling exchanges instead of 1 (loopback-style,
  in-process, still no external server needed for self-play specifically).
  Bandwidth/CPU is O(N^2) connections — irrelevant at self-play's real scale
  (2-5 agents), a real problem past ~6-8.
- **Real SFU (one hub, each participant sends once, hub forwards to N-1).**
  Needs genuinely new code this repo does not have: a room registry, a
  packet-forwarding/relay path (per pion's own SFU pattern — receive RTP on
  one PeerConnection's inbound track, re-write/forward it out multiple other
  PeerConnections' outbound tracks), and either audio mixing or simulcast/
  track-selection if you don't want to hand every participant N-1 raw
  streams. This is a meaningfully larger, new subsystem — track_in.go and
  track_out.go give you the codec/RTP plumbing per leg, but the routing hub
  itself does not exist and is most of an SFU's real complexity.
- **Recording** in either shape: tap each participant's own inbound/outbound
  tracks (mesh) or the hub's per-participant forwarded streams (SFU) — same
  effort either way, no new recording concept needed beyond Option A's.
- **Relationship to v9/v10/b4-rtc-*:** the mesh shape reuses those lanes'
  exact primitives per-connection, so it hardens the same code paths Option A
  does, just N times. The SFU shape adds a new component (the hub) that sits
  *alongside* that transport code, not inside it — it would not itself
  exercise/harden the v9/v10/b4-rtc-* verticals, only the per-leg connections
  underneath it would.

**Mesh is the right shape here, not a hub-based SFU.** At self-play's actual
scale (a handful of agents, not hundreds of viewers), mesh is simpler,
reuses 100% of existing primitives with zero new routing/mixing code, and
its O(N^2) ceiling only bites at a scale this use case will likely never
reach. A hub-based SFU is the right architecture for a broadcast/viewer
scenario (1 speaker, many listeners) — that is not this problem.

## Option C — external SFU (LiveKit)

New dependency, and not just a Go import — LiveKit is a separately-run
service (or LiveKit Cloud), meaning new infrastructure to deploy/operate,
new auth/token plumbing, and a second recording/egress pipeline to learn
that is unrelated to this repo's own `SessionDiagnosticSink`/`StreamObserver`
execution-loop capture (LiveKit's built-in recording covers audio/video, not
this repo's own diagnostic/tool-call trace format — you'd still need to wire
the execution-loop capture yourself regardless of transport choice).
Real room semantics and turnkey recording/egress are a genuine advantage if
this becomes customer-facing multi-party infrastructure later, but for a
self-play *validation* harness it is the heaviest option for no near-term
benefit, and it does not exercise this repo's own WebRTC transport code at
all (defeats the "validate our real path" motivation entirely).

## Recommendation

Build **Option B, mesh shape** for Phase 1, not Option A and not a hub SFU:

- It costs almost nothing extra over Option A today (N=2 mesh is *exactly*
  Option A's connection topology — one `rtc.Peer` pair) while establishing
  the actual generalizable shape from the start: a participant registry
  keyed by agent ID, each agent holding one `rtc.Peer` per peer, N=2 today
  and N=3+ later is genuinely "add a participant to the registry," not a
  redesign.
- It reuses 100% of the existing, already-hardened `rtc.Peer`/`InboundTrack`/
  `OutboundTrack` primitives, at every N, which Option A's loopback-specific
  path cannot claim past N=2 and Option C forfeits entirely.
- Loopback signaling still works fine for local self-play at small N (no
  external signaling server needed) — the mesh registry just needs to hand
  out N-1 loopback pairs per agent instead of 1, and manage N-1 `Peer`
  instances per agent instead of 1. Concretely: replace `NewLoopbackSignalingPair()`
  (hardcoded 2-endpoint type) with a small new `Room` type — a registry
  keyed by agent ID that, on `Join`, creates one fresh loopback-style pair
  per existing member and returns the new agent its set of peer connections.
  That `Room` type does not exist today and is the one genuinely new piece.
- Defer the hub/SFU shape and LiveKit entirely — revisit only if a real
  future need for large-N broadcast (many passive listeners, not mutually-
  peered active agents) actually materializes.

Net new work for Phase 1 beyond Option A: a small `Room`/registry type
(agent-ID-keyed, hands out per-pair loopback signaling + `rtc.Peer`
instances on join). Everything else — connection lifecycle, codec, track
I/O, recording taps — is identical to Option A and already exists.
