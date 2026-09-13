package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
)

const (
	browserEventsVersion = "webmcp.browser-events.v1"
	browserMaxEvents     = 4096
	browserMaxBytes      = 4 << 20
)

type browserEventType string

const (
	browserTargetAttached   browserEventType = "browser.chrome.target_attached"
	browserCatalogAdded     browserEventType = "browser.catalog.tool_added"
	browserCatalogRemoved   browserEventType = "browser.catalog.tool_removed"
	browserCatalogReady     browserEventType = "browser.catalog.ready"
	browserInvocationMade   browserEventType = "browser.invocation.created"
	browserInvocationSent   browserEventType = "browser.invocation.dispatched"
	browserInvocationDone   browserEventType = "browser.invocation.completed"
	browserInvocationError  browserEventType = "browser.invocation.error"
	browserInvocationCancel browserEventType = "browser.invocation.canceled"
	browserGeneration       browserEventType = "browser.page.generation_changed"
	browserTargetDetached   browserEventType = "browser.target.detached"
	browserTargetClosed     browserEventType = "browser.chrome.target_closed"
)

type browserEventInput struct {
	typeName      browserEventType
	browserID     string
	targetID      string
	generation    uint64
	generationSet bool
	payload       json.RawMessage
	at            time.Time
}

type browserWireEvent struct {
	Version       string           `json:"version"`
	Sequence      uint64           `json:"sequence"`
	MonotonicMS   uint64           `json:"monotonic_ms"`
	Type          browserEventType `json:"type"`
	BrowserID     string           `json:"browser_id,omitempty"`
	TargetID      string           `json:"target_id,omitempty"`
	Generation    *uint64          `json:"generation,omitempty"`
	Payload       json.RawMessage  `json:"payload,omitempty"`
	PayloadSHA256 string           `json:"payload_sha256,omitempty"`
	Redaction     browserRedaction `json:"redaction"`
}

type browserRedaction struct {
	Mode  string   `json:"mode"`
	Rules []string `json:"rules,omitempty"`
}

func (e browserWireEvent) MarshalJSON() ([]byte, error) {
	type wire browserWireEvent
	return json.Marshal(wire(e))
}

type browserRecorder struct {
	options     recording.BrowserOptions
	credentials []string
	base        time.Time

	mu        sync.Mutex
	events    []browserWireEvent
	bytes     int
	recordErr error
	cancel    context.CancelFunc
	done      chan struct{}
	started   bool
}

func newBrowserRecorder(options recording.BrowserOptions, credentials []string, base time.Time) *browserRecorder {
	return &browserRecorder{
		options: options, credentials: append([]string(nil), credentials...), base: base,
	}
}

func (r *browserRecorder) start() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return
	}
	r.started = true
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.done = make(chan struct{})
	source := r.options.Events
	if r.options.EventSource != nil {
		source = r.options.EventSource(ctx)
	}
	done := r.done
	r.mu.Unlock()
	if source == nil {
		close(done)
		return
	}
	go func() {
		defer close(done)
		for event := range source {
			r.record(event)
		}
	}()
}

func (r *browserRecorder) stop() {
	if r == nil {
		return
	}
	r.mu.Lock()
	cancel, done, started := r.cancel, r.done, r.started
	r.mu.Unlock()
	if !started {
		return
	}
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		r.setRecordError(errors.New("browser recording observer did not stop"))
	}
}

func (r *browserRecorder) record(event recording.BrowserEvent) {
	inputs, err := browserEventInputs(event, r.options.IncludeArguments, r.options.IncludeResults)
	if err != nil {
		r.setRecordError(fmt.Errorf("convert browser event %s: %w", event.Type, err))
		return
	}
	for _, input := range inputs {
		if err := r.recordInput(input); err != nil {
			r.setRecordError(err)
			return
		}
	}
}

func (r *browserRecorder) recordInput(input browserEventInput) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recordErr != nil {
		return r.recordErr
	}
	if len(r.events) >= browserMaxEvents || r.bytes+len(input.payload) > browserMaxBytes {
		return errors.New("browser recording evidence budget exceeded")
	}
	payload, redaction, err := redactBrowserPayload(input.payload, r.options, r.credentials)
	if err != nil {
		return err
	}
	wire := browserWireEvent{
		Version: browserEventsVersion, Sequence: uint64(len(r.events) + 1),
		MonotonicMS: browserMonotonicMS(input.at, r.base), Type: input.typeName,
		BrowserID: input.browserID, TargetID: input.targetID,
		Generation: browserGenerationValue(input), Payload: payload, Redaction: redaction,
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return err
	}
	r.bytes += len(encoded)
	r.events = append(r.events, wire)
	return nil
}

func (r *browserRecorder) setRecordError(err error) {
	if err == nil {
		return
	}
	r.mu.Lock()
	if r.recordErr == nil {
		r.recordErr = err
	}
	r.mu.Unlock()
}

func browserGenerationValue(input browserEventInput) *uint64 {
	if !input.generationSet && !browserNeedsGeneration(input.typeName) {
		return nil
	}
	value := input.generation
	return &value
}

func browserMonotonicMS(at, base time.Time) uint64 {
	if at.IsZero() || !at.After(base) {
		return 0
	}
	return uint64(at.Sub(base) / time.Millisecond)
}

func (r *browserRecorder) artifact() (*transcript.BrowserArtifact, error) {
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
	data := make([]byte, 0, r.bytes+len(r.events))
	for _, event := range r.events {
		encoded, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		data = append(data, encoded...)
		data = append(data, '\n')
	}
	if !utf8.Valid(data) {
		return nil, errors.New("browser artifact is not valid UTF-8")
	}
	policy := transcript.BrowserRedactionPolicy{
		URLQuery: r.options.RedactURLQuery, URLFragment: r.options.RedactURLFragment,
		ToolArguments:      append([]string(nil), r.options.ToolArguments...),
		ResultJSONPointers: append([]string(nil), r.options.ResultJSONPointers...),
		DigestTools:        append([]string(nil), r.options.DigestTools...),
	}
	return &transcript.BrowserArtifact{Format: browserEventsVersion, Path: transcript.BrowserArtifactDefaultPath, Data: data, Redaction: policy}, nil
}
