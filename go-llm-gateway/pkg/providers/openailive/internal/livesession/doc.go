// Package livesession is the GPT-Live session state machine shared by both
// wire dialects of the "openai-live" provider: the public gpt-live-1
// vocabulary over the primary WebSocket (package openailive) and the
// quicksilver dialect of gpt-live-1-codex over WebRTC plus a sideband
// (package openailive/codexlive).
//
// A Session runs on the shared realtime skeleton over one transport.Conn. It
// owns what the two dialects have in common:
//
//   - synthesized speech segments (MESSAGE.START .. MESSAGE.END, ids
//     live_seg_<n>) and user utterances (INPUT_ITEM.ADDED .. TRANSCRIPT.END,
//     ids live_utt_<n>), closed by provider boundaries or by a quiet gap on
//     the injected clock (segments.go);
//   - one ordered outbox for the read loop, the idle watcher and
//     RESPONSE.CANCEL, drained by a pump with lossless backpressure until the
//     close handshake starts (outbox.go);
//   - the fail-closed outbound mapping, the full-duplex capabilities and the
//     close handshake with its terminal mapping (outbound.go, session.go).
//
// A Dialect supplies the rest: it decodes each server frame into calls on an
// Inbound, and builds the input-audio and close events of its wire.
package livesession

import (
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

// AudioTypePCM is mono signed 16-bit little-endian PCM. RTC media and the
// odd-byte carry apply only to it; G.711 bytes pass through unchanged.
const AudioTypePCM = "audio/pcm"

// Session close reasons. They are the public GPT-Live session.closed
// reasons; a dialect without a close event maps its transport ends onto
// them, and any other reason is reported verbatim as a provider close.
const (
	CloseReasonCloseRequested = "close_requested"
	CloseReasonExpired        = "expired"
	CloseReasonContent        = "content"
	CloseReasonRemoteHangup   = "remote_hangup"
	CloseReasonConnectionLost = "connection_lost"
)

// Format is the session audio format, shared by input and output.
type Format struct {
	// Type is AudioTypePCM or a G.711 media type.
	Type string
	// Rate is the sample rate in Hz.
	Rate int
}

// Transcript is one transcript fragment. StartMS and EndMS are the server
// timeline in milliseconds; a dialect without timing leaves them zero, so
// only the clock and provider boundaries close runs.
type Transcript struct {
	Delta          string
	StartMS, EndMS int64
}

// Dialect is the wire-specific part of a Session.
type Dialect interface {
	// Inbound decodes one server frame and maps it through in. It runs under
	// the session lock: it must not block or perform I/O.
	Inbound(frame []byte, in *Inbound)
	// InputAudio builds the wire event that carries one chunk of input audio
	// in the session format (PCM chunks hold whole samples).
	InputAudio(audio []byte) models.SessionEvent
	// CloseEvent is the event that starts the close handshake.
	CloseEvent() models.SessionEvent
}

// InputEnder is an optional Dialect extension for a transport that buffers
// input audio: InputEnd is queued after a user turn's audio when the turn
// ends (MESSAGE.END), so the transport sends what it holds. It is local to
// the transport and never asks the model to respond.
type InputEnder interface {
	InputEnd() models.SessionEvent
}
