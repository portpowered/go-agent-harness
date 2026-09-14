package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserconversation"
)

const (
	defaultRecorderEvents = 4096
	defaultRecorderBytes  = 16 << 20
	recorderCloseTimeout  = time.Second
)

type recorder struct {
	mu           sync.Mutex
	request      browserconversation.RecordingRequest
	events       []browserconversation.RecordedBrowserEvent
	sourceEvents []browserconversation.BrowserEvent
	bytes        int
	err          error
	started      bool
	closing      bool
	closed       bool
	cancel       context.CancelFunc
	done         chan struct{}
}

func newRecorder(request browserconversation.RecordingRequest) (browserconversation.Recorder, error) {
	if request.Watch == nil {
		return nil, errors.New("browser conversation recorder watch is required")
	}
	if request.MaxEvents < 0 || request.MaxBytes < 0 {
		return nil, errors.New("browser conversation recorder bounds must not be negative")
	}
	if request.MaxEvents == 0 {
		request.MaxEvents = defaultRecorderEvents
	}
	if request.MaxBytes == 0 {
		request.MaxBytes = defaultRecorderBytes
	}
	request.Credentials = append([]string(nil), request.Credentials...)
	return &recorder{request: request}, nil
}

func (r *recorder) Start(ctx context.Context) {
	if r == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	if r.started || r.closing || r.closed {
		r.mu.Unlock()
		return
	}
	child, cancel := context.WithCancel(ctx)
	r.started = true
	r.cancel = cancel
	r.done = make(chan struct{})
	done := r.done
	r.mu.Unlock()
	events := r.request.Watch(child)
	if events == nil {
		close(done)
		return
	}
	go func() {
		defer close(done)
		for event := range events {
			r.record(event)
		}
	}()
}

func (r *recorder) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		err := r.err
		r.mu.Unlock()
		return err
	}
	if r.closing {
		done := r.done
		r.mu.Unlock()
		if done != nil && !waitRecorderDone(done) {
			r.mu.Lock()
			r.closed = true
			r.closing = false
			if r.err == nil {
				r.err = errors.New("browser conversation recorder did not stop")
			}
			err := r.err
			r.mu.Unlock()
			return err
		}
		r.mu.Lock()
		err := r.err
		r.mu.Unlock()
		return err
	}
	r.closing = true
	cancel, done := r.cancel, r.done
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil && !waitRecorderDone(done) {
		r.mu.Lock()
		r.closed = true
		r.closing = false
		if r.err == nil {
			r.err = errors.New("browser conversation recorder did not stop")
		}
		err := r.err
		r.mu.Unlock()
		return err
	}
	r.mu.Lock()
	r.closed = true
	r.closing = false
	defer r.mu.Unlock()
	return r.err
}

func waitRecorderDone(done <-chan struct{}) bool {
	timer := time.NewTimer(recorderCloseTimeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func (r *recorder) Snapshot() (browserconversation.RecordingSnapshot, error) {
	if r == nil {
		return browserconversation.RecordingSnapshot{}, errors.New("browser conversation recorder is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	events := append([]browserconversation.RecordedBrowserEvent(nil), r.events...)
	for i := range events {
		events[i].Tools = append([]string(nil), events[i].Tools...)
		events[i].RemovedToolNames = append([]string(nil), events[i].RemovedToolNames...)
	}
	if r.err != nil {
		return browserconversation.RecordingSnapshot{Events: events}, r.err
	}
	artifact, err := buildRecordingArtifact(r.sourceEvents, r.request)
	return browserconversation.RecordingSnapshot{Events: events, Artifact: artifact}, err
}

func (r *recorder) record(event browserconversation.BrowserEvent) {
	value := browserconversation.RecordedBrowserEvent{
		Sequence: event.Sequence, At: event.At, Type: event.Type,
		BrowserID: event.BrowserID, TargetID: event.TargetID,
		Generation: event.Generation, PreviousGeneration: event.PreviousGeneration,
		InvocationID: event.InvocationID, FrameID: event.FrameID, ToolName: event.ToolName,
		State: event.State, Status: event.Status, ErrorCode: event.ErrorCode,
		Reason: sanitizeRecordingText(event.Reason, r.request.Credentials),
		Tools:  recordingToolNames(event), RemovedToolNames: append([]string(nil), event.RemovedToolNames...),
	}
	if r.request.IncludeArguments {
		value.Input = safeRecordingJSON(event.Input, r.request.Credentials)
	}
	if r.request.IncludeResults {
		value.Output = safeRecordingJSON(event.Output, r.request.Credentials)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		r.setError(fmt.Errorf("marshal browser event: %w", err))
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.err != nil {
		return
	}
	if len(r.events) >= r.request.MaxEvents || r.bytes+len(encoded) > r.request.MaxBytes {
		r.err = errors.New("browser conversation recorder bound exceeded")
		return
	}
	r.events = append(r.events, value)
	r.sourceEvents = append(r.sourceEvents, cloneBrowserEvent(event))
	r.bytes += len(encoded)
}

func (r *recorder) setError(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err == nil {
		r.err = err
	}
}

func recordingToolNames(event browserconversation.BrowserEvent) []string {
	if len(event.Tools) == 0 {
		return nil
	}
	names := make([]string, 0, len(event.Tools))
	for _, tool := range event.Tools {
		if name := sanitizeRecordingText(tool.Name, nil); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func safeRecordingJSON(raw json.RawMessage, credentials []string) any {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	text := sanitizeRecordingText(string(raw), credentials)
	if text == "[redacted]" {
		return text
	}
	var value any
	if json.Unmarshal([]byte(text), &value) == nil {
		return value
	}
	return "[invalid-json]"
}

func sanitizeRecordingText(value string, credentials []string) string {
	value = strings.TrimSpace(value)
	for _, credential := range credentials {
		credential = strings.TrimSpace(credential)
		if credential != "" {
			value = strings.ReplaceAll(value, credential, "[redacted]")
		}
	}
	if browserConversationContainsCredentialMarker(value) {
		return "[redacted]"
	}
	var builder strings.Builder
	for _, char := range value {
		if char == '\n' || char == '\r' || char == '\t' || char < 0x20 || char == 0x7f {
			builder.WriteByte(' ')
		} else {
			builder.WriteRune(char)
		}
		if builder.Len() >= 4096 {
			break
		}
	}
	return strings.TrimSpace(builder.String())
}

func cloneBrowserEvent(event browserconversation.BrowserEvent) browserconversation.BrowserEvent {
	clone := event
	clone.Tools = make([]browserconversation.BrowserToolDescriptor, len(event.Tools))
	for index, tool := range event.Tools {
		clone.Tools[index] = tool
		clone.Tools[index].InputSchema = append(json.RawMessage(nil), tool.InputSchema...)
	}
	clone.RemovedToolNames = append([]string(nil), event.RemovedToolNames...)
	clone.Input = append(json.RawMessage(nil), event.Input...)
	clone.Output = append(json.RawMessage(nil), event.Output...)
	return clone
}

var _ browserconversation.Recorder = (*recorder)(nil)
