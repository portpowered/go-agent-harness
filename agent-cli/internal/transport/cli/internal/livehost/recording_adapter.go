package livehost

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	runtimeRecording "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const browserRecordingStopTimeout = time.Second

// RecordingDialer is the narrow raw-wire compatibility port used by the
// provider builder. Capture admission and semantic evidence remain owned by
// the runtime recording service.
type RecordingDialer interface {
	transport.Dialer
	FlushToFile(string) error
}

// NewRecordingDialer adapts the existing provider wire recorder for the
// legacy raw --record transport seam. It owns no session or policy state.
func NewRecordingDialer(inner transport.Dialer, provider, model string) RecordingDialer {
	return testing.NewRecordingWebSocketDialer(inner, provider, model)
}

// StartBrowserRecording copies host browser observations into the public
// recording contract. Projection, redaction, ordering, bounds, and artifact
// publication remain owned by recording.Service; this adapter only translates
// the host event type and joins its bounded watcher.
func StartBrowserRecording(ctx context.Context, enabled bool, source func(context.Context) <-chan webmcp.BrowserEvent, recorder runtimeSession.LiveRecorder) func() {
	if !enabled || source == nil || recorder == nil {
		return func() {}
	}
	browser, ok := recorder.(runtimeRecording.BrowserRecorder)
	if !ok {
		return func() {}
	}
	watchContext, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	events := source(watchContext)
	if events == nil {
		close(done)
		return func() { cancel() }
	}
	go func() {
		defer close(done)
		for event := range events {
			if err := browser.RecordBrowserEvent(watchContext, browserRecordingEvent(event)); err != nil {
				return
			}
		}
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(browserRecordingStopTimeout):
		}
	}
}

func browserRecordingEvent(event webmcp.BrowserEvent) runtimeRecording.BrowserEvent {
	toolNames := make([]string, 0, len(event.Tools))
	for _, tool := range event.Tools {
		if tool.Name != "" {
			toolNames = append(toolNames, tool.Name)
		}
	}
	return runtimeRecording.BrowserEvent{
		Type: string(event.Type), At: event.At, BrowserID: string(event.BrowserID),
		TargetID: string(event.TargetID), FrameID: string(event.FrameID),
		Generation: event.Generation, PreviousGeneration: event.PreviousGeneration,
		ToolNames: toolNames, RemovedToolNames: append([]string(nil), event.RemovedToolNames...),
		ToolCount: event.ToolCount, ToolCountKnown: event.ToolCountKnown, ToolName: event.ToolName,
		InvocationID: string(event.InvocationID), Status: event.Status,
		Input: append([]byte(nil), event.Input...), Output: append([]byte(nil), event.Output...),
		ErrorCode: event.ErrorCode, Reason: event.Reason,
	}
}
