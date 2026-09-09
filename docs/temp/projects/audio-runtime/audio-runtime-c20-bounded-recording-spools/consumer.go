package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
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
	Case            string           `json:"case"`
	FinalizationErr string           `json:"finalization_error,omitempty"`
	ProviderErr     string           `json:"provider_error,omitempty"`
	Status          string           `json:"status,omitempty"`
	TranscriptLines map[string]int   `json:"transcript_lines,omitempty"`
	FileBytes       map[string]int64 `json:"file_bytes,omitempty"`
	TotalBytes      int64            `json:"total_bytes"`
	ProviderBytes   int64            `json:"provider_bytes"`
	SidecarBytes    int64            `json:"sidecar_bytes"`
	SourceRevision  string           `json:"source_revision,omitempty"`
	LimitsApplied   map[string]int64 `json:"limits_applied,omitempty"`
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

func run(caseName, destination, sourceRevision string, requested limits) error {
	root := filepath.Dir(destination)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	providerPath := filepath.Join(root, "provider.json")
	service := recordingwire.NewService(clock.Real{})
	providerService := recordingwire.NewProviderCaptureService(clock.Real{})
	providerOptions := recording.ProviderCaptureOptions{Destination: providerPath}
	applyLimitFields(&providerOptions, requested)
	sink, err := providerService.OpenProviderCapture(providerOptions)
	if err != nil {
		return fmt.Errorf("open provider capture: %w", err)
	}
	providerRecords := []gatewaytesting.CapturedSessionEvent{{
		Sequence: 1, Direction: gatewaytesting.DirectionClientToServer,
		Type: "session.update", PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage,
		Payload: []byte(`{"type":"session.update","case":"` + caseName + `"}`),
	}}
	providerCapture := gatewaytesting.SessionCapture{Version: gatewaytesting.SessionCaptureVersion, Records: providerRecords}
	if caseName == "provider-overflow" {
		for sequence := 2; sequence <= 8; sequence++ {
			providerRecords = append(providerRecords, gatewaytesting.CapturedSessionEvent{
				Sequence: sequence, Direction: gatewaytesting.DirectionServerToClient,
				Type: "response.output_text.delta", PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage,
				Payload: []byte(`{"type":"response.output_text.delta","text":"` + repeated("provider-evidence-", 64) + `"}`),
			})
		}
		providerCapture.Records = providerRecords
	}
	for _, event := range providerRecords {
		if err := sink.Append(event); err != nil {
			providerErr := err
			_ = sink.Abort()
			return writeResult(result{Case: caseName, ProviderErr: providerErr.Error(), SourceRevision: sourceRevision, LimitsApplied: requestedMap(requested)})
		}
		if err := sink.Commit(event.Sequence); err != nil {
			_ = sink.Abort()
			return fmt.Errorf("commit provider event %d: %w", event.Sequence, err)
		}
	}
	providerErr := sink.FlushToFile(providerPath, providerCapture)

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
	case "provider-overflow":
		count = 2
	case "default-overflow":
		count = 48
	}
	for index := 0; index < count; index++ {
		text := fmt.Sprintf("public recording observation %04d", index)
		if caseName == "large-record" {
			text = repeated("large-record-", 8192)
		}
		if caseName == "default-overflow" {
			text = strings.Repeat("default-overflow-", 1<<16)
		}
		if err := recorder.RecordMessage(ctx, session.LiveRecord{Direction: session.LiveRecordAgent, Timestamp: time.Now().UTC(), Message: messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(text)}}); err != nil {
			return fmt.Errorf("record message %d: %w", index, err)
		}
		if caseName == "many-small" || caseName == "baseline" || caseName == "default-overflow" {
			time.Sleep(time.Millisecond)
		}
	}
	if caseName == "normal" {
		frame := sharedaudio.PCMFrame{Samples: []int16{1, -2, 3, -4}, Format: sharedaudio.PCM16DeviceFormat(24000), EndOfResponse: true}
		if err := recorder.RecordAudio(ctx, session.LiveAudioRecord{Direction: session.LiveRecordAgent, Admission: session.LiveAudioMessageObserved, Timestamp: time.Now().UTC(), Frame: frame}); err != nil {
			return fmt.Errorf("record audio: %w", err)
		}
	}
	terminal := messages.NewSessionCloseValueWithTerminal("c20-public-consumer", "complete", "complete", messages.TerminalReasonProviderAuthoredCompletion, messages.TerminalProvenanceProvider, messages.TerminalOutputComplete)
	if err := recorder.RecordEvent(ctx, session.LiveEvent{Kind: string(session.LiveEventTerminal), Timestamp: time.Now().UTC(), Terminal: terminal, Critical: true}); err != nil {
		return fmt.Errorf("record terminal: %w", err)
	}
	finalErr := recorder.Finalize(ctx, nil)
	output := inspectResult(caseName, destination, providerPath, sourceRevision, requested, finalErr, providerErr)
	return writeResult(output)
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
	output := result{Case: caseName, SourceRevision: sourceRevision, LimitsApplied: requestedMap(requested), TranscriptLines: map[string]int{}, FileBytes: map[string]int64{}}
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
			if data, err := os.ReadFile(filepath.Join(destination, rel)); err == nil {
				lines := 0
				for _, line := range splitLines(data) {
					if len(line) > 0 {
						lines++
					}
				}
				output.TranscriptLines[rel] = lines
			}
		}
	}
	if info, err := os.Stat(providerPath); err == nil {
		output.ProviderBytes = info.Size()
	}
	if info, err := os.Stat(strings.TrimSuffix(providerPath, filepath.Ext(providerPath)) + ".jsonl"); err == nil {
		output.SidecarBytes = info.Size()
	}
	return output
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
