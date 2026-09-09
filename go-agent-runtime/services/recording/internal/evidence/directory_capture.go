package evidence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"io"
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
	clientOffset, err := spoolOffset(r.client)
	if err != nil {
		return recordingWriteError("inspect client transcript", err)
	}
	if _, err := spoolOffset(r.agent); err != nil {
		return recordingWriteError("inspect agent transcript", err)
	}
	if err := r.writeCompleteSpool(r.client, client); err != nil {
		return recordingWriteError("write client transcript", err)
	}
	if err := r.writeCompleteSpool(r.agent, agent); err != nil {
		var writeFailure *spoolWriteError
		if errors.As(err, &writeFailure) && writeFailure.partial {
			if rollbackErr := rollbackSpoolFile(r.client, clientOffset); rollbackErr != nil {
				return errors.Join(recordingWriteError("write agent transcript", err), rollbackErr)
			}
		}
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
	var audioFile *os.File
	var audioOffset *uint64
	var audioStart uint64
	var audioCreated bool
	if len(data) > 0 {
		pathCount := len(r.outputPaths)
		if item.direction == session.LiveRecordClient {
			pathCount = len(r.inputPaths)
		}
		file, _, offset, fileErr := r.audioFile(item.direction)
		if fileErr != nil {
			r.workerErr = fileErr
			return
		}
		audioFile, audioOffset, audioStart = file, offset, *offset
		if item.direction == session.LiveRecordClient {
			audioCreated = len(r.inputPaths) > pathCount
		} else {
			audioCreated = len(r.outputPaths) > pathCount
		}
		if err := r.writeCompleteSpool(audioFile, data); err != nil {
			r.workerErr = errors.Join(recordingWriteError("write audio evidence", err), r.rollbackAudioAttempt(item.direction, audioFile, audioOffset, audioStart, audioCreated))
			return
		}
		*audioOffset += uint64(len(data))
	}
	if err := r.writeTranscriptRecords(client, agent, sequence); err != nil {
		r.workerErr = errors.Join(err, r.rollbackAudioAttempt(item.direction, audioFile, audioOffset, audioStart, audioCreated))
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

func (r *directoryRecorder) writeCompleteSpool(file *os.File, data []byte) error {
	return writeCompleteSpoolWithWriter(file, r.writeSpool, data)
}

type spoolWriteError struct {
	err     error
	partial bool
}

func (e *spoolWriteError) Error() string {
	if e == nil || e.err == nil {
		return "recording spool write failed"
	}
	return e.err.Error()
}

func (e *spoolWriteError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func writeCompleteSpoolWithWriter(file *os.File, writer func(*os.File, []byte) error, data []byte) error {
	start, err := spoolOffset(file)
	if err != nil {
		return err
	}
	if writer == nil {
		return errors.New("recording spool writer is unavailable")
	}
	writeErr := writer(file, data)
	end, endErr := spoolOffset(file)
	if writeErr == nil && endErr == nil && end == start+int64(len(data)) {
		return nil
	}
	if writeErr == nil && endErr == nil {
		writeErr = io.ErrShortWrite
	}
	return &spoolWriteError{
		err:     errors.Join(writeErr, endErr, rollbackSpoolFile(file, start)),
		partial: endErr == nil && end > start,
	}
}

func spoolOffset(file *os.File) (int64, error) {
	if file == nil {
		return 0, errors.New("recording spool is not open")
	}
	return file.Seek(0, io.SeekCurrent)
}

func rollbackSpoolFile(file *os.File, offset int64) error {
	if file == nil {
		return nil
	}
	if offset < 0 {
		return errors.New("recording spool rollback offset is negative")
	}
	if err := file.Truncate(offset); err != nil {
		return fmt.Errorf("truncate recording spool: %w", err)
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return fmt.Errorf("seek recording spool: %w", err)
	}
	return nil
}

func (r *directoryRecorder) rollbackAudioAttempt(direction session.LiveRecordDirection, file *os.File, offset *uint64, start uint64, created bool) error {
	if file == nil || offset == nil {
		return nil
	}
	result := rollbackSpoolFile(file, int64(start))
	*offset = start
	if !created || start != 0 {
		return result
	}
	if err := file.Close(); err != nil {
		result = errors.Join(result, err)
	}
	path := filepath.Join(r.spool, "out.pcm")
	if direction == session.LiveRecordClient {
		path = filepath.Join(r.spool, "in.pcm")
		r.inputFile = nil
		if len(r.inputPaths) > 0 {
			r.inputPaths = r.inputPaths[:len(r.inputPaths)-1]
		}
	} else {
		r.outputFile = nil
		if len(r.outputPaths) > 0 {
			r.outputPaths = r.outputPaths[:len(r.outputPaths)-1]
		}
	}
	removeErr := os.Remove(path)
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	return errors.Join(result, removeErr)
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
