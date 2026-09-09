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
		if !isEvidenceBudgetError(err) {
			r.workerErr = err
		}
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
	r.latchProjectionError()
}

func (r *directoryRecorder) writeTranscript(item directoryEvidenceItem, stream transcript.Stream, payload []byte) error {
	client, agent, sequence, err := r.encodeTranscript(item, stream, payload)
	if err != nil {
		return err
	}
	if err := r.budget.reserveTranscript(int64(len(client)+len(agent)), 1, item.terminal != nil); err != nil {
		r.latch(recordingWriteError("admit transcript evidence", err))
		return err
	}
	return r.writeTranscriptRecords(client, agent, sequence)
}

func (r *directoryRecorder) encodeTranscript(item directoryEvidenceItem, stream transcript.Stream, payload []byte) ([]byte, []byte, uint64, error) {
	sequence := r.sequence + 1
	clientDirection, agentDirection := transcript.DirectionIn, transcript.DirectionOut
	if item.direction == session.LiveRecordClient {
		clientDirection, agentDirection = transcript.DirectionOut, transcript.DirectionIn
	}
	client, clientErr := transcript.Encode(transcript.NewRecord(sequence, item.timestamp, transcript.PeerClient, clientDirection, stream, payload))
	agent, agentErr := transcript.Encode(transcript.NewRecord(sequence, item.timestamp, transcript.PeerAgent, agentDirection, stream, payload))
	if err := errors.Join(clientErr, agentErr); err != nil {
		return nil, nil, 0, recordingWriteError("encode transcript frame", err)
	}
	return client, agent, sequence, nil
}

func (r *directoryRecorder) writeTranscriptRecords(client, agent []byte, sequence uint64) error {
	if err := r.ensureTranscriptFiles(); err != nil {
		return err
	}
	if err := r.writeSpool(r.client, client); err != nil {
		return recordingWriteError("write client transcript", err)
	}
	if err := r.writeSpool(r.agent, agent); err != nil {
		return recordingWriteError("write agent transcript", err)
	}
	r.sequence = sequence
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
	data := codec.EncodePCM16(item.frame.Samples)
	segment, offset := r.audioLocation(item.direction)
	payload, err := json.Marshal(audioBoundary(item, segment, offset))
	if err != nil {
		r.workerErr = recordingWriteError("encode audio boundary", err)
		return
	}
	client, agent, sequence, err := r.encodeTranscript(item, transcript.StreamRuntimeAudio, payload)
	if err != nil {
		r.workerErr = err
		return
	}
	if err := r.budget.reserveAudioWithTranscript(int64(len(data)), int64(len(client)+len(agent)), boolToInt64(len(data) > 0)); err != nil {
		r.latch(recordingWriteError("admit audio evidence", err))
		return
	}
	if len(data) > 0 {
		file, _, audioOffset, fileErr := r.audioFile(item.direction)
		if fileErr != nil {
			r.workerErr = fileErr
			return
		}
		if err := r.writeSpool(file, data); err != nil {
			r.workerErr = recordingWriteError("write audio evidence", err)
			return
		}
		*audioOffset += uint64(len(data))
	}
	if err := r.writeTranscriptRecords(client, agent, sequence); err != nil {
		r.workerErr = err
		return
	}
	if item.direction == session.LiveRecordAgent && item.frame.PlaybackResponse.ResponseID != "" {
		r.conversation.recordResponseAudio(item.frame.PlaybackResponse.ResponseID, uint64(len(data)), offset, segment)
	} else {
		r.conversation.observeAudio(item.direction == session.LiveRecordClient, len(data), offset, segment)
	}
	r.latchProjectionError()
}

func audioBoundary(item directoryEvidenceItem, segment string, offset uint64) evidenceAudioBoundary {
	frame := item.frame
	sampleCount := len(frame.Samples)
	frame.Samples = nil
	return evidenceAudioBoundary{Kind: "audio.frame", Segment: segment, ByteOffset: offset, SampleCount: sampleCount, Admission: item.admission, Frame: frame}
}

func boolToInt64(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func (r *directoryRecorder) audioLocation(direction session.LiveRecordDirection) (string, uint64) {
	if direction == session.LiveRecordClient {
		return "audio/in-000.pcm", r.inputBytes
	}
	return "audio/out-000.pcm", r.outputBytes
}

func (r *directoryRecorder) latchProjectionError() {
	if r == nil {
		return
	}
	if err := r.conversation.projectionError(); err != nil {
		r.latch(recordingWriteError("retain conversation summary", err))
	}
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
		Reason: boundedEventText(reason), Classification: boundedEventText(classification),
		TerminalReason: value.TerminalReason, TerminalProvenance: value.TerminalProvenance,
		OutputState: value.OutputState,
	}
}
