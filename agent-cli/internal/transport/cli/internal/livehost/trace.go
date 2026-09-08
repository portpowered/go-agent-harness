package livehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	runtimeRecording "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/recording"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	websocketTextMessage               = 1
	websocketBinaryMessage             = 2
	liveTraceDirectoryMode os.FileMode = 0o755
)

// liveTraceRecorder keeps the livehost path on the same staged trace contract
// as the legacy session dispatcher. The semantic recorder remains the owner
// of the record-dir bundle; this wrapper owns only the independently staged
// audio trace and projects the already-published raw provider capture into its
// runtime timeline before it is attached.
type liveTraceRecorder struct {
	inner        runtimeSession.LiveRecorder
	trace        *recording.Trace
	tracePath    string
	destination  string
	providerPath string
	providerRate int

	finalizeOnce sync.Once
	finalizeErr  error
}

func newLiveTraceRecorder(inner runtimeSession.LiveRecorder, destination, providerPath string, providerRate int, source clock.Source) (*liveTraceRecorder, error) {
	if strings.TrimSpace(destination) == "" {
		return nil, errors.New("live trace recorder destination is empty")
	}
	if source == nil {
		return nil, errors.New("live trace clock is required")
	}
	parent := filepath.Dir(filepath.Clean(destination))
	if err := os.MkdirAll(parent, liveTraceDirectoryMode); err != nil {
		return nil, fmt.Errorf("create live trace parent: %w", err)
	}
	tracePath, err := os.MkdirTemp(parent, ".session-audio-trace-")
	if err != nil {
		return nil, fmt.Errorf("stage live audio trace: %w", err)
	}
	trace, err := recording.NewTrace(tracePath, source)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open live audio trace: %w", err), os.RemoveAll(tracePath))
	}
	return &liveTraceRecorder{
		inner:        inner,
		trace:        trace,
		tracePath:    tracePath,
		destination:  destination,
		providerPath: strings.TrimSpace(providerPath),
		providerRate: providerRate,
	}, nil
}

func liveTraceSource(scheduler clock.Scheduler) clock.Source {
	if scheduler != nil {
		return scheduler
	}
	return clock.Real{}
}

func (r *liveTraceRecorder) RecordMessage(ctx context.Context, record runtimeSession.LiveRecord) error {
	if r == nil {
		return runtimeRecording.ErrLiveEvidenceClosed
	}
	if r.inner == nil {
		return nil
	}
	return r.inner.RecordMessage(ctx, record)
}

func (r *liveTraceRecorder) RecordAudio(ctx context.Context, record runtimeSession.LiveAudioRecord) error {
	if r == nil {
		return runtimeRecording.ErrLiveEvidenceClosed
	}
	innerErr := error(nil)
	if r.inner != nil {
		innerErr = r.inner.RecordAudio(ctx, record)
	}
	traceErr := r.captureAudio(ctx, record)
	return errors.Join(innerErr, traceErr)
}

func (r *liveTraceRecorder) RecordEvent(ctx context.Context, event runtimeSession.LiveEvent) error {
	if r == nil {
		return runtimeRecording.ErrLiveEvidenceClosed
	}
	if r.inner == nil {
		return nil
	}
	return r.inner.RecordEvent(ctx, event)
}

func (r *liveTraceRecorder) captureAudio(ctx context.Context, record runtimeSession.LiveAudioRecord) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if r.trace == nil || len(record.Frame.Samples) == 0 {
		return nil
	}
	rate := record.Frame.Format.SampleRate
	if record.Direction == runtimeSession.LiveRecordAgent && r.providerRate > 0 {
		rate = r.providerRate
	}
	if rate <= 0 {
		return fmt.Errorf("live audio trace sample rate is unavailable")
	}
	samples := record.Frame.Samples
	switch record.Direction {
	case runtimeSession.LiveRecordClient:
		if record.Admission == runtimeSession.LiveAudioQueueAdmitted {
			r.trace.CaptureMicrophonePreGate(rate, samples)
		} else {
			r.trace.CaptureMicrophoneUploaded(rate, samples)
		}
	case runtimeSession.LiveRecordAgent:
		return r.trace.CaptureSpeakerEnqueued(ctx, rate, samples)
	}
	return nil
}

func (r *liveTraceRecorder) Finalize(ctx context.Context, runErr error) error {
	if r == nil {
		return nil
	}
	r.finalizeOnce.Do(func() {
		innerErr := error(nil)
		if r.inner != nil {
			innerErr = r.inner.Finalize(ctx, runErr)
		}
		projectionErr := error(nil)
		if innerErr == nil && runErr == nil && r.providerCapturePath() != "" {
			projectionErr = projectProviderCapture(r.trace, r.providerCapturePath())
		}
		closeErr := r.trace.Close()
		result := errors.Join(runErr, innerErr, projectionErr, closeErr)
		if result == nil {
			result = attachLiveTrace(r.tracePath, r.destination)
		} else {
			result = errors.Join(result, fmt.Errorf("live session audio trace retained at %s", r.tracePath))
		}
		r.finalizeErr = result
	})
	return r.finalizeErr
}

func (r *liveTraceRecorder) providerCapturePath() string {
	if r == nil {
		return ""
	}
	return r.providerPath
}

func attachLiveTrace(staged, bundle string) error {
	destination := filepath.Join(bundle, "audio-trace")
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("audio trace destination exists; evidence retained at %s", staged)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect audio trace destination: %w; evidence retained at %s", err, staged)
	}
	if err := os.Rename(staged, destination); err != nil {
		return fmt.Errorf("attach audio trace: %w; evidence retained at %s", err, staged)
	}
	return nil
}

func projectProviderCapture(trace *recording.Trace, path string) error {
	if trace == nil {
		return errors.New("live audio trace is unavailable")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("provider capture path is unavailable for live audio trace")
	}
	loaded, err := gatewaytesting.LoadSessionCaptureForReplay(path)
	if err != nil {
		return fmt.Errorf("load provider capture for audio trace: %w", err)
	}
	calls := make(map[string]string)
	for _, event := range loaded.Capture.Records {
		if err := projectProviderEvent(trace, event, calls); err != nil {
			return err
		}
	}
	return nil
}

func projectProviderEvent(trace *recording.Trace, event gatewaytesting.CapturedSessionEvent, calls map[string]string) error {
	wire, err := traceWireEnvelope(event)
	if err != nil {
		return fmt.Errorf("project provider wire sequence %d: %w", event.Sequence, err)
	}
	kind := "provider_wire_receive"
	if event.Direction == gatewaytesting.DirectionClientToServer {
		kind = "provider_wire_send"
	}
	trace.ObserveRuntime(recording.RuntimeEvent{Kind: kind, Tick: uint64(event.Sequence), Clean: true, Payload: wire})
	return projectProviderToolEvent(trace, event, calls)
}

func projectProviderToolEvent(trace *recording.Trace, event gatewaytesting.CapturedSessionEvent, calls map[string]string) error {
	switch event.Type {
	case "response.function_call_arguments.done":
		callPayload, callID, callName, err := traceToolCall(event.Payload)
		if err != nil {
			return fmt.Errorf("project tool call sequence %d: %w", event.Sequence, err)
		}
		calls[callID] = callName
		trace.ObserveRuntime(recording.RuntimeEvent{Kind: "tool_call", Tick: uint64(event.Sequence), Clean: true, Payload: callPayload})
	case "conversation.item.create":
		resultPayload, callID, ok, err := traceToolResult(event.Payload, calls)
		if err != nil {
			return fmt.Errorf("project tool result sequence %d: %w", event.Sequence, err)
		}
		if ok {
			trace.ObserveRuntime(recording.RuntimeEvent{Kind: "tool_result", Tick: uint64(event.Sequence), Clean: true, Payload: resultPayload})
			delete(calls, callID)
		}
	}
	return nil
}

func traceWireEnvelope(event gatewaytesting.CapturedSessionEvent) ([]byte, error) {
	payload := event.Payload
	if len(payload) == 0 {
		payload = event.Data
	}
	if len(payload) == 0 {
		return nil, errors.New("provider payload is empty")
	}
	messageType := websocketTextMessage
	var textPayload json.RawMessage
	var binaryPayload []byte
	if json.Valid(payload) {
		textPayload = append(json.RawMessage(nil), payload...)
	} else {
		messageType = websocketBinaryMessage
		binaryPayload = append([]byte(nil), payload...)
	}
	return json.Marshal(struct {
		MessageType   int             `json:"message_type"`
		Payload       json.RawMessage `json:"payload,omitempty"`
		BinaryPayload []byte          `json:"binary_payload,omitempty"`
	}{MessageType: messageType, Payload: textPayload, BinaryPayload: binaryPayload})
}

func traceToolCall(payload []byte) ([]byte, string, string, error) {
	var raw struct {
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, "", "", err
	}
	raw.CallID = strings.TrimSpace(raw.CallID)
	raw.Name = strings.TrimSpace(raw.Name)
	if raw.CallID == "" || raw.Name == "" || !json.Valid([]byte(raw.Arguments)) {
		return nil, "", "", errors.New("function call identity or arguments are invalid")
	}
	encoded, err := json.Marshal(struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}{ID: raw.CallID, Name: raw.Name, Arguments: raw.Arguments})
	return encoded, raw.CallID, raw.Name, err
}

func traceToolResult(payload []byte, calls map[string]string) ([]byte, string, bool, error) {
	var raw struct {
		Item struct {
			Type   string          `json:"type"`
			CallID string          `json:"call_id"`
			Output json.RawMessage `json:"output"`
		} `json:"item"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, "", false, err
	}
	if raw.Item.Type != "function_call_output" {
		return nil, "", false, nil
	}
	callID := strings.TrimSpace(raw.Item.CallID)
	name := strings.TrimSpace(calls[callID])
	if callID == "" || name == "" || len(raw.Item.Output) == 0 {
		return nil, "", false, errors.New("function call output identity is incomplete")
	}
	content := ""
	if err := json.Unmarshal(raw.Item.Output, &content); err != nil {
		if !json.Valid(raw.Item.Output) {
			return nil, "", false, fmt.Errorf("function call output is invalid: %w", err)
		}
		content = string(raw.Item.Output)
	}
	encoded, err := json.Marshal(struct {
		CallID   string `json:"call_id"`
		Name     string `json:"name"`
		Failed   bool   `json:"failed"`
		Response struct {
			ToolCallID string `json:"tool_call_id"`
			Name       string `json:"name"`
			Content    string `json:"content"`
		} `json:"response"`
	}{CallID: callID, Name: name, Response: struct {
		ToolCallID string `json:"tool_call_id"`
		Name       string `json:"name"`
		Content    string `json:"content"`
	}{ToolCallID: callID, Name: name, Content: content}})
	return encoded, callID, true, err
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

var _ runtimeSession.LiveRecorder = (*liveTraceRecorder)(nil)
