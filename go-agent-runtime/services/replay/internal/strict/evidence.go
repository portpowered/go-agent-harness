package strict

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/recording"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

type evidenceBuilder struct {
	capture         gwtesting.SessionCapture
	wires           []gwtesting.CapturedSessionEvent
	wireTypes       []int
	tools           *recordedToolExecutor
	firstSendType   string
	handshakeModel  string
	createdModel    string
	sawClosed       bool
	sawResponseDone bool
	terminalDone    bool
}

func deriveEvidence(events []recording.Event, request replay.Request) (gwtesting.SessionCapture, *recordedToolExecutor, []int, int, int, error) {
	builder := newEvidenceBuilder()
	for _, event := range events {
		if event.Kind != runtimeEventKind {
			continue
		}
		if err := builder.consume(event); err != nil {
			return gwtesting.SessionCapture{}, nil, nil, 0, 0, err
		}
	}
	if err := builder.validate(request); err != nil {
		return gwtesting.SessionCapture{}, nil, nil, 0, 0, err
	}
	if err := builder.tools.validateShape(); err != nil {
		return gwtesting.SessionCapture{}, nil, nil, 0, 0, err
	}
	builder.finishCapture(request)
	sealed, err := gwtesting.SealSessionCapture(builder.capture)
	if err != nil {
		return gwtesting.SessionCapture{}, nil, nil, 0, 0, fmt.Errorf("%w: seal derived capture: %w", replay.ErrBundleMismatch, err)
	}
	return sealed, builder.tools, builder.wireTypes, len(builder.wires), len(builder.tools.calls), nil
}

func newEvidenceBuilder() *evidenceBuilder {
	return &evidenceBuilder{
		capture: gwtesting.SessionCapture{
			Version: gwtesting.SessionCaptureVersion,
			Session: gwtesting.SessionMetadata{FixtureProvenance: gwtesting.SessionFixtureProvenanceProviderRecorded},
		},
		tools: newRecordedToolExecutor(),
	}
}

func (b *evidenceBuilder) consume(event recording.Event) error {
	switch event.RuntimeKind {
	case providerWireSend, providerWireReceive:
		return b.consumeWire(event)
	case toolCallKind:
		call, err := decodeToolCall(event.Payload)
		if err != nil {
			return fmt.Errorf("%w: tool call: %w", replay.ErrBundleIncomplete, err)
		}
		return b.tools.addCall(call)
	case toolResultKind:
		result, err := decodeToolResult(event.Payload)
		if err != nil {
			return fmt.Errorf("%w: tool result: %w", replay.ErrBundleIncomplete, err)
		}
		return b.tools.addResult(result)
	default:
		return nil
	}
}

func (b *evidenceBuilder) consumeWire(event recording.Event) error {
	if b.skipFailedReceive(event) {
		return nil
	}
	if len(event.Payload) == 0 || (event.RuntimeKind == providerWireSend && !event.Clean) {
		return fmt.Errorf("%w: provider wire %s has no successful payload", replay.ErrBundleIncomplete, event.RuntimeKind)
	}
	messageType, rawPayload, wireType, err := decodeWireEnvelope(event.Payload)
	if err != nil {
		return fmt.Errorf("%w: provider wire %s: %w", replay.ErrBundleIncomplete, event.RuntimeKind, err)
	}
	if err := b.observeWireMetadata(event, wireType, rawPayload); err != nil {
		return err
	}
	ms, ok := elapsedMilliseconds(event.ElapsedNS)
	if !ok {
		return fmt.Errorf("%w: provider wire timestamp overflows milliseconds", replay.ErrBundleIncomplete)
	}
	direction := gwtesting.DirectionServerToClient
	if event.RuntimeKind == providerWireSend {
		direction = gwtesting.DirectionClientToServer
	}
	b.wireTypes = append(b.wireTypes, messageType)
	b.wires = append(b.wires, gwtesting.CapturedSessionEvent{
		Sequence:    len(b.wires) + 1,
		Direction:   direction,
		TimestampMs: ms,
		Type:        wireType,
		PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
		Payload:     append(json.RawMessage(nil), rawPayload...),
	})
	return nil
}

func (b *evidenceBuilder) skipFailedReceive(event recording.Event) bool {
	if event.RuntimeKind != providerWireReceive || !emptyFailedWireObservation(event) {
		return false
	}
	return b.sawResponseDone
}

func (b *evidenceBuilder) observeWireMetadata(event recording.Event, wireType string, payload []byte) error {
	if event.RuntimeKind == providerWireSend {
		b.terminalDone = false
	}
	if event.RuntimeKind == providerWireSend && b.firstSendType == "" {
		b.firstSendType = wireType
		if wireType == sessionUpdateType {
			b.handshakeModel = sessionUpdateModel(payload)
		}
	}
	if event.RuntimeKind == providerWireReceive && wireType == "session.created" {
		if err := b.recordCreatedModel(sessionCreatedModel(payload)); err != nil {
			return err
		}
	}
	if event.RuntimeKind == providerWireReceive && wireType == sessionClosedType {
		b.sawClosed = true
	}
	if event.RuntimeKind == providerWireReceive && wireType == "response.done" {
		b.sawResponseDone = true
		b.terminalDone = true
	}
	if event.RuntimeKind == providerWireReceive && wireType != "response.done" && wireType != sessionClosedType {
		b.terminalDone = false
	}
	return nil
}

func (b *evidenceBuilder) recordCreatedModel(model string) error {
	if model == "" {
		return nil
	}
	if b.createdModel != "" && b.createdModel != model {
		return fmt.Errorf("%w: captured session.created model changes from %q to %q", replay.ErrBundleMismatch, b.createdModel, model)
	}
	b.createdModel = model
	return nil
}

func (b *evidenceBuilder) validate(request replay.Request) error {
	if len(b.wires) == 0 || b.firstSendType != sessionUpdateType {
		return fmt.Errorf("%w: missing initial provider session.update handshake", replay.ErrBundleIncomplete)
	}
	if b.handshakeModel != "" && b.createdModel != "" && b.handshakeModel != b.createdModel {
		return fmt.Errorf("%w: captured handshake model %q differs from session.created model %q", replay.ErrBundleMismatch, b.handshakeModel, b.createdModel)
	}
	if !b.sawResponseDone || !b.terminalDone {
		return fmt.Errorf("%w: provider session has no terminal response.done completion", replay.ErrBundleIncomplete)
	}
	verifiedModel := b.handshakeModel
	if verifiedModel == "" {
		verifiedModel = b.createdModel
	}
	if request.Model != "" {
		if verifiedModel == "" {
			return fmt.Errorf("%w: requested model %q has no captured handshake model to verify", replay.ErrBundleIncomplete, request.Model)
		}
		if request.Model != verifiedModel {
			return fmt.Errorf("%w: requested model %q differs from captured handshake model %q", replay.ErrBundleMismatch, request.Model, verifiedModel)
		}
	}
	return nil
}

func (b *evidenceBuilder) finishCapture(request replay.Request) {
	if !b.sawResponseDone && !b.sawClosed {
		b.capture.EndsWithDisconnect = true
	}
	if request.Provider != "" {
		b.capture.Provider.Name = request.Provider
	}
	b.capture.Provider.Model = request.Model
	if b.capture.Provider.Model == "" {
		b.capture.Provider.Model = b.handshakeModel
		if b.capture.Provider.Model == "" {
			b.capture.Provider.Model = b.createdModel
		}
	}
	b.capture.Records = b.wires
}

func emptyFailedWireObservation(event recording.Event) bool {
	if event.Clean || len(event.Payload) == 0 {
		return false
	}
	var envelope struct {
		MessageType   int             `json:"message_type"`
		Payload       json.RawMessage `json:"payload"`
		BinaryPayload []byte          `json:"binary_payload"`
	}
	if json.Unmarshal(event.Payload, &envelope) != nil {
		return false
	}
	return envelope.MessageType <= 0 && len(envelope.Payload) == 0 && len(envelope.BinaryPayload) == 0
}

func sessionUpdateModel(payload []byte) string  { return capturedModel(payload) }
func sessionCreatedModel(payload []byte) string { return capturedModel(payload) }

func capturedModel(payload []byte) string {
	var envelope struct {
		Session struct {
			Model string `json:"model"`
		} `json:"session"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return ""
	}
	return strings.TrimSpace(envelope.Session.Model)
}

func elapsedMilliseconds(nanos int64) (int64, bool) {
	if nanos < 0 {
		return 0, false
	}
	return nanos / int64(time.Millisecond), true
}

func decodeWireEnvelope(payload []byte) (int, []byte, string, error) {
	var envelope struct {
		MessageType   int             `json:"message_type"`
		Payload       json.RawMessage `json:"payload"`
		BinaryPayload []byte          `json:"binary_payload"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return 0, nil, "", err
	}
	if envelope.MessageType <= 0 {
		return 0, nil, "", errors.New("message_type is invalid")
	}
	if len(envelope.Payload) > 0 && string(envelope.Payload) != "null" {
		typeName, err := payloadType(envelope.Payload)
		return envelope.MessageType, append([]byte(nil), envelope.Payload...), typeName, err
	}
	if len(envelope.BinaryPayload) > 0 {
		return envelope.MessageType, append([]byte(nil), envelope.BinaryPayload...), "binary", nil
	}
	return 0, nil, "", errors.New("wire payload is empty")
}

func payloadType(payload []byte) (string, error) {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return "", err
	}
	if strings.TrimSpace(envelope.Type) == "" {
		return "", errors.New("payload type is empty")
	}
	return envelope.Type, nil
}
