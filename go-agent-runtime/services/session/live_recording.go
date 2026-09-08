package session

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// LiveRecordDirection identifies the author of one provider-bound message or
// the direction of one media frame. It is deliberately independent of a
// transport protocol so recorders can use the same contract for WebSocket,
// WebRTC, and embedded provider sessions.
type LiveRecordDirection string

const (
	LiveRecordClient LiveRecordDirection = "client"
	LiveRecordAgent  LiveRecordDirection = "agent"
)

// LiveAudioAdmission identifies the runtime boundary at which an audio
// observation was accepted. Queue admission means the bounded loop input
// queue accepted the samples; it does not acknowledge provider transmission.
// Media bridging means the session media bridge accepted the frame for its
// provider or host endpoint.
type LiveAudioAdmission string

const (
	LiveAudioQueueAdmitted   LiveAudioAdmission = "queue_admitted"
	LiveAudioMediaBridged    LiveAudioAdmission = "media_bridged"
	LiveAudioMessageObserved LiveAudioAdmission = "message_observed"
)

// LiveRecord is one ordered stream observation at the runtime boundary. The
// message is copied by the recorder before this call returns; callers may
// reuse their message payload after admission.
type LiveRecord struct {
	Direction LiveRecordDirection
	Timestamp time.Time
	Message   messages.StreamMessage
}

// LiveAudioRecord is one media observation at an explicit bounded runtime
// boundary. Admission distinguishes local loop queue admission from media
// bridge admission, so a queue observation cannot be mistaken for provider
// transmission. Samples are copied by the recorder. Format is retained on the
// frame when a provider or device supplied negotiated media metadata.
type LiveAudioRecord struct {
	Direction LiveRecordDirection
	Admission LiveAudioAdmission
	Timestamp time.Time
	Frame     sharedaudio.PCMFrame
}

// LiveRecorder owns invocation evidence. RecordMessage, RecordAudio, and
// RecordEvent are called from provider, media, and event workers; an
// implementation must make each admission bounded and promptly return. A
// recorder may mark evidence incomplete when its bounded spool overflows, but
// it must preserve the terminal error through Finalize.
type LiveRecorder interface {
	RecordMessage(context.Context, LiveRecord) error
	RecordAudio(context.Context, LiveAudioRecord) error
	RecordEvent(context.Context, LiveEvent) error
	Finalize(context.Context, error) error
}
