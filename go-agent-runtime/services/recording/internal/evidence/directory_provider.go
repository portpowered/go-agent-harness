package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
)

func (r *directoryRecorder) ProviderCapturePath() string {
	if r == nil {
		return ""
	}
	if r.options.ProviderCapturePath != "" {
		return r.options.ProviderCapturePath
	}
	return filepath.Join(r.spool, "provider.json")
}

// providerArtifact fingerprints the completed source without allocating a
// second capture-sized buffer. Bundle staging streams it again and checks
// this digest, rejecting source changes or redaction that would invalidate a
// provider capture's own integrity envelope.
func (r *directoryRecorder) providerArtifact() (transcript.RecordingArtifact, bool, error) {
	path := r.ProviderCapturePath()
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		if r != nil && !r.options.ProviderCaptureRequired && r.options.ProviderCapturePath == "" && (len(r.inputPaths) == 0 && len(r.outputPaths) == 0 || r.runtimeAudio) {
			// A semantic-only injected session may have no raw wire writer. Once
			// PCM is observed, missing provider evidence is incomplete.
			return transcript.RecordingArtifact{}, false, nil
		}
		return transcript.RecordingArtifact{}, false, recordingWriteError("finalize provider evidence", err)
	}
	if err != nil {
		return transcript.RecordingArtifact{}, false, err
	}
	info, statErr := file.Stat()
	if statErr != nil {
		return transcript.RecordingArtifact{}, false, errors.Join(statErr, file.Close())
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return transcript.RecordingArtifact{}, false, errors.Join(errors.New("provider capture must be a non-empty regular file"), file.Close())
	}
	digest := sha256.New()
	_, copyErr := io.Copy(digest, file)
	if err := errors.Join(copyErr, file.Close()); err != nil {
		return transcript.RecordingArtifact{}, false, err
	}
	return transcript.RecordingArtifact{Path: "provider.json", SourcePath: path, SHA256: hex.EncodeToString(digest.Sum(nil))}, true, nil
}

var _ recording.ProviderCapture = (*directoryRecorder)(nil)

func (r *directoryRecorder) captureUsage(processed bool) {
	if r == nil {
		return
	}
	r.usageMu.Lock()
	defer r.usageMu.Unlock()
	r.usage.TranscriptBytes = r.budget.transcriptBytes
	r.usage.TranscriptItems = r.budget.transcriptItems
	r.usage.AudioBytes = r.budget.audioBytes
	r.usage.AudioItems = r.budget.audioItems
	r.usage.SidecarBytes = r.budget.sidecarBytes
	r.usage.SidecarItems = r.budget.sidecarItems
	r.usage.MetadataBytes = r.budget.metadataBytes
	r.usage.MetadataItems = r.budget.metadataItems
	r.usage.TerminalBytes = r.budget.terminalBytes
	r.usage.TerminalItems = r.budget.terminalItems
	if r.conversation.budget != nil {
		r.usage.SummaryBytes = r.conversation.budget.bytes
		r.usage.SummaryItems = int64(r.conversation.budget.items)
		if r.usage.SummaryBytes > r.usage.PeakSummaryBytes {
			r.usage.PeakSummaryBytes = r.usage.SummaryBytes
		}
		if r.usage.SummaryItems > r.usage.PeakSummaryItems {
			r.usage.PeakSummaryItems = r.usage.SummaryItems
		}
	}
	if processed {
		r.usage.ProcessedItems++
	}
}

// ResourceUsage reports the last worker snapshot plus the current queue
// backlog. It never exposes the recorder's private spool path or caller data.
func (r *directoryRecorder) ResourceUsage() recording.ResourceUsage {
	if r == nil {
		return recording.ResourceUsage{}
	}
	r.mu.Lock()
	queuedBytes, queuedItems := r.queuedBytes, r.queuedItems
	r.mu.Unlock()
	r.usageMu.Lock()
	usage := r.usage
	usage.QueueBytes = queuedBytes
	usage.QueueItems = queuedItems
	r.usageMu.Unlock()
	return usage
}

var _ recording.ResourceUsageReporter = (*directoryRecorder)(nil)

func (r *directoryRecorder) processItem(item directoryEvidenceItem) {
	defer r.captureUsage(true)
	switch item.kind {
	case evidenceMessage:
		r.processMessage(item)
	case evidenceAudio:
		r.processAudio(item)
	case evidenceEvent:
		r.processEvent(item)
	}
}

func (r *directoryRecorder) processEvent(item directoryEvidenceItem) {
	if r.workerErr == nil {
		if err := r.writeTranscript(item, transcript.StreamRuntimeEvent, item.payload); err != nil && !isEvidenceBudgetError(err) {
			r.workerErr = err
		}
	}
	if item.terminal != nil {
		if err := r.writeDurationSidecarTerminal(item.timestamp, item.terminal); err != nil && r.workerErr == nil && !isEvidenceBudgetError(err) {
			r.workerErr = err
		}
	}
}

func (r *directoryRecorder) releaseQueueItem(item directoryEvidenceItem) {
	r.mu.Lock()
	r.queuedBytes -= item.bytes
	if r.queuedItems > 0 {
		r.queuedItems--
	}
	queuedBytes, queuedItems := r.queuedBytes, r.queuedItems
	r.mu.Unlock()
	r.usageMu.Lock()
	r.usage.QueueBytes = queuedBytes
	r.usage.QueueItems = queuedItems
	r.usageMu.Unlock()
}

func boolToInt64(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func (r *directoryRecorder) latchProjectionError() {
	if r == nil {
		return
	}
	if err := r.conversation.projectionError(); err != nil {
		r.latch(recordingWriteError("retain conversation summary", err))
	}
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

// Media and normalized messages have independent consumers. Join their summary
// by provider response identity at finalization, never by goroutine arrival order.
// Raw artifacts retain their actual admission order and exact PCM offsets.
type evidenceResponseAudio struct {
	bytes, offset uint64
	segment       string
}

func (c *evidenceConversation) trackResponse(id string) {
	if c == nil || c.summaryFull || id == "" || slices.Contains(c.turn.responseIDs, id) || !c.ensureTurn() {
		return
	}
	need := summarySliceEntryBytes + summaryCost(id)
	if !c.reserve(need, 1) {
		return
	}
	c.turn.responseIDs = append(c.turn.responseIDs, id)
}

func (c *evidenceConversation) recordResponseAudio(id string, count, offset uint64, segment string) {
	if c == nil || c.summaryFull || id == "" {
		return
	}
	c.ensureBudget()
	if c.responseAudio == nil {
		c.responseAudio = make(map[string]evidenceResponseAudio)
	}
	if _, exists := c.responseAudio[id]; !exists {
		need := summaryMapEntryBytes + summaryCost(id, segment)
		if !c.reserve(need, 1) {
			return
		}
	}
	audio := c.responseAudio[id]
	if audio.bytes == 0 {
		audio.offset = offset
		audio.segment = segment
	}
	audio.bytes += count
	c.responseAudio[id] = audio
}

func (c evidenceConversation) withResponseAudio(turn evidenceTurn) evidenceTurn {
	for _, id := range turn.responseIDs {
		audio := c.responseAudio[id]
		if audio.bytes == 0 {
			continue
		}
		if turn.outputAudio == 0 || audio.offset < turn.outputOffset {
			turn.outputOffset = audio.offset
		}
		turn.outputAudio += audio.bytes
		if !slices.Contains(turn.outputSegments, audio.segment) {
			turn.outputSegments = append(turn.outputSegments, audio.segment)
		}
	}
	return turn
}
