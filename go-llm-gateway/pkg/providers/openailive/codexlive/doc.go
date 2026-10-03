// Package codexlive is the gpt-live-1-codex session of the "openai-live"
// provider: GPT-Live on a ChatGPT login (docs/architecture/chatgpt-oauth.md
// route C, docs/architecture/gpt-live-provider.md PR 9).
//
// ConnectSession creates a WebRTC offer with a codexrtc.Peer, creates the call
// on the ChatGPT backend with that offer and the quicksilver session, applies
// the SDP answer, dials the control sideband, and waits for the media
// connection. The session then runs on the same state machine as the public
// gpt-live-1 session (openailive/internal/livesession): the same speech
// segments, ordered outbox, full-duplex capabilities and close handshake. Only
// the transport and the dialect differ:
//
//   - Audio travels as Opus over the peer. Input PCM at the session rate
//     (24 kHz by default) is resampled to the peer's 48 kHz mono, cut into
//     20 ms frames and paced at 20 ms on the injected clock. Output frames are
//     resampled back to the session rate; inbound sequence gaps are concealed
//     by Opus packet-loss concealment in the peer.
//   - The sideband carries the quicksilver dialect. Transcripts, turn.done,
//     output_audio_buffer.cleared and errors map onto the harness stream;
//     delegation.created is logged and ignored, as on the public route.
//   - A sideband that drops is redialed with backoff, as Codex and OpenClaw
//     do; a credential the backend rejects (invalid_token,
//     authentication_error, token_expired, or HTTP 401) ends the session with
//     a terminal error that says to sign in again.
//
// The transport is presented to the state machine as one transport.Conn
// (conn.go), so the skeleton's read and write loops drive it unchanged.
package codexlive
