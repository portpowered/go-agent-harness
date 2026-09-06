package evidence

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"os"
	"path/filepath"
)

// Files and conversation state belong exclusively to the drain worker. The
// admission mutex is never held across encoding or filesystem operations.
func (r *directoryRecorder) processMessage(item directoryEvidenceItem) {
	if r.workerErr != nil {
		return
	}
	if err := r.writeTranscript(item, transcript.StreamRuntimeMessage, item.payload); err != nil {
		r.workerErr = err
		return
	}
	message, err := gatewaytesting.UnmarshalStreamMessage(item.payload)
	if err != nil {
		// Keep the original admitted payload and subsequent PCM even when a
		// convenience projection cannot interpret a newly introduced type.
		r.latch(recordingWriteError("decode admitted stream message", err))
		return
	}
	r.conversation.observe(message, item.direction == session.LiveRecordClient, r.sequence)
}

func (r *directoryRecorder) writeTranscript(item directoryEvidenceItem, stream transcript.Stream, payload []byte) error {
	if err := r.ensureTranscriptFiles(); err != nil {
		return err
	}
	r.sequence++
	clientDirection, agentDirection := transcript.DirectionIn, transcript.DirectionOut
	if item.direction == session.LiveRecordClient {
		clientDirection, agentDirection = transcript.DirectionOut, transcript.DirectionIn
	}
	client, clientErr := transcript.Encode(transcript.NewRecord(r.sequence, item.timestamp, transcript.PeerClient, clientDirection, stream, payload))
	agent, agentErr := transcript.Encode(transcript.NewRecord(r.sequence, item.timestamp, transcript.PeerAgent, agentDirection, stream, payload))
	if err := errors.Join(clientErr, agentErr); err != nil {
		return recordingWriteError("encode transcript frame", err)
	}
	if err := r.writeSpool(r.client, client); err != nil {
		return recordingWriteError("write client transcript", err)
	}
	if err := r.writeSpool(r.agent, agent); err != nil {
		return recordingWriteError("write agent transcript", err)
	}
	return nil
}

type evidenceAudioBoundary struct {
	Kind        string                     `json:"kind"`
	Segment     string                     `json:"segment"`
	ByteOffset  uint64                     `json:"byte_offset"`
	SampleCount int                        `json:"sample_count"`
	Admission   session.LiveAudioAdmission `json:"admission,omitempty"`
	Frame       sharedaudio.PCMFrame       `json:"frame"`
}

func (r *directoryRecorder) processAudio(item directoryEvidenceItem) {
	if r.workerErr != nil {
		return
	}
	if len(item.frame.Samples) == 0 {
		r.workerErr = r.writeAudioBoundary(item, "", 0)
		return
	}
	file, segment, offset, err := r.audioFile(item.direction)
	if err != nil {
		r.workerErr = err
		return
	}
	data := codec.EncodePCM16(item.frame.Samples)
	if err := r.writeSpool(file, data); err != nil {
		r.workerErr = recordingWriteError("write audio evidence", err)
		return
	}
	if err := r.writeAudioBoundary(item, segment, *offset); err != nil {
		r.workerErr = err
		return
	}
	*offset += uint64(len(data))
	r.conversation.observeAudio(item.direction == session.LiveRecordClient, 0, len(data), item.timestamp)
}

func (r *directoryRecorder) writeAudioBoundary(item directoryEvidenceItem, segment string, offset uint64) error {
	frame := item.frame
	frame.Samples = nil
	boundary := evidenceAudioBoundary{Kind: "audio.frame", Segment: segment, ByteOffset: offset, SampleCount: len(item.frame.Samples), Admission: item.admission, Frame: frame}
	payload, err := json.Marshal(boundary)
	if err == nil {
		err = r.writeTranscript(item, transcript.StreamRuntimeAudio, payload)
	}
	if err != nil {
		return recordingWriteError("write audio boundary", err)
	}
	return nil
}

func (r *directoryRecorder) audioFile(direction session.LiveRecordDirection) (*os.File, string, *uint64, error) {
	file, paths, offset, name := &r.outputFile, &r.outputPaths, &r.outputBytes, "out"
	if direction == session.LiveRecordClient {
		file, paths, offset, name = &r.inputFile, &r.inputPaths, &r.inputBytes, "in"
	}
	segment := "audio/" + name + "-000.pcm"
	if *file == nil {
		path := filepath.Join(r.spool, name+".pcm")
		opened, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, evidenceFileMode)
		if err != nil {
			return nil, "", nil, recordingWriteError("create audio spool", err)
		}
		*file = opened
		*paths = []string{path}
	}
	return *file, segment, offset, nil
}

func (r *directoryRecorder) ensureTranscriptFiles() error {
	if r.client != nil && r.agent != nil {
		return nil
	}
	clientPath, agentPath := filepath.Join(r.spool, "client.transcript.jsonl"), filepath.Join(r.spool, "agent.transcript.jsonl")
	client, err := os.OpenFile(clientPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, evidenceFileMode)
	if err != nil {
		return evidenceDestinationError(r.destination, "create client transcript spool", err)
	}
	agent, err := os.OpenFile(agentPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, evidenceFileMode)
	if err != nil {
		return errors.Join(evidenceDestinationError(r.destination, "create agent transcript spool", err), client.Close())
	}
	r.client, r.agent = client, agent
	r.clientPath, r.agentPath = clientPath, agentPath
	return nil
}

func (r *directoryRecorder) latch(err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	r.latchLocked(err)
	r.mu.Unlock()
}

func (r *directoryRecorder) latchLocked(err error) {
	if err != nil && r.recordErr == nil {
		r.recordErr = err
	}
}

func recordingWriteError(operation string, cause error) error {
	return &transcript.RecordingError{Kind: transcript.ErrRecordingWrite, Operation: operation, Cause: cause}
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func writeAll(file *os.File, data []byte) error {
	if file == nil {
		return errors.New("recording spool is not open")
	}
	for len(data) > 0 {
		written, err := file.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(data) {
			return errors.New("recording spool write made no progress")
		}
		data = data[written:]
	}
	return nil
}

func terminalSummary(value *messages.SessionCloseValue) *transcript.RecordingTerminalSummary {
	if value == nil {
		return nil
	}
	classification := value.Classification
	if classification == "" {
		classification = string(value.TerminalReason)
	}
	reason := value.Reason
	if reason == "" {
		reason = classification
	}
	return &transcript.RecordingTerminalSummary{
		Reason: reason, Classification: classification,
		TerminalReason: value.TerminalReason, TerminalProvenance: value.TerminalProvenance,
		OutputState: value.OutputState,
	}
}
