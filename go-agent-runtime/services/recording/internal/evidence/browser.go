package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
)

const (
	browserEventsVersion = "webmcp.browser-events.v1"
	redactionMarker      = "REDACTED"
)

type browserEvidence struct {
	options     recording.BrowserRecordingOptions
	credentials []string
	maxBytes    int64
	maxItems    int64

	mu        sync.Mutex
	events    []browserEvent
	usedBytes int64
	usedItems int64
	next      uint64
	recordErr error
}

type browserEvent struct {
	Version     string           `json:"version"`
	Sequence    uint64           `json:"sequence"`
	MonotonicMS uint64           `json:"monotonic_ms"`
	Type        string           `json:"type"`
	BrowserID   string           `json:"browser_id,omitempty"`
	TargetID    string           `json:"target_id,omitempty"`
	Generation  *uint64          `json:"generation,omitempty"`
	Payload     json.RawMessage  `json:"payload,omitempty"`
	Redaction   browserRedaction `json:"redaction"`
}

type browserRedaction struct {
	Mode  string   `json:"mode"`
	Rules []string `json:"rules,omitempty"`
}

func newBrowserEvidence(options recording.BrowserRecordingOptions, credentials []string, limits recording.ResourceLimits) *browserEvidence {
	if !options.Enabled {
		return nil
	}
	maxBytes, maxItems := limits.SidecarBytes, limits.SidecarItems
	if maxBytes <= 0 {
		maxBytes = recording.DefaultSidecarBytes
	}
	if maxItems <= 0 {
		maxItems = recording.DefaultSidecarItems
	}
	return &browserEvidence{
		options:     options,
		credentials: append([]string(nil), credentials...),
		maxBytes:    maxBytes,
		maxItems:    maxItems,
		next:        1,
	}
}

func (b *browserEvidence) record(event recording.BrowserEvent) error {
	if b == nil {
		return nil
	}
	inputs, err := browserEventInputs(event, b.options)
	if err != nil {
		b.latchRecordError(err)
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.recordErr != nil {
		return nil
	}
	for _, input := range inputs {
		if err := b.appendBrowserEvent(input); err != nil {
			b.recordErr = err
			return nil
		}
	}
	return nil
}

func (b *browserEvidence) latchRecordError(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.recordErr == nil {
		b.recordErr = err
	}
}

func (b *browserEvidence) appendBrowserEvent(input browserEventInput) error {
	if input.BrowserID == "" || input.TargetID == "" {
		return nil
	}
	payload, redaction, err := redactBrowserPayload(input.Payload, b.options, b.credentials, input.ToolName, input.Type)
	if err != nil {
		return err
	}
	candidate := browserEvent{
		Version: browserEventsVersion, Sequence: b.next, Type: input.Type,
		BrowserID: input.BrowserID, TargetID: input.TargetID,
		Generation: browserEventGeneration(input), Payload: payload, Redaction: redaction,
	}
	encoded, err := json.Marshal(candidate)
	if err != nil {
		return fmt.Errorf("encode browser event: %w", err)
	}
	encodedBytes := int64(len(encoded)) + 1
	if b.usedBytes > b.maxBytes-encodedBytes || b.usedItems >= b.maxItems {
		return fmt.Errorf("browser evidence budget exceeded: bytes=%d/%d items=%d/%d", b.usedBytes+encodedBytes, b.maxBytes, b.usedItems+1, b.maxItems)
	}
	b.events = append(b.events, candidate)
	b.usedBytes += encodedBytes
	b.usedItems++
	b.next++
	return nil
}

func browserEventGeneration(input browserEventInput) *uint64 {
	if !input.RequiresGeneration {
		return nil
	}
	value := input.Generation
	return &value
}

func (b *browserEvidence) artifact() (*transcript.BrowserArtifact, error) {
	if b == nil {
		return nil, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.recordErr != nil {
		return nil, b.recordErr
	}
	if len(b.events) == 0 {
		return nil, nil
	}
	var data bytes.Buffer
	for _, event := range b.events {
		encoded, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		data.Write(encoded)
		data.WriteByte('\n')
	}
	sum := sha256.Sum256(data.Bytes())
	return &transcript.BrowserArtifact{
		Format: browserEventsVersion, Path: transcript.BrowserArtifactDefaultPath,
		Data: data.Bytes(), SHA256: fmt.Sprintf("%x", sum),
		Redaction: transcript.BrowserRedactionPolicy{
			URLQuery: b.options.RedactURLQuery, URLFragment: b.options.RedactURLFragment,
		},
	}, nil
}
