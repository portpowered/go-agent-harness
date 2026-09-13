package service

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

func (p *participantRecorder) ID() string {
	if p == nil {
		return ""
	}
	return p.id
}

func (p *participantRecorder) Artifacts() roomevidence.ArtifactPaths {
	if p == nil {
		return roomevidence.ArtifactPaths{}
	}
	return p.artifacts
}

func (p *participantRecorder) RecordDiagnostic(record roomevidence.DiagnosticRecord) error {
	if err := p.openCheck(); err != nil {
		return err
	}
	at := record.At
	if at.IsZero() {
		at = p.owner.clock.source.Now().UTC()
	}
	data, err := json.Marshal(diagnosticRecord{Event: record.Event, Fields: cloneFields(record.Fields)})
	if err != nil {
		return p.MarkError(p.artifacts.Diagnostics, err)
	}
	data = redactJSON(data, p.owner.secrets)
	data, err = stampJSONAt(data, p.owner.clock, at)
	if err != nil {
		return p.MarkError(p.artifacts.Diagnostics, err)
	}
	if err := p.diagnostics.writeRaw(data); err != nil {
		return p.MarkError(p.artifacts.Diagnostics, err)
	}
	return nil
}

func (p *participantRecorder) ObserveDelta(message messages.StreamMessage) error {
	if err := p.openCheck(); err != nil {
		return err
	}
	data, err := marshalStreamMessage(message)
	if err != nil {
		return p.MarkError(p.artifacts.Deltas, err)
	}
	data = redactJSON(data, p.owner.secrets)
	data, err = stampJSON(data, p.owner.clock)
	if err != nil {
		return p.MarkError(p.artifacts.Deltas, err)
	}
	deltaErr := p.MarkError(p.artifacts.Deltas, p.deltas.writeRaw(data))
	eventErr := p.MarkError(p.artifacts.Events, p.events.writeRaw(data))
	return errors.Join(deltaErr, eventErr)
}

func (p *participantRecorder) ObserveAudio(pcm []byte) error {
	if err := p.openCheck(); err != nil {
		return err
	}
	if _, err := audioSamples(pcm); err != nil {
		return p.MarkError(p.artifacts.WAV, err)
	}
	return p.MarkError(p.artifacts.WAV, p.wav.write(pcm))
}

func (p *participantRecorder) ObserveSentAudio(pcm []byte) error {
	return errors.Join(p.ObserveAudio(pcm), p.ObserveSentStream(pcm))
}

func (p *participantRecorder) ObserveSentStream(pcm []byte) error {
	if err := p.openCheck(); err != nil {
		return err
	}
	samples, err := audioSamples(pcm)
	if err != nil {
		return p.MarkError(p.artifacts.SentPCM, err)
	}
	writeErr := p.MarkError(p.artifacts.SentPCM, p.sentPCM.write(pcm))
	offset, _ := p.owner.clock.now()
	mixErr := p.owner.mix.Add(offset, samples)
	if mixErr != nil {
		p.owner.recordError("", roomevidence.MixPath, mixErr)
	}
	if event := p.sentSpeech.transition(audio.PCM16HasSignal(pcm)); event != "" {
		if err := p.owner.RecordTimeline("speech_"+event, p.id, nil); err != nil && !errors.Is(err, roomevidence.ErrFinalized) {
			p.owner.recordError("", roomevidence.TimelinePath, err)
		}
	}
	return errors.Join(writeErr, mixErr)
}

func (p *participantRecorder) CloseSentSpeechSegment() error {
	if p == nil || p.sentSpeech == nil {
		return nil
	}
	if event := p.sentSpeech.transition(false); event != "" && p.owner != nil {
		return p.owner.RecordTimeline("speech_"+event, p.id, nil)
	}
	return nil
}

func (p *participantRecorder) ObserveReceivedAudio(pcm []byte) error {
	if err := p.openCheck(); err != nil {
		return err
	}
	if _, err := audioSamples(pcm); err != nil {
		return p.MarkError(p.artifacts.ReceivedPCM, err)
	}
	writeErr := p.MarkError(p.artifacts.ReceivedPCM, p.receivedPCM.write(pcm))
	if event := p.receivedSpeech.transition(audio.PCM16HasSignal(pcm)); event != "" {
		return errors.Join(writeErr, p.owner.RecordTimeline("received_speech_"+event, p.id, nil))
	}
	return writeErr
}

func (p *participantRecorder) RecordAudioDropped(reason string, bytes int) error {
	if p == nil || p.owner == nil {
		return roomevidence.ErrRecorderClosed
	}
	fields := map[string]string{"reason": reason, "bytes": fmt.Sprintf("%d", bytes)}
	err := p.RecordDiagnostic(roomevidence.DiagnosticRecord{Event: "room.audio.input_dropped", Fields: fields})
	return errors.Join(err, p.owner.RecordTimeline("audio_input_dropped", p.id, fields))
}

func (p *participantRecorder) MarkError(artifact string, err error) error {
	if err != nil && p != nil && p.owner != nil {
		p.owner.recordError(p.id, artifact, err)
	}
	return err
}

func (p *participantRecorder) openCheck() error {
	if p == nil || p.owner == nil {
		return roomevidence.ErrRecorderClosed
	}
	return p.owner.checkOpen()
}
