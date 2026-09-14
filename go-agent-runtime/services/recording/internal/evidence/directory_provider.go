package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
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
		if r != nil && r.options.ProviderCapturePath == "" && (len(r.inputPaths) == 0 && len(r.outputPaths) == 0 || r.runtimeAudio) {
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

func (r *directoryRecorder) rotateAudioFile(direction session.LiveRecordDirection) error {
	file, offset := &r.outputFile, &r.outputSegmentBytes
	if direction == session.LiveRecordClient {
		file, offset = &r.inputFile, &r.inputSegmentBytes
	}
	if *file == nil {
		return nil
	}
	err := (*file).Sync()
	err = errors.Join(err, (*file).Close())
	*file = nil
	*offset = 0
	return err
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

type browserEventInput struct {
	Type               string
	BrowserID          string
	TargetID           string
	Generation         uint64
	RequiresGeneration bool
	Payload            map[string]any
	ToolName           string
}

func browserEventInputs(event recording.BrowserEvent, options recording.BrowserRecordingOptions) ([]browserEventInput, error) {
	if strings.TrimSpace(event.BrowserID) == "" || strings.TrimSpace(event.TargetID) == "" {
		return nil, nil
	}
	add := func(eventType string, generation uint64, requiresGeneration bool, payload map[string]any) browserEventInput {
		return browserEventInput{Type: eventType, BrowserID: event.BrowserID, TargetID: event.TargetID, Generation: generation, RequiresGeneration: requiresGeneration, Payload: payload, ToolName: event.ToolName}
	}
	toolNames := append([]string(nil), event.ToolNames...)
	switch event.Type {
	case "target_attached":
		return []browserEventInput{add("browser.chrome.target_attached", 0, false, map[string]any{"phase": "attached"})}, nil
	case "tools_added":
		count := len(toolNames)
		if event.ToolCountKnown {
			count = event.ToolCount
		}
		return []browserEventInput{add("browser.catalog.tool_added", event.Generation, true, map[string]any{"tools": toolNames, "tool_count": count})}, nil
	case "tools_removed":
		return []browserEventInput{add("browser.catalog.tool_removed", event.Generation, true, map[string]any{"tools": append([]string(nil), event.RemovedToolNames...)})}, nil
	case "catalog_ready":
		count := event.ToolCount
		if !event.ToolCountKnown {
			count = len(toolNames)
		}
		return []browserEventInput{add("browser.catalog.ready", event.Generation, true, map[string]any{"tool_count": count, "schema_digest": browserSchemaDigest(toolNames)})}, nil
	case "tool_invoked":
		return browserInvocationInputs(event, options, add)
	case "tool_responded":
		return browserResponseInputs(event, options, add)
	case "page_navigated", "frame_navigated":
		reason := strings.TrimSpace(event.Reason)
		if reason == "" {
			reason = "navigation"
		}
		return []browserEventInput{add("browser.page.generation_changed", 0, false, map[string]any{"previous_generation": event.PreviousGeneration, "current_generation": event.Generation, "reason": reason})}, nil
	case "target_detached", "browser_disconnected":
		return []browserEventInput{add("browser.target.detached", 0, false, map[string]any{"reason": event.Reason})}, nil
	case "session_closed":
		return []browserEventInput{add("browser.chrome.target_closed", 0, false, map[string]any{"reason": event.Reason})}, nil
	default:
		return nil, nil
	}
}

func browserInvocationInputs(event recording.BrowserEvent, options recording.BrowserRecordingOptions, add func(string, uint64, bool, map[string]any) browserEventInput) ([]browserEventInput, error) {
	if strings.TrimSpace(event.InvocationID) == "" {
		return nil, nil
	}
	created := map[string]any{"invocation_id": event.InvocationID, "tool_name": event.ToolName}
	if event.FrameID != "" {
		created["frame_id"] = event.FrameID
	}
	dispatched := map[string]any{"invocation_id": event.InvocationID}
	if options.IncludeArguments {
		dispatched["input"] = browserRaw(event.Input)
	}
	return []browserEventInput{
		add("browser.invocation.created", event.Generation, true, created),
		add("browser.invocation.dispatched", event.Generation, true, dispatched),
	}, nil
}

func browserResponseInputs(event recording.BrowserEvent, options recording.BrowserRecordingOptions, add func(string, uint64, bool, map[string]any) browserEventInput) ([]browserEventInput, error) {
	if strings.TrimSpace(event.InvocationID) == "" {
		return nil, nil
	}
	status := strings.ToLower(strings.TrimSpace(event.Status))
	reason := strings.TrimSpace(event.Reason)
	if reason == "" {
		reason = status
	}
	switch status {
	case "canceled", "cancelled", "timed_out", "timeout", "timedout":
		return []browserEventInput{add("browser.invocation.canceled", event.Generation, true, map[string]any{"invocation_id": event.InvocationID, "source": "browser", "reason": reason})}, nil
	case "error", "failed":
		return []browserEventInput{add("browser.invocation.error", event.Generation, true, browserErrorFields(event, options, reason))}, nil
	default:
		statusValue := event.Status
		if statusValue == "" {
			statusValue = "completed"
		}
		fields := map[string]any{"invocation_id": event.InvocationID, "status": statusValue}
		if options.IncludeResults {
			fields["output"] = browserRaw(event.Output)
		}
		return []browserEventInput{add("browser.invocation.completed", event.Generation, true, fields)}, nil
	}
}

func browserErrorFields(event recording.BrowserEvent, options recording.BrowserRecordingOptions, reason string) map[string]any {
	fields := map[string]any{"invocation_id": event.InvocationID, "code": browserErrorCode(event)}
	if options.IncludeResults && reason != "" {
		fields["message"] = reason
	}
	if options.IncludeResults && len(event.Output) > 0 {
		fields["error"] = browserRaw(event.Output)
	}
	return fields
}

func browserRaw(raw []byte) any {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return json.RawMessage(append([]byte(nil), raw...))
}

func browserSchemaDigest(toolNames []string) string {
	hash := sha256.New()
	for _, name := range toolNames {
		hash.Write([]byte(name))
		hash.Write([]byte{0})
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func browserErrorCode(event recording.BrowserEvent) string {
	if code := strings.TrimSpace(event.ErrorCode); code != "" {
		return code
	}
	return "invocation_error"
}
