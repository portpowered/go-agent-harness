package agentruntime

import (
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
)

// roomTimeline is a non-owning marker retained for the room runner's existing
// admission guard. The runtime service owns the timeline file and ordering.
type roomTimeline struct{ recorder roomevidence.Recorder }

// roomTimelineEntry remains a local decode fixture for replay tests. Runtime
// recording serializes the same fields behind the host-neutral service.
type roomTimelineEntry struct {
	TOffsetMS   float64           `json:"t_offset_ms"`
	TUnixMS     int64             `json:"t_unix_ms"`
	Event       string            `json:"event"`
	Participant string            `json:"participant,omitempty"`
	Fields      map[string]string `json:"fields,omitempty"`
}

func (p *roomParticipantEvidence) RecordSessionDiagnostic(record SessionDiagnosticRecord) {
	if p == nil {
		return
	}
	if p.service == nil {
		if err := p.recordError(p.artifacts.Diagnostics, errors.New("room evidence service is not initialized")); err != nil {
			return
		}
		return
	}
	if err := p.service.RecordDiagnostic(roomevidence.DiagnosticRecord{Event: record.Event, Fields: record.Fields}); err != nil {
		if recordErr := p.recordError(p.artifacts.Diagnostics, err); recordErr != nil {
			return
		}
	}
}

func (p *roomParticipantEvidence) observeDelta(message messages.StreamMessage) error {
	if p == nil || p.service == nil {
		return errors.New("room participant delta sink is not initialized")
	}
	return p.service.ObserveDelta(message)
}

func (p *roomParticipantEvidence) observeAudio(pcm []byte) error {
	if p == nil || p.service == nil {
		return errors.New("room participant WAV sink is not initialized")
	}
	return p.service.ObserveAudio(pcm)
}

func (p *roomParticipantEvidence) observeSentAudio(pcm []byte) error {
	if p == nil || p.service == nil {
		return errors.New("room participant sent-audio sink is not initialized")
	}
	return p.service.ObserveSentAudio(pcm)
}

func (p *roomParticipantEvidence) observeSentStream(pcm []byte) error {
	if p == nil || p.service == nil {
		return errors.New("room participant sent-audio sink is not initialized")
	}
	return p.service.ObserveSentStream(pcm)
}

func (p *roomParticipantEvidence) closeSentSpeechSegment() {
	if p != nil && p.service != nil {
		if err := p.service.CloseSentSpeechSegment(); err != nil {
			if recordErr := p.recordError(p.artifacts.SentPCM, err); recordErr != nil {
				return
			}
		}
	}
}

func (p *roomParticipantEvidence) observeReceivedAudio(pcm []byte) error {
	if p == nil || p.service == nil {
		return errors.New("room participant received-audio sink is not initialized")
	}
	return p.service.ObserveReceivedAudio(pcm)
}

func (p *roomParticipantEvidence) recordAudioDropped(reason string, bytes int) {
	if p == nil {
		return
	}
	fields := map[string]string{"reason": reason, "bytes": fmt.Sprintf("%d", bytes)}
	p.RecordSessionDiagnostic(SessionDiagnosticRecord{Event: "room.audio.input_dropped", Fields: fields})
	if p.owner != nil {
		p.owner.recordTimelineEvent("audio_input_dropped", p.id, fields)
	}
}
