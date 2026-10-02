// Package quicksilver speaks the older GPT-Live wire dialect that the
// ChatGPT-credential route uses: model gpt-live-1-codex, a WebRTC call created
// on the ChatGPT backend, and a control sideband on
// wss://api.openai.com/v1/live/{call_id}. Codex calls the dialect "frameless
// bidi"; its connection header is "OpenAI-Alpha: quicksilver=v2".
//
// The dialect is not the public GPT-Live vocabulary of the parent openailive
// package. It has no session. prefix on most events, no session.start (the
// session goes in the call-creation request, and a plain WebSocket sends
// session.update), and delegation results go back as context appends with a
// channel:
//
//   - client events: input_audio.append, session.update,
//     session.context.append, delegation.context.append, session.close;
//   - server events: session.started, session.updated, output_audio.delta,
//     input_transcript.added, output_transcript.added, turn.done,
//     delegation.created, output_audio_buffer.cleared, error.
//
// This package holds the wire layer only: typed events (events.go), a codec
// whose unknown types decode to UnknownEvent (codec.go), and the session
// builder used for call creation and session.update (session.go). The
// transport (call creation, the sideband and the WebRTC peer) is the
// sibling codexrtc package. No production session reaches either yet.
//
// Sources: Codex codex-rs (revision 1e6185e522)
// codex-api/src/endpoint/realtime_websocket/{protocol.rs,
// protocol_frameless_bidi.rs, methods_frameless_bidi.rs} and OpenClaw
// (revision 0dc63ee) extensions/openai/realtime-quicksilver-{events,wire,
// protocol}.ts. The design is docs/architecture/chatgpt-oauth.md section 3.2.
package quicksilver
