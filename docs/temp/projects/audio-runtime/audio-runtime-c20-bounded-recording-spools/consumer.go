package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	recordingwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

type result struct {
	Case                 string            `json:"case"`
	FinalizationErr      string            `json:"finalization_error,omitempty"`
	ProviderErr          string            `json:"provider_error,omitempty"`
	Status               string            `json:"status,omitempty"`
	TranscriptLines      map[string]int    `json:"transcript_lines,omitempty"`
	FileBytes            map[string]int64  `json:"file_bytes,omitempty"`
	TotalBytes           int64             `json:"total_bytes"`
	ProviderBytes        int64             `json:"provider_bytes"`
	SidecarBytes         int64             `json:"sidecar_bytes"`
	SourceRevision       string            `json:"source_revision,omitempty"`
	LimitsApplied        map[string]int64  `json:"limits_applied,omitempty"`
	TranscriptSHA256     map[string]string `json:"transcript_sha256,omitempty"`
	ProviderSequences    []int             `json:"provider_sequences,omitempty"`
	ProviderCaptureValid bool              `json:"provider_capture_valid"`
	InputErrors          []string          `json:"input_errors,omitempty"`
	SemanticUsage        *usageSnapshot    `json:"semantic_usage,omitempty"`
	ProviderUsage        *usageSnapshot    `json:"provider_usage,omitempty"`
	DiskUsage            diskUsage         `json:"disk_usage"`
	Finalization         finalizationUsage `json:"finalization"`
	RepeatFinalize       string            `json:"repeat_finalization_error,omitempty"`
	RepeatProvider       string            `json:"repeat_provider_error,omitempty"`
}

type usageSnapshot struct {
	QueueBytes             int64 `json:"queue_bytes"`
	QueueItems             int64 `json:"queue_items"`
	PeakQueueBytes         int64 `json:"peak_queue_bytes"`
	PeakQueueItems         int64 `json:"peak_queue_items"`
	AcceptedItems          int64 `json:"accepted_items"`
	ProcessedItems         int64 `json:"processed_items"`
	AcceptedMessages       int64 `json:"accepted_messages"`
	AcceptedAudio          int64 `json:"accepted_audio"`
	AcceptedEvents         int64 `json:"accepted_events"`
	TranscriptBytes        int64 `json:"transcript_bytes"`
	TranscriptItems        int64 `json:"transcript_items"`
	AudioBytes             int64 `json:"audio_bytes"`
	AudioItems             int64 `json:"audio_items"`
	SidecarBytes           int64 `json:"sidecar_bytes"`
	SidecarItems           int64 `json:"sidecar_items"`
	MetadataBytes          int64 `json:"metadata_bytes"`
	MetadataItems          int64 `json:"metadata_items"`
	TerminalBytes          int64 `json:"terminal_bytes"`
	TerminalItems          int64 `json:"terminal_items"`
	SummaryBytes           int64 `json:"summary_bytes"`
	SummaryItems           int64 `json:"summary_items"`
	PeakSummaryBytes       int64 `json:"peak_summary_bytes"`
	PeakSummaryItems       int64 `json:"peak_summary_items"`
	ProviderQueueBytes     int64 `json:"provider_queue_bytes"`
	ProviderQueueItems     int64 `json:"provider_queue_items"`
	PeakProviderQueueBytes int64 `json:"peak_provider_queue_bytes"`
	PeakProviderQueueItems int64 `json:"peak_provider_queue_items"`
	ProviderAcceptedItems  int64 `json:"provider_accepted_items"`
	ProviderBytes          int64 `json:"provider_bytes"`
	ProviderItems          int64 `json:"provider_items"`
	PeakProviderBytes      int64 `json:"peak_provider_bytes"`
	PeakProviderItems      int64 `json:"peak_provider_items"`
}

type diskUsage struct {
	FinalBytes         int64 `json:"final_bytes"`
	PeakFinalBytes     int64 `json:"peak_final_bytes"`
	PeakStagingBytes   int64 `json:"peak_staging_bytes"`
	PeakTemporaryBytes int64 `json:"peak_temporary_bytes"`
	PeakOwnedBytes     int64 `json:"peak_owned_bytes"`
	Samples            int64 `json:"samples"`
}

type finalizationUsage struct {
	DurationMS      int64  `json:"duration_ms"`
	AllocatedBytes  uint64 `json:"allocated_bytes"`
	HeapAllocBefore uint64 `json:"heap_alloc_before"`
	HeapAllocAfter  uint64 `json:"heap_alloc_after"`
}

func main() {
	caseName := flag.String("case", "normal", "bounded recording consumer case")
	destination := flag.String("destination", "", "recording destination")
	sourceRevision := flag.String("source-revision", "", "source revision for evidence")
	transcriptLimit := flag.Int64("transcript-limit", 0, "optional public transcript byte limit")
	transcriptItems := flag.Int64("transcript-items", 0, "optional public transcript item limit")
	audioLimit := flag.Int64("audio-limit", 0, "optional public audio byte limit")
	audioItems := flag.Int64("audio-items", 0, "optional public audio item limit")
	providerLimit := flag.Int64("provider-limit", 0, "optional public provider byte limit")
	providerItems := flag.Int64("provider-items", 0, "optional public provider item limit")
	flag.Parse()
	if *destination == "" {
		fail(errors.New("destination is required"))
	}
	if err := run(*caseName, *destination, *sourceRevision, limits{transcriptBytes: *transcriptLimit, transcriptItems: *transcriptItems, audioBytes: *audioLimit, audioItems: *audioItems, providerBytes: *providerLimit, providerItems: *providerItems}); err != nil {
		fail(err)
	}
}

type limits struct {
	transcriptBytes int64
	transcriptItems int64
	audioBytes      int64
	audioItems      int64
	providerBytes   int64
	providerItems   int64
}

type diskMonitor struct {
	destination         string
	destinationParent   string
	destinationPrefix   string
	providerParent      string
	providerSpoolPrefix string
	providerStagePrefix string

	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
	sampleMu sync.Mutex
	mu       sync.Mutex
	usage    diskUsage
}

func newDiskMonitor(destination, providerPath string) *diskMonitor {
	providerParent := filepath.Dir(providerPath)
	providerBase := filepath.Base(providerPath)
	monitor := &diskMonitor{
		destination:         destination,
		destinationParent:   filepath.Dir(destination),
		destinationPrefix:   "." + filepath.Base(destination) + ".staging-",
		providerParent:      providerParent,
		providerSpoolPrefix: "." + providerBase + ".provider-spool-",
		providerStagePrefix: "." + providerBase + ".provider-publish-",
		stop:                make(chan struct{}),
		done:                make(chan struct{}),
	}
	monitor.sample()
	go monitor.run()
	return monitor
}

func (m *diskMonitor) run() {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer func() {
		ticker.Stop()
		close(m.done)
	}()
	for {
		select {
		case <-ticker.C:
			m.sample()
		case <-m.stop:
			return
		}
	}
}

func (m *diskMonitor) close() diskUsage {
	if m == nil {
		return diskUsage{}
	}
	m.stopOnce.Do(func() { close(m.stop) })
	<-m.done
	m.sample()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.usage
}

func (m *diskMonitor) sample() {
	if m == nil {
		return
	}
	m.sampleMu.Lock()
	defer m.sampleMu.Unlock()
	finalBytes := treeBytes(m.destination)
	stagingBytes := matchingTreeBytes(m.destinationParent, m.destinationPrefix) + matchingTreeBytes(m.providerParent, m.providerStagePrefix)
	temporaryBytes := matchingTreeBytes(os.TempDir(), ".go-agent-runtime-recording-") + matchingTreeBytes(m.providerParent, m.providerSpoolPrefix)
	ownedBytes := finalBytes + stagingBytes + temporaryBytes
	m.mu.Lock()
	m.usage.FinalBytes = finalBytes
	if finalBytes > m.usage.PeakFinalBytes {
		m.usage.PeakFinalBytes = finalBytes
	}
	if stagingBytes > m.usage.PeakStagingBytes {
		m.usage.PeakStagingBytes = stagingBytes
	}
	if temporaryBytes > m.usage.PeakTemporaryBytes {
		m.usage.PeakTemporaryBytes = temporaryBytes
	}
	if ownedBytes > m.usage.PeakOwnedBytes {
		m.usage.PeakOwnedBytes = ownedBytes
	}
	m.usage.Samples++
	m.mu.Unlock()
}

func treeBytes(root string) int64 {
	var total int64
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return 0
	}
	return total
}

func matchingTreeBytes(root, prefix string) int64 {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	var total int64
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			total += treeBytes(filepath.Join(root, entry.Name()))
		}
	}
	return total
}

func finalizeWithUsage(recorder session.LiveRecorder, ctx context.Context) (error, finalizationUsage) {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	err := recorder.Finalize(ctx, nil)
	duration := time.Since(started)
	runtime.ReadMemStats(&after)
	allocated := uint64(0)
	if after.TotalAlloc >= before.TotalAlloc {
		allocated = after.TotalAlloc - before.TotalAlloc
	}
	return err, finalizationUsage{DurationMS: duration.Milliseconds(), AllocatedBytes: allocated, HeapAllocBefore: before.HeapAlloc, HeapAllocAfter: after.HeapAlloc}
}

func snapshotUsage(target any) *usageSnapshot {
	value := reflect.ValueOf(target)
	if !value.IsValid() || (value.Kind() == reflect.Pointer && value.IsNil()) {
		return nil
	}
	method := value.MethodByName("ResourceUsage")
	if !method.IsValid() {
		return nil
	}
	results := method.Call(nil)
	if len(results) != 1 {
		return nil
	}
	value = results[0]
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return nil
	}
	usage := &usageSnapshot{}
	fields := map[string]*int64{
		"QueueBytes": &usage.QueueBytes, "QueueItems": &usage.QueueItems,
		"PeakQueueBytes": &usage.PeakQueueBytes, "PeakQueueItems": &usage.PeakQueueItems,
		"AcceptedItems": &usage.AcceptedItems, "ProcessedItems": &usage.ProcessedItems,
		"AcceptedMessages": &usage.AcceptedMessages, "AcceptedAudio": &usage.AcceptedAudio, "AcceptedEvents": &usage.AcceptedEvents,
		"TranscriptBytes": &usage.TranscriptBytes, "TranscriptItems": &usage.TranscriptItems,
		"AudioBytes": &usage.AudioBytes, "AudioItems": &usage.AudioItems,
		"SidecarBytes": &usage.SidecarBytes, "SidecarItems": &usage.SidecarItems,
		"MetadataBytes": &usage.MetadataBytes, "MetadataItems": &usage.MetadataItems,
		"TerminalBytes": &usage.TerminalBytes, "TerminalItems": &usage.TerminalItems,
		"SummaryBytes": &usage.SummaryBytes, "SummaryItems": &usage.SummaryItems,
		"PeakSummaryBytes": &usage.PeakSummaryBytes, "PeakSummaryItems": &usage.PeakSummaryItems,
		"ProviderQueueBytes": &usage.ProviderQueueBytes, "ProviderQueueItems": &usage.ProviderQueueItems,
		"PeakProviderQueueBytes": &usage.PeakProviderQueueBytes, "PeakProviderQueueItems": &usage.PeakProviderQueueItems,
		"ProviderAcceptedItems": &usage.ProviderAcceptedItems, "ProviderBytes": &usage.ProviderBytes,
		"ProviderItems": &usage.ProviderItems, "PeakProviderBytes": &usage.PeakProviderBytes, "PeakProviderItems": &usage.PeakProviderItems,
	}
	for name, destination := range fields {
		field := value.FieldByName(name)
		if field.IsValid() && field.Kind() >= reflect.Int && field.Kind() <= reflect.Int64 {
			*destination = field.Int()
		}
	}
	return usage
}

func run(caseName, destination, sourceRevision string, requested limits) error {
	if _, ok := supportedCases[caseName]; !ok {
		return fmt.Errorf("public recording case %q is not implemented", caseName)
	}
	root := filepath.Dir(destination)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	providerPath := filepath.Join(root, "provider.json")
	disk := newDiskMonitor(destination, providerPath)
	defer disk.close()
	service := recordingwire.NewService(clock.Real{})
	providerService := recordingwire.NewProviderCaptureService(clock.Real{})
	providerOptions := recording.ProviderCaptureOptions{Destination: providerPath}
	applyLimitFields(&providerOptions, requested)
	sink, err := providerService.OpenProviderCapture(providerOptions)
	if err != nil {
		return fmt.Errorf("open provider capture: %w", err)
	}
	providerRecords := providerEvents(caseName)
	providerCapture := gatewaytesting.SessionCapture{Version: gatewaytesting.SessionCaptureVersion, Records: providerRecords}
	providerErr := settleProviderCapture(sink, providerRecords, caseName)
	if providerErr != nil {
		providerErr = errors.Join(providerErr, sink.FlushToFile(providerPath, providerCapture))
	} else {
		providerErr = sink.FlushToFile(providerPath, providerCapture)
	}
	disk.sample()

	options := recording.LiveEvidenceOptions{
		Destination:         providerDestination(destination),
		ProviderCapturePath: providerPath,
		SessionID:           "c20-public-consumer",
		ParticipantID:       "consumer",
		Provider:            "fixture",
		Model:               "fixture-model",
		ClockBase:           time.Now().UTC(), WallClockStart: time.Now().UTC(),
	}
	applyLimitFields(&options, requested)
	recorder, err := service.OpenLiveEvidence(options)
	if err != nil {
		return fmt.Errorf("open live evidence: %w", err)
	}
	ctx := context.Background()
	count := 1
	switch caseName {
	case "many-small", "baseline":
		count = 512
	case "large-record":
		count = 1
	case "semantic-boundaries", "encoded-expansion":
		count = 2
	case "overflow-healthy-terminal":
		count = 8
	case "summary-only-overflow":
		count = 1
	case "default-overflow", "cumulative-overflow":
		count = 128
	case "publication-resources":
		count = 4
	case "composition", "audio-tool":
		count = 2
	case "interruption":
		count = 1
	case "ask":
		count = 2
	}
	var inputErrors []string
	for index := 0; index < count; index++ {
		text := fmt.Sprintf("public recording observation %04d", index)
		if caseName == "large-record" {
			text = repeated("large-record-", 8192)
		}
		if caseName == "summary-only-overflow" {
			text = repeated("summary-only-overflow-", 1<<18)
		}
		if caseName == "default-overflow" || caseName == "cumulative-overflow" {
			text = strings.Repeat("default-overflow-", 1<<16)
		}
		if caseName == "encoded-expansion" {
			text = strings.Repeat("quotes=\"\\\" slash=\\ backslash=\\n control=\n ", 1024)
		}
		message := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(text)}
		direction := session.LiveRecordAgent
		if caseName == "ask" && index == 0 {
			direction = session.LiveRecordClient
			message.Role = messages.RoleUser
			message.Value = messages.NewTextDeltaValue("public ask input")
		}
		if caseName == "default-overflow" || caseName == "cumulative-overflow" {
			message = messages.StreamMessage{Type: messages.StreamTypeReasoningDelta, Value: messages.NewReasoningDeltaValue(text)}
		}
		if caseName == "interruption" {
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if err := recorder.RecordMessage(canceled, session.LiveRecord{Direction: direction, Timestamp: time.Now().UTC(), Message: message}); err != nil {
				inputErrors = append(inputErrors, err.Error())
			} else {
				return errors.New("interruption case did not observe cancellation")
			}
			continue
		}
		if err := recorder.RecordMessage(ctx, session.LiveRecord{Direction: direction, Timestamp: time.Now().UTC(), Message: message}); err != nil {
			return fmt.Errorf("record message %d: %w", index, err)
		}
		if caseName == "many-small" || caseName == "baseline" || caseName == "default-overflow" || caseName == "cumulative-overflow" {
			time.Sleep(time.Millisecond)
		}
		disk.sample()
	}
	if caseName == "composition" {
		if err := recorder.RecordEvent(ctx, session.LiveEvent{Kind: "composition.checkpoint", Timestamp: time.Now().UTC(), Text: "provider and semantic evidence composed", Critical: false}); err != nil {
			return fmt.Errorf("record composition checkpoint: %w", err)
		}
	}
	if caseName == "normal" || caseName == "overflow-healthy-terminal" || caseName == "summary-only-overflow" || caseName == "cleanup-failures" || caseName == "publication-resources" || caseName == "audio-tool" {
		frameCount := 1
		if caseName == "audio-tool" {
			frameCount = 2
		}
		for frameIndex := 0; frameIndex < frameCount; frameIndex++ {
			frame := sharedaudio.PCMFrame{Samples: []int16{int16(frameIndex + 1), -2, 3, -4}, Format: sharedaudio.PCM16DeviceFormat(24000), EndOfResponse: true}
			if err := recorder.RecordAudio(ctx, session.LiveAudioRecord{Direction: session.LiveRecordAgent, Admission: session.LiveAudioMessageObserved, Timestamp: time.Now().UTC(), Frame: frame}); err != nil {
				return fmt.Errorf("record audio: %w", err)
			}
		}
	}
	terminal := messages.NewSessionCloseValueWithTerminal("c20-public-consumer", "complete", "complete", messages.TerminalReasonProviderAuthoredCompletion, messages.TerminalProvenanceProvider, messages.TerminalOutputComplete)
	if err := recorder.RecordEvent(ctx, session.LiveEvent{Kind: string(session.LiveEventTerminal), Timestamp: time.Now().UTC(), Terminal: terminal, Critical: true}); err != nil {
		return fmt.Errorf("record terminal: %w", err)
	}
	disk.sample()
	finalErr, finalization := finalizeWithUsage(recorder, ctx)
	var repeatFinalizeErr error
	var repeatProviderErr error
	if caseName == "cleanup-failures" {
		repeatFinalizeErr = recorder.Finalize(ctx, nil)
		repeatProviderErr = sink.FlushToFile(providerPath, providerCapture)
	}
	diskUsage := disk.close()
	output := inspectResult(caseName, destination, providerPath, sourceRevision, requested, finalErr, providerErr)
	output.InputErrors = inputErrors
	output.SemanticUsage = snapshotUsage(recorder)
	output.ProviderUsage = snapshotUsage(sink)
	output.DiskUsage = diskUsage
	output.Finalization = finalization
	if repeatFinalizeErr != nil {
		output.RepeatFinalize = repeatFinalizeErr.Error()
	}
	if repeatProviderErr != nil {
		output.RepeatProvider = repeatProviderErr.Error()
	}
	return writeResult(output)
}

var supportedCases = map[string]struct{}{
	"baseline": {}, "normal": {}, "many-small": {}, "large-record": {},
	"semantic-boundaries": {}, "encoded-expansion": {}, "overflow-healthy-terminal": {},
	"summary-only-overflow": {}, "provider-boundaries": {}, "provider-settlement": {},
	"provider-overflow": {}, "provider-overflow-controls": {}, "cleanup-failures": {},
	"default-overflow": {}, "cumulative-overflow": {}, "publication-resources": {},
	"composition": {}, "audio-tool": {}, "interruption": {}, "ask": {},
}

func providerEvents(caseName string) []gatewaytesting.CapturedSessionEvent {
	events := []gatewaytesting.CapturedSessionEvent{{
		Sequence: 1, Direction: gatewaytesting.DirectionClientToServer,
		Type: "session.update", PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage,
		Payload: []byte(`{"type":"session.update","case":"` + caseName + `"}`),
	}}
	if caseName != "provider-overflow" && caseName != "provider-overflow-controls" {
		return append(events,
			gatewaytesting.CapturedSessionEvent{Sequence: 2, Direction: gatewaytesting.DirectionServerToClient, Type: "session.created", PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage, Payload: []byte(`{"type":"session.created"}`)},
			gatewaytesting.CapturedSessionEvent{Sequence: 3, Direction: gatewaytesting.DirectionServerToClient, Type: "response.done", PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage, Payload: []byte(`{"type":"response.done"}`)},
		)
	}
	for sequence := 2; sequence <= 8; sequence++ {
		events = append(events, gatewaytesting.CapturedSessionEvent{
			Sequence: sequence, Direction: gatewaytesting.DirectionServerToClient,
			Type: "response.output_text.delta", PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage,
			Payload: []byte(`{"type":"response.output_text.delta","text":"` + repeated("provider-evidence-", 64) + `"}`),
		})
	}
	return events
}

func settleProviderCapture(sink recording.ProviderCaptureSink, events []gatewaytesting.CapturedSessionEvent, caseName string) error {
	accepted := make([]gatewaytesting.CapturedSessionEvent, 0, len(events))
	var appendErr error
	for _, event := range events {
		if err := sink.Append(event); err != nil {
			appendErr = err
			break
		}
		accepted = append(accepted, event)
	}
	if appendErr != nil {
		// A data budget failure must not strand already admitted mutations. Use
		// both settlement controls after the overflow so the bounded control
		// reserve is exercised before FlushToFile reports the failed prefix.
		for _, event := range accepted {
			var err error
			if event.Sequence%2 == 0 {
				err = sink.Discard(event.Sequence)
			} else {
				err = sink.Commit(event.Sequence)
			}
			if err != nil {
				return errors.Join(appendErr, err)
			}
		}
		return appendErr
	}
	switch caseName {
	case "provider-boundaries":
		if err := sink.Commit(events[0].Sequence); err != nil {
			return err
		}
		if err := sink.Discard(events[1].Sequence); err != nil {
			return err
		}
		return sink.Commit(events[2].Sequence)
	case "provider-settlement":
		if err := sink.Commit(events[2].Sequence); err != nil {
			return err
		}
		if err := sink.Discard(events[1].Sequence); err != nil {
			return err
		}
		return sink.Commit(events[0].Sequence)
	default:
		for _, event := range events {
			if err := sink.Commit(event.Sequence); err != nil {
				return fmt.Errorf("commit provider event %d: %w", event.Sequence, err)
			}
		}
		return nil
	}
}

func providerDestination(destination string) string {
	return destination
}

func applyLimitFields(target any, requested limits) {
	value := reflect.ValueOf(target)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return
	}
	limitsField := value.Elem().FieldByName("Limits")
	if !limitsField.IsValid() || limitsField.Kind() != reflect.Struct {
		return
	}
	set := func(name string, value int64) {
		field := limitsField.FieldByName(name)
		if field.IsValid() && field.CanSet() && value > 0 {
			field.SetInt(value)
		}
	}
	set("TranscriptBytes", requested.transcriptBytes)
	set("TranscriptItems", requested.transcriptItems)
	set("AudioBytes", requested.audioBytes)
	set("AudioItems", requested.audioItems)
	set("ProviderBytes", requested.providerBytes)
	set("ProviderItems", requested.providerItems)
}

func inspectResult(caseName, destination, providerPath, sourceRevision string, requested limits, finalErr, providerErr error) result {
	output := result{Case: caseName, SourceRevision: sourceRevision, LimitsApplied: requestedMap(requested), TranscriptLines: map[string]int{}, FileBytes: map[string]int64{}, TranscriptSHA256: map[string]string{}}
	if finalErr != nil {
		output.FinalizationErr = finalErr.Error()
	}
	if providerErr != nil {
		output.ProviderErr = providerErr.Error()
	}
	var manifest map[string]any
	if data, err := os.ReadFile(filepath.Join(destination, "manifest.json")); err == nil {
		_ = json.Unmarshal(data, &manifest)
		if status, ok := manifest["recording_status"].(map[string]any); ok {
			output.Status, _ = status["state"].(string)
		} else {
			output.Status = "complete"
		}
	}
	var paths []string
	_ = filepath.Walk(destination, func(path string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			rel, relErr := filepath.Rel(destination, path)
			if relErr == nil {
				paths = append(paths, rel)
				output.FileBytes[rel] = info.Size()
				output.TotalBytes += info.Size()
			}
		}
		return nil
	})
	sort.Strings(paths)
	for _, rel := range paths {
		if filepath.Base(rel) == "client.transcript.jsonl" || filepath.Base(rel) == "agent.transcript.jsonl" {
			path := filepath.Join(destination, rel)
			if data, err := os.ReadFile(path); err == nil {
				lines := 0
				for _, line := range splitLines(data) {
					if len(line) > 0 {
						lines++
					}
				}
				output.TranscriptLines[rel] = lines
			}
			if digest, err := hashFile(path); err == nil {
				output.TranscriptSHA256[rel] = digest
			}
		}
	}
	if info, err := os.Stat(providerPath); err == nil {
		output.ProviderBytes = info.Size()
	}
	if info, err := os.Stat(strings.TrimSuffix(providerPath, filepath.Ext(providerPath)) + ".jsonl"); err == nil {
		output.SidecarBytes = info.Size()
	}
	if capture, err := gatewaytesting.LoadSessionCapture(providerPath); err == nil {
		output.ProviderCaptureValid = true
		output.ProviderSequences = make([]int, 0, len(capture.Records))
		for _, event := range capture.Records {
			output.ProviderSequences = append(output.ProviderSequences, event.Sequence)
		}
	}
	return output
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	for len(data) > 0 {
		index := 0
		for index < len(data) && data[index] != '\n' {
			index++
		}
		lines = append(lines, data[:index])
		if index == len(data) {
			break
		}
		data = data[index+1:]
	}
	return lines
}

func requestedMap(value limits) map[string]int64 {
	return map[string]int64{"transcript_bytes": value.transcriptBytes, "transcript_items": value.transcriptItems, "audio_bytes": value.audioBytes, "audio_items": value.audioItems, "provider_bytes": value.providerBytes, "provider_items": value.providerItems}
}

func repeated(prefix string, count int) string {
	return strings.Repeat(prefix, count)
}

func writeResult(value result) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(append(encoded, '\n'))
	return err
}

func fail(err error) {
	_, _ = fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
