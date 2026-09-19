package agentruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

const sessionBrowserRecordingStopTimeout = time.Second

// sessionBrowserRecording is a passive adapter-event observer. It deliberately
// has no executor, provider session, or continuation reference: the broker
// owns the event stream and the ordinary session lifecycle owns delivery.
type sessionBrowserRecording struct {
	watch            func(context.Context) <-chan webmcp.BrowserEvent
	includeArguments bool
	includeResults   bool
	policy           testkit.RedactionPolicy
	credentials      []string
	recorder         *testkit.Recorder

	mu        sync.Mutex
	events    []testkit.Event
	recordErr error
	failed    bool
	started   bool
	cancel    context.CancelFunc
	done      chan struct{}
}

func newSessionBrowserRecording(opts SessionRunOptions, plan sessionRuntimePlan) *sessionBrowserRecording {
	if opts.LoadedConfig == nil || !opts.LoadedConfig.Browser.Recording.Enabled || opts.BrowserEventWatch == nil {
		return nil
	}
	settings := opts.LoadedConfig.Browser.Recording
	credentials := sessionRecordingCredentials(opts, plan)
	policy := testkit.RedactionPolicy{
		URLQuery:    settings.RedactURLQuery,
		URLFragment: settings.RedactURLFragment,
	}
	recorder, err := testkit.NewRecorder(
		io.Discard,
		testkit.WithClockFunc(func() uint64 { return 0 }),
		testkit.WithRedactionConfig(testkit.RedactionConfig{Policy: policy, Credentials: credentials}),
	)
	return &sessionBrowserRecording{
		watch:            opts.BrowserEventWatch,
		includeArguments: settings.IncludeArguments,
		includeResults:   settings.IncludeResults,
		policy:           policy,
		credentials:      append([]string(nil), credentials...),
		recorder:         recorder,
		recordErr:        err,
	}
}

func (r *sessionBrowserRecording) start(ctx context.Context) {
	if r == nil || r.watch == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return
	}
	child, cancel := context.WithCancel(ctx)
	r.started = true
	r.cancel = cancel
	r.done = make(chan struct{})
	r.mu.Unlock()

	events := r.watch(child)
	if events == nil {
		close(r.done)
		return
	}
	go func() {
		defer close(r.done)
		for event := range events {
			r.record(event)
		}
	}()
}

func (r *sessionBrowserRecording) stop() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if !r.started {
		r.mu.Unlock()
		return
	}
	cancel := r.cancel
	done := r.done
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(sessionBrowserRecordingStopTimeout):
		r.mu.Lock()
		if r.recordErr == nil {
			r.recordErr = errors.New("browser recording observer did not stop")
		}
		r.mu.Unlock()
	}
}

func (r *sessionBrowserRecording) record(event webmcp.BrowserEvent) {
	inputs, err := sessionBrowserEventInputs(event, r.includeArguments, r.includeResults)
	if err != nil {
		r.mu.Lock()
		if r.recordErr == nil {
			r.recordErr = fmt.Errorf("convert browser event %s: %w", event.Type, err)
		}
		r.failed = true
		r.mu.Unlock()
		return
	}
	for _, input := range inputs {
		r.mu.Lock()
		if r.failed || r.recorder == nil {
			r.mu.Unlock()
			return
		}
		recorded, recordErr := r.recorder.Record(input)
		if recordErr != nil {
			if r.recordErr == nil {
				r.recordErr = fmt.Errorf("record browser event %s: %w", event.Type, recordErr)
			}
			r.failed = true
			r.mu.Unlock()
			return
		}
		r.events = append(r.events, recorded)
		r.mu.Unlock()
	}
}

func (r *sessionBrowserRecording) artifact() (*transcript.BrowserArtifact, error) {
	if r == nil {
		return nil, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recordErr != nil {
		return nil, r.recordErr
	}
	if len(r.events) == 0 {
		return nil, nil
	}
	events := append([]testkit.Event(nil), r.events...)
	artifact, err := testkit.BuildRedactedBrowserArtifact(events, testkit.RedactionConfig{
		Policy:      r.policy,
		Credentials: append([]string(nil), r.credentials...),
	})
	if err != nil {
		return nil, err
	}
	converted := artifact.RecordingArtifact(transcript.BrowserArtifactDefaultPath)
	return &converted, nil
}

func sessionBrowserEventInputs(event webmcp.BrowserEvent, includeArguments, includeResults bool) ([]testkit.EventInput, error) {
	inputs := make([]testkit.EventInput, 0, 2)
	add := func(eventType testkit.EventType, generation uint64, payload map[string]any) error {
		if strings.TrimSpace(string(event.BrowserID)) == "" || strings.TrimSpace(string(event.TargetID)) == "" {
			return nil
		}
		input, err := testkit.NewEventInput(eventType, payload)
		if err != nil {
			return err
		}
		input.BrowserID = string(event.BrowserID)
		input.TargetID = string(event.TargetID)
		input.Generation = generation
		inputs = append(inputs, input)
		return nil
	}

	toolNames := make([]string, 0, len(event.Tools))
	for _, tool := range event.Tools {
		if tool.Name != "" {
			toolNames = append(toolNames, tool.Name)
		}
	}
	switch event.Type {
	case webmcp.EventTargetAttached:
		if err := add(testkit.EventBrowserChromeTargetAttached, 0, map[string]any{"phase": "attached"}); err != nil {
			return nil, err
		}
	case webmcp.EventToolsAdded:
		count := len(toolNames)
		if event.ToolCountKnown {
			count = event.ToolCount
		}
		if err := add(testkit.EventBrowserCatalogToolAdded, event.Generation, map[string]any{
			"tools":      toolNames,
			"tool_count": count,
		}); err != nil {
			return nil, err
		}
	case webmcp.EventToolsRemoved:
		if err := add(testkit.EventBrowserCatalogToolRemoved, event.Generation, map[string]any{
			"tools": append([]string(nil), event.RemovedToolNames...),
		}); err != nil {
			return nil, err
		}
	case webmcp.EventCatalogReady:
		count := event.ToolCount
		if !event.ToolCountKnown {
			count = len(toolNames)
		}
		if err := add(testkit.EventBrowserCatalogReady, event.Generation, map[string]any{
			"tool_count":    count,
			"schema_digest": sessionBrowserSchemaDigest(event.Tools),
		}); err != nil {
			return nil, err
		}
	case webmcp.EventToolInvoked:
		if strings.TrimSpace(string(event.InvocationID)) == "" {
			return inputs, nil
		}
		created := map[string]any{
			"invocation_id": string(event.InvocationID),
			"tool_name":     event.ToolName,
		}
		if event.FrameID != "" {
			created["frame_id"] = string(event.FrameID)
		}
		if err := add(testkit.EventBrowserInvocationCreated, event.Generation, created); err != nil {
			return nil, err
		}
		dispatched := map[string]any{"invocation_id": string(event.InvocationID)}
		if includeArguments {
			dispatched["input"] = sessionBrowserRawValue(event.Input)
		}
		if err := add(testkit.EventBrowserInvocationDispatched, event.Generation, dispatched); err != nil {
			return nil, err
		}
	case webmcp.EventToolResponded:
		if strings.TrimSpace(string(event.InvocationID)) == "" {
			return inputs, nil
		}
		status := strings.ToLower(strings.TrimSpace(event.Status))
		reason := strings.TrimSpace(event.Reason)
		if reason == "" {
			reason = status
		}
		switch {
		case status == "canceled" || status == "cancelled" || status == "timed_out" || status == "timeout" || status == "timedout":
			if err := add(testkit.EventBrowserInvocationCanceled, event.Generation, map[string]any{
				"invocation_id": string(event.InvocationID),
				"source":        "browser",
				"reason":        reason,
			}); err != nil {
				return nil, err
			}
		case event.ErrorCode != "" || status == "error" || status == "failed":
			fields := map[string]any{
				"invocation_id": string(event.InvocationID),
				"code":          sessionBrowserErrorCode(event),
			}
			if includeResults && reason != "" {
				fields["message"] = reason
			}
			if includeResults && len(event.Output) > 0 {
				fields["error"] = sessionBrowserRawValue(event.Output)
			}
			if err := add(testkit.EventBrowserInvocationError, event.Generation, fields); err != nil {
				return nil, err
			}
		default:
			fields := map[string]any{
				"invocation_id": string(event.InvocationID),
				"status":        event.Status,
			}
			if fields["status"] == "" {
				fields["status"] = string(webmcp.InvocationCompleted)
			}
			if includeResults {
				fields["output"] = sessionBrowserRawValue(event.Output)
			}
			if err := add(testkit.EventBrowserInvocationCompleted, event.Generation, fields); err != nil {
				return nil, err
			}
		}
	case webmcp.EventPageNavigated, webmcp.EventFrameNavigated:
		reason := strings.TrimSpace(event.Reason)
		if reason == "" {
			reason = "navigation"
		}
		if err := add(testkit.EventBrowserPageGenerationChanged, 0, map[string]any{
			"previous_generation": event.PreviousGeneration,
			"current_generation":  event.Generation,
			"reason":              reason,
		}); err != nil {
			return nil, err
		}
	case webmcp.EventTargetDetached, webmcp.EventBrowserDisconnected:
		if err := add(testkit.EventBrowserTargetDetached, 0, map[string]any{"reason": event.Reason}); err != nil {
			return nil, err
		}
	case webmcp.EventSessionClosed:
		if err := add(testkit.EventBrowserChromeTargetClosed, 0, map[string]any{"reason": event.Reason}); err != nil {
			return nil, err
		}
	}
	return inputs, nil
}

func sessionBrowserRawValue(raw json.RawMessage) any {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return json.RawMessage(append([]byte(nil), raw...))
}

func sessionBrowserSchemaDigest(tools []webmcp.ToolDescriptor) string {
	digest := sha256.New()
	for _, tool := range tools {
		digest.Write([]byte(tool.Name))
		digest.Write([]byte{0})
		digest.Write(bytes.TrimSpace(tool.InputSchema))
		digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func sessionBrowserErrorCode(event webmcp.BrowserEvent) string {
	if code := strings.TrimSpace(event.ErrorCode); code != "" {
		return code
	}
	return "invocation_error"
}
