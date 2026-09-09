package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	recordingwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	watchdog             = 50 * time.Second
	knownSummaryMaxBytes = 2 << 20
	knownSummaryMaxItems = 4096
)

type report struct {
	Case                  string               `json:"case"`
	Source                string               `json:"source"`
	SummaryBudget         budgetReport         `json:"summary_budget"`
	Queue                 queueReport          `json:"queue"`
	Finalization          finalizationReport   `json:"finalization"`
	NormalFinalization    *finalizationReport  `json:"normal_finalization,omitempty"`
	OverflowFinalization  *finalizationReport  `json:"overflow_finalization,omitempty"`
	Normal                *normalReport        `json:"normal,omitempty"`
	Overflow              *overflowReport      `json:"overflow,omitempty"`
	Measurements          []measurement        `json:"measurements,omitempty"`
	NoRecordingControl    []controlMeasurement `json:"no_recording_control,omitempty"`
	RawEvidenceAfterLimit bool                 `json:"raw_evidence_after_limit,omitempty"`
	CleanShutdown         bool                 `json:"clean_shutdown"`
	LockReleased          bool                 `json:"lock_released"`
	SessionLogBytes       int                  `json:"session_log_bytes"`
	SessionLogTurns       int                  `json:"session_log_turns"`
	Error                 string               `json:"error,omitempty"`
}

type budgetReport struct {
	MaxBytes int    `json:"max_bytes"`
	MaxItems int    `json:"max_items"`
	Scope    string `json:"scope"`
}

type queueReport struct {
	CapacityBytes int    `json:"capacity_bytes"`
	DrainWaitMS   int    `json:"drain_wait_ms"`
	Events        int    `json:"events"`
	PayloadBytes  uint64 `json:"payload_bytes"`
}

type finalizationReport struct {
	FirstError  string `json:"first_error,omitempty"`
	SecondError string `json:"second_error,omitempty"`
	Stable      bool   `json:"stable"`
	HeapLive    uint64 `json:"heap_live_bytes"`
	HeapAfter   uint64 `json:"heap_after_finalize_bytes"`
	TotalAlloc  uint64 `json:"shutdown_total_alloc_bytes"`
	ElapsedMS   int64  `json:"shutdown_elapsed_ms"`
}

type normalReport struct {
	ExpectedTurns       int  `json:"expected_turns"`
	CompleteSummary     bool `json:"complete_summary"`
	TranscriptSnapshots bool `json:"transcript_snapshots"`
	ToolResultExactOnce bool `json:"tool_result_exact_once"`
	LateAudioAttributed bool `json:"late_audio_attributed"`
	PCMBytes            int  `json:"pcm_bytes"`
}

type overflowReport struct {
	PartialStatus     bool `json:"partial_status"`
	AcceptedTurns     int  `json:"accepted_turns"`
	RawTailPresent    bool `json:"raw_tail_present"`
	RawPCMBytes       int  `json:"raw_pcm_bytes"`
	TerminalPreserved bool `json:"terminal_preserved"`
	SilentSuccess     bool `json:"silent_success"`
}

type measurement struct {
	Turns              int    `json:"turns"`
	Events             int    `json:"events"`
	PayloadBytes       uint64 `json:"payload_bytes"`
	HeapLiveBytes      uint64 `json:"heap_live_bytes"`
	HeapObjects        uint64 `json:"heap_objects"`
	ShutdownHeapBytes  uint64 `json:"shutdown_heap_bytes"`
	ShutdownAlloc      uint64 `json:"shutdown_alloc_bytes"`
	ShutdownElapsedMS  int64  `json:"shutdown_elapsed_ms"`
	DrainWaitMS        int    `json:"drain_wait_ms"`
	FinalizationStable bool   `json:"finalization_stable"`
	LockReleased       bool   `json:"lock_released"`
}

type controlMeasurement struct {
	Turns         int    `json:"turns"`
	Events        int    `json:"events"`
	PayloadBytes  uint64 `json:"payload_bytes"`
	HeapLiveBytes uint64 `json:"heap_live_bytes"`
	HeapObjects   uint64 `json:"heap_objects"`
}

type logEntry struct {
	TurnIndex int `json:"turn_index"`
	Input     struct {
		Text             string   `json:"text"`
		AudioOffsetBytes uint64   `json:"audio_offset_bytes"`
		AudioBytes       uint64   `json:"audio_bytes"`
		Committed        bool     `json:"committed"`
		AudioSegments    []string `json:"audio_segments,omitempty"`
	} `json:"input"`
	Response struct {
		Text             string   `json:"text"`
		Complete         bool     `json:"complete"`
		AudioOffsetBytes uint64   `json:"audio_offset_bytes"`
		AudioBytes       uint64   `json:"audio_bytes"`
		AudioSegments    []string `json:"audio_segments,omitempty"`
	} `json:"response"`
	ToolEvents []struct {
		Type       string `json:"type"`
		ToolCallID string `json:"tool_call_id,omitempty"`
		ToolName   string `json:"tool_name,omitempty"`
		Arguments  string `json:"arguments,omitempty"`
		Status     string `json:"status,omitempty"`
		Content    string `json:"content,omitempty"`
	} `json:"tool_events,omitempty"`
}

type runState struct {
	recorder          session.LiveRecorder
	destination       string
	providerPath      string
	events            int
	payloadBytes      uint64
	drainWaitMS       int
	base              *clock.Deterministic
	finalized         bool
	finalizeError     error
	finalizeElapsedMS int64
}

func main() {
	caseName := flag.String("case", "normal", "normal, overflow, characterize, or matrix")
	output := flag.String("output", "", "JSON report path")
	flag.Parse()

	result, err := runCase(*caseName)
	if err != nil {
		if result == nil {
			result = &report{Case: *caseName}
		}
		result.Error = err.Error()
	}
	if *output != "" && result != nil {
		if writeErr := writeReport(*output, result); writeErr != nil {
			fmt.Fprintln(os.Stderr, writeErr)
			os.Exit(1)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runCase(name string) (*report, error) {
	source := os.Getenv("C19_SOURCE_REVISION")
	if source == "" {
		source = "working-tree"
	}
	result := &report{
		Case:          name,
		Source:        source + "; public recording Wire + LiveRecorder; raw transcript/PCM/provider files are authoritative",
		SummaryBudget: budgetReport{MaxBytes: knownSummaryMaxBytes, MaxItems: knownSummaryMaxItems, Scope: "retained convenience projection and final JSONL; queue/raw disk are separate"},
		Queue:         queueReport{CapacityBytes: 16 << 20},
	}
	switch name {
	case "normal":
		return result, runNormal(result)
	case "overflow":
		return result, runOverflow(result)
	case "characterize":
		return result, runCharacterize(result)
	case "matrix":
		return result, runMatrix(result)
	default:
		return result, fmt.Errorf("unsupported case %q", name)
	}
}

func runMatrix(result *report) error {
	normal := *result
	normal.Case = "normal"
	if err := runNormal(&normal); err != nil {
		return err
	}
	overflow := *result
	overflow.Case = "overflow"
	if err := runOverflow(&overflow); err != nil {
		return err
	}
	result.Normal = normal.Normal
	result.Overflow = overflow.Overflow
	result.NormalFinalization = &normal.Finalization
	result.OverflowFinalization = &overflow.Finalization
	result.Finalization = overflow.Finalization
	result.Queue = overflow.Queue
	result.RawEvidenceAfterLimit = overflow.RawEvidenceAfterLimit
	result.CleanShutdown = normal.CleanShutdown && overflow.CleanShutdown
	result.LockReleased = normal.LockReleased && overflow.LockReleased
	result.SessionLogBytes = overflow.SessionLogBytes
	result.SessionLogTurns = overflow.SessionLogTurns
	return nil
}

func newRun() (*runState, func(), error) {
	root, err := os.MkdirTemp("", "audio-runtime-c19-consumer-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	destination := filepath.Join(root, "recording")
	providerPath := filepath.Join(root, "provider.json")
	if err := os.WriteFile(providerPath, []byte(`{"fixture":"c19-provider-capture"}`), 0o600); err != nil {
		cleanup()
		return nil, nil, err
	}
	base := clock.NewDeterministic(time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC), time.Millisecond)
	service := recordingwire.NewService(base)
	recorder, err := service.OpenLiveEvidence(recording.LiveEvidenceOptions{
		Destination: destination, SessionID: "c19-consumer", ParticipantID: "fixture",
		Provider: "fixture", Model: "bounded-summary", ProviderCapturePath: providerPath,
	})
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return &runState{recorder: recorder, destination: destination, providerPath: providerPath, base: base}, cleanup, nil
}

func (r *runState) send(direction session.LiveRecordDirection, message messages.StreamMessage) error {
	if r == nil || r.recorder == nil {
		return errors.New("consumer recorder is unavailable")
	}
	if err := r.recorder.RecordMessage(context.Background(), session.LiveRecord{Direction: direction, Timestamp: r.base.Now(), Message: message}); err != nil {
		return err
	}
	r.events++
	payload, err := gatewaytesting.MarshalStreamMessage(message)
	if err != nil {
		return err
	}
	r.payloadBytes += uint64(len(payload) * 2)
	return nil
}

func (r *runState) sendAudio(direction session.LiveRecordDirection, frame sharedaudio.PCMFrame) error {
	if err := r.recorder.RecordAudio(context.Background(), session.LiveAudioRecord{Direction: direction, Admission: session.LiveAudioMessageObserved, Timestamp: r.base.Now(), Frame: frame}); err != nil {
		return err
	}
	r.events++
	r.payloadBytes += uint64(len(frame.Samples) * 2)
	return nil
}

func (r *runState) terminal() error {
	value := messages.NewSessionCloseValueWithTerminal("c19-consumer", "completed", "completed", messages.TerminalReasonProviderAuthoredCompletion, messages.TerminalProvenanceProvider, messages.TerminalOutputComplete)
	return r.recorder.RecordEvent(context.Background(), session.LiveEvent{Kind: string(session.LiveEventTerminal), Timestamp: r.base.Now(), Terminal: value, Critical: true})
}

func (r *runState) drain() {
	const wait = 100 * time.Millisecond
	time.Sleep(wait)
	runtime.Gosched()
	r.drainWaitMS += int(wait / time.Millisecond)
}

func (r *runState) drainN(count int) {
	for index := 0; index < count; index++ {
		r.drain()
	}
}

func (r *runState) finalize() (error, error) {
	started := time.Now()
	first := r.recorder.Finalize(context.Background(), nil)
	second := r.recorder.Finalize(context.Background(), nil)
	r.finalizeElapsedMS = time.Since(started).Milliseconds()
	r.finalized = true
	r.finalizeError = first
	if errorText(first) != errorText(second) {
		return first, fmt.Errorf("finalization was not idempotent: first=%q second=%q", errorText(first), errorText(second))
	}
	return first, nil
}

func runNormal(result *report) error {
	run, cleanup, err := newRun()
	if err != nil {
		return err
	}
	defer cleanup()
	const turns = 24
	for index := 0; index < turns; index++ {
		responseID := fmt.Sprintf("response-%03d", index)
		if err := run.send(session.LiveRecordClient, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleUser, Value: messages.NewTextDeltaValue(fmt.Sprintf("question-%03d", index))}); err != nil {
			return err
		}
		if err := run.sendAudio(session.LiveRecordClient, sharedaudio.PCMFrame{Samples: []int16{int16(index + 1)}, Format: sharedaudio.PCM16DeviceFormat(24000)}); err != nil {
			return err
		}
		if err := run.send(session.LiveRecordClient, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleUser}); err != nil {
			return err
		}
		if index == 0 {
			if err := run.send(session.LiveRecordAgent, messages.StreamMessage{Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleUser, Value: messages.NewTranscriptDeltaValueForItem("partial transcript", "input-item-0")}); err != nil {
				return err
			}
			if err := run.send(session.LiveRecordAgent, messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd, Role: messages.RoleUser, Value: messages.NewTranscriptEndValueForItem("corrected transcript", "input-item-0")}); err != nil {
				return err
			}
		}
		if index == 1 {
			if err := run.send(session.LiveRecordAgent, messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewToolCallEndValue("call-1", "lookup", `{"query":"fixture"}`)}); err != nil {
				return err
			}
			if err := run.send(session.LiveRecordAgent, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: responseID}); err != nil {
				return err
			}
			if err := run.send(session.LiveRecordAgent, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleTool, ToolCallId: "call-1", Value: messages.NewTextDeltaValue("tool-result")}); err != nil {
				return err
			}
		}
		if err := run.sendAudio(session.LiveRecordAgent, sharedaudio.PCMFrame{Samples: []int16{int16(100 + index)}, Format: sharedaudio.PCM16DeviceFormat(24000), PlaybackResponse: sharedaudio.PlaybackResponse{ResponseID: responseID}}); err != nil {
			return err
		}
		if err := run.send(session.LiveRecordAgent, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewTextDeltaValue(fmt.Sprintf("answer-%03d", index))}); err != nil {
			return err
		}
		if err := run.send(session.LiveRecordAgent, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: responseID}); err != nil {
			return err
		}
		if index%8 == 7 {
			run.drain()
		}
	}
	if err := run.terminal(); err != nil {
		return err
	}
	run.drain()
	heapLive := heapBytes()
	first, stableErr := run.finalize()
	if stableErr != nil {
		return stableErr
	}
	result.Finalization.FirstError = errorText(first)
	result.Finalization.SecondError = errorText(run.finalizeError)
	result.Finalization.Stable = stableErr == nil && errorText(first) == errorText(run.finalizeError)
	result.Queue.Events = run.events
	result.Queue.PayloadBytes = run.payloadBytes
	result.Queue.DrainWaitMS = run.drainWaitMS
	result.Finalization.HeapLive = heapLive
	result.Finalization.HeapAfter = heapBytes()
	result.Finalization.TotalAlloc = totalAlloc()
	result.Finalization.ElapsedMS = run.finalizeElapsedMS
	result.CleanShutdown = true
	result.LockReleased = !pathExists(run.destination + ".lock")
	entries, manifest, err := readBundle(run.destination)
	if err != nil {
		return err
	}
	result.SessionLogBytes = len(entries.raw)
	result.SessionLogTurns = len(entries.entries)
	if manifest.RecordingStatus != nil {
		return fmt.Errorf("normal recording unexpectedly partial: %s", manifest.RecordingStatus.Reason)
	}
	for index, entry := range entries.entries {
		if index == 0 && (entry.Input.Text != "question-000corrected transcript" || entry.Response.Text != "answer-000" || entry.Response.AudioBytes != 2) {
			return fmt.Errorf("normal summary mismatch at turn 1: %+v", entry)
		}
	}
	result.Normal = &normalReport{ExpectedTurns: turns, CompleteSummary: len(entries.entries) == turns, TranscriptSnapshots: strings.Contains(entries.rawString, "corrected transcript"), ToolResultExactOnce: strings.Count(entries.rawString, "tool-result") == 1, LateAudioAttributed: len(entries.entries) > 0 && entries.entries[0].Response.AudioBytes == 2, PCMBytes: fileSize(filepath.Join(run.destination, "audio", "out-000.pcm"))}
	if !result.Normal.CompleteSummary || !result.Normal.TranscriptSnapshots || !result.Normal.ToolResultExactOnce || !result.Normal.LateAudioAttributed {
		return errors.New("normal summary parity assertion failed")
	}
	return nil
}

func runOverflow(result *report) error {
	run, cleanup, err := newRun()
	if err != nil {
		return err
	}
	defer cleanup()
	if err := run.send(session.LiveRecordAgent, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: "response-before", Value: messages.NewTextDeltaValue("accepted-before-overflow")}); err != nil {
		return err
	}
	if err := run.send(session.LiveRecordAgent, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "response-before"}); err != nil {
		return err
	}
	if err := sendOverflowText(run); err != nil {
		return err
	}
	if err := run.send(session.LiveRecordAgent, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: "response-after", Value: messages.NewTextDeltaValue("raw-tail-after-overflow")}); err != nil {
		return err
	}
	if err := run.sendAudio(session.LiveRecordAgent, sharedaudio.PCMFrame{Samples: []int16{21, -22, 23}, Format: sharedaudio.PCM16DeviceFormat(24000)}); err != nil {
		return err
	}
	if err := run.terminal(); err != nil {
		return err
	}
	run.drainN(20)
	heapLive := heapBytes()
	first, stableErr := run.finalize()
	if stableErr != nil {
		return stableErr
	}
	result.Finalization.FirstError = errorText(first)
	result.Finalization.SecondError = errorText(run.finalizeError)
	result.Finalization.Stable = errorText(first) == errorText(run.finalizeError)
	result.Finalization.HeapLive = heapLive
	result.Finalization.HeapAfter = heapBytes()
	result.Finalization.TotalAlloc = totalAlloc()
	result.Finalization.ElapsedMS = run.finalizeElapsedMS
	result.Queue.Events = run.events
	result.Queue.PayloadBytes = run.payloadBytes
	result.Queue.DrainWaitMS = run.drainWaitMS
	result.CleanShutdown = true
	result.LockReleased = !pathExists(run.destination + ".lock")
	entries, manifest, err := readBundle(run.destination)
	if err != nil {
		return err
	}
	tailPresent := rawTranscriptContains(filepath.Join(run.destination, "agent.transcript.jsonl"), "raw-tail-after-overflow")
	partial := manifest.RecordingStatus != nil && manifest.RecordingStatus.State == transcript.RecordingStatusPartial
	terminal := manifest.Terminal != nil && manifest.Terminal.Reason == "completed"
	result.RawEvidenceAfterLimit = tailPresent
	result.SessionLogBytes = len(entries.raw)
	result.SessionLogTurns = len(entries.entries)
	result.Overflow = &overflowReport{PartialStatus: partial, AcceptedTurns: len(entries.entries), RawTailPresent: tailPresent, RawPCMBytes: fileSize(filepath.Join(run.destination, "audio", "out-000.pcm")), TerminalPreserved: terminal, SilentSuccess: first == nil}
	if first == nil || !partial || !tailPresent || !terminal || result.Overflow.RawPCMBytes != 6 {
		return fmt.Errorf("overflow evidence incomplete: partial=%t tail=%t terminal=%t pcm=%d", partial, tailPresent, terminal, result.Overflow.RawPCMBytes)
	}
	return nil
}

func sendOverflowText(run *runState) error {
	large := strings.Repeat("bounded-summary-overflow-", 250_000)
	message := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: "response-overflow", Value: messages.NewTextDeltaValue(large)}
	return run.send(session.LiveRecordAgent, message)
}

func runCharacterize(result *report) error {
	result.LockReleased = true
	result.Finalization.Stable = true
	for _, turns := range []int{16, 64, 128, 256, 512} {
		control, err := measureNoRecording(turns)
		if err != nil {
			return err
		}
		result.NoRecordingControl = append(result.NoRecordingControl, control)
		measurement, err := measure(turns)
		if err != nil {
			return err
		}
		result.Measurements = append(result.Measurements, measurement)
		result.Queue.Events += measurement.Events
		result.Queue.PayloadBytes += measurement.PayloadBytes
		result.Queue.DrainWaitMS += measurement.DrainWaitMS
		result.Finalization.Stable = result.Finalization.Stable && measurement.FinalizationStable
		result.LockReleased = result.LockReleased && measurement.LockReleased
		if measurement.HeapLiveBytes > result.Finalization.HeapLive {
			result.Finalization.HeapLive = measurement.HeapLiveBytes
		}
		if measurement.ShutdownHeapBytes > result.Finalization.HeapAfter {
			result.Finalization.HeapAfter = measurement.ShutdownHeapBytes
		}
		if measurement.ShutdownAlloc > result.Finalization.TotalAlloc {
			result.Finalization.TotalAlloc = measurement.ShutdownAlloc
		}
		if measurement.ShutdownElapsedMS > result.Finalization.ElapsedMS {
			result.Finalization.ElapsedMS = measurement.ShutdownElapsedMS
		}
	}
	result.CleanShutdown = true
	return nil
}

func measure(turns int) (measurement, error) {
	run, cleanup, err := newRun()
	if err != nil {
		return measurement{}, err
	}
	defer cleanup()
	for index := 0; index < turns; index++ {
		responseID := fmt.Sprintf("measure-response-%d", index)
		if err := run.send(session.LiveRecordClient, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleUser, Value: messages.NewTextDeltaValue(fmt.Sprintf("measure-question-%d", index))}); err != nil {
			return measurement{}, err
		}
		if err := run.send(session.LiveRecordClient, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleUser}); err != nil {
			return measurement{}, err
		}
		if err := run.send(session.LiveRecordAgent, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewTextDeltaValue(fmt.Sprintf("measure-answer-%d", index))}); err != nil {
			return measurement{}, err
		}
		if err := run.send(session.LiveRecordAgent, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: responseID}); err != nil {
			return measurement{}, err
		}
		if index%32 == 31 {
			run.drain()
		}
	}
	if err := run.terminal(); err != nil {
		return measurement{}, err
	}
	run.drain()
	live := heapSample()
	first, stableErr := run.finalize()
	if stableErr != nil || first != nil {
		return measurement{}, fmt.Errorf("characterization finalization failed: %v", errors.Join(first, stableErr))
	}
	return measurement{Turns: turns, Events: run.events, PayloadBytes: run.payloadBytes, HeapLiveBytes: live.heap, HeapObjects: live.objects, ShutdownHeapBytes: heapBytes(), ShutdownAlloc: totalAlloc(), ShutdownElapsedMS: run.finalizeElapsedMS, DrainWaitMS: run.drainWaitMS, FinalizationStable: true, LockReleased: !pathExists(run.destination + ".lock")}, nil
}

func measureNoRecording(turns int) (controlMeasurement, error) {
	var events int
	var payloadBytes uint64
	for index := 0; index < turns; index++ {
		responseID := fmt.Sprintf("measure-response-%d", index)
		messagesForTurn := []messages.StreamMessage{
			{Type: messages.StreamTypeTextDelta, Role: messages.RoleUser, Value: messages.NewTextDeltaValue(fmt.Sprintf("measure-question-%d", index))},
			{Type: messages.StreamTypeMessageEnd, Role: messages.RoleUser},
			{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewTextDeltaValue(fmt.Sprintf("measure-answer-%d", index))},
			{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: responseID},
		}
		for _, message := range messagesForTurn {
			payload, err := gatewaytesting.MarshalStreamMessage(message)
			if err != nil {
				return controlMeasurement{}, err
			}
			events++
			payloadBytes += uint64(len(payload) * 2)
		}
	}
	live := heapSample()
	return controlMeasurement{Turns: turns, Events: events, PayloadBytes: payloadBytes, HeapLiveBytes: live.heap, HeapObjects: live.objects}, nil
}

type heapSampleValue struct {
	heap    uint64
	objects uint64
}

func heapSample() heapSampleValue {
	runtime.GC()
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return heapSampleValue{heap: stats.HeapAlloc, objects: stats.HeapObjects}
}

func heapBytes() uint64 { return heapSample().heap }

func totalAlloc() uint64 {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.TotalAlloc
}

type bundleData struct {
	rawString string
	raw       []byte
	entries   []logEntry
}

func readBundle(destination string) (bundleData, transcript.RecordingManifest, error) {
	data, err := os.ReadFile(filepath.Join(destination, "session-log.jsonl"))
	if err != nil {
		return bundleData{}, transcript.RecordingManifest{}, err
	}
	var entries []logEntry
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var entry logEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return bundleData{}, transcript.RecordingManifest{}, err
		}
		entries = append(entries, entry)
	}
	manifestBytes, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil {
		return bundleData{}, transcript.RecordingManifest{}, err
	}
	var manifest transcript.RecordingManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return bundleData{}, transcript.RecordingManifest{}, err
	}
	return bundleData{rawString: string(data), raw: data, entries: entries}, manifest, nil
}

func rawTranscriptContains(path, want string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range bytesSplit(data, '\n') {
		if len(line) == 0 {
			continue
		}
		record, err := transcript.Decode(line)
		if err == nil && strings.Contains(string(record.Payload), want) {
			return true
		}
	}
	return false
}

func bytesSplit(data []byte, separator byte) [][]byte {
	var result [][]byte
	start := 0
	for index, value := range data {
		if value != separator {
			continue
		}
		result = append(result, data[start:index])
		start = index + 1
	}
	return append(result, data[start:])
}

func writeReport(path string, value *report) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func fileSize(path string) int {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return int(info.Size())
}
