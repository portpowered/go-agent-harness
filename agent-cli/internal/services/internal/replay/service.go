// Package replay implements the strict offline replay service.
package replay

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/services/replay"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/recording"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const (
	providerWireSend    = "provider_wire_send"
	providerWireReceive = "provider_wire_receive"
	toolCallKind        = "tool_call"
	toolResultKind      = "tool_result"
	sessionUpdateType   = "session.update"
	sessionClosedType   = "session.closed"
)

// ClockFactory creates a fresh virtual clock for each prepared replay. The
// origin comes from the bundle's recording epoch; sharing the returned clock
// across Prepare calls would couple otherwise independent runs.
type ClockFactory func(time.Time) *clock.Deterministic

type Dependencies struct {
	ClockFactory ClockFactory
	Runtime      replay.RuntimeFactory
}

type Service struct {
	clockFactory ClockFactory
	runtime      replay.RuntimeFactory
}

var _ replay.Service = (*Service)(nil)

func New(deps Dependencies) *Service {
	return &Service{clockFactory: deps.ClockFactory, runtime: deps.Runtime}
}

func (s *Service) Run(ctx context.Context, out io.Writer, request replay.Request) (replay.Result, error) {
	if out == nil {
		return replay.Result{}, fmt.Errorf("%w: output is nil", replay.ErrRuntimeFactoryRequired)
	}
	if s == nil || s.runtime == nil {
		return replay.Result{}, replay.ErrRuntimeFactoryRequired
	}
	prepared, err := s.Prepare(ctx, request)
	if err != nil {
		return replay.Result{}, err
	}
	runtime, err := s.runtime.New(prepared)
	if err != nil {
		return replay.Result{}, errors.Join(fmt.Errorf("construct offline replay runtime: %w", err), prepared.ValidateComplete())
	}
	if runtime == nil {
		return replay.Result{}, errors.Join(replay.ErrRuntimeFactoryRequired, prepared.ValidateComplete())
	}
	runErr := runtime.Run(ctx, out)
	validationErr := prepared.ValidateComplete()
	return replay.Result{Capture: prepared.Capture, Scope: prepared.Scope, WireEvents: prepared.WireEvents, ToolCalls: prepared.ToolCalls}, errors.Join(runErr, validationErr)
}

func (s *Service) Prepare(ctx context.Context, request replay.Request) (replay.Prepared, error) {
	if err := contextError(ctx); err != nil {
		return replay.Prepared{}, err
	}
	if s == nil || s.clockFactory == nil {
		return replay.Prepared{}, replay.ErrDeterministicClockRequired
	}
	bundlePath := strings.TrimSpace(request.BundlePath)
	if bundlePath == "" {
		return replay.Prepared{}, fmt.Errorf("%w: bundle path is empty", replay.ErrBundleIncomplete)
	}
	tracePath, err := resolveTraceDirectory(bundlePath)
	if err != nil {
		return replay.Prepared{}, err
	}
	events, err := readTimeline(tracePath)
	if err != nil {
		return replay.Prepared{}, err
	}
	origin, err := timelineOrigin(events)
	if err != nil {
		return replay.Prepared{}, err
	}
	deterministic := s.clockFactory(origin)
	if deterministic == nil {
		return replay.Prepared{}, replay.ErrDeterministicClockRequired
	}
	audioReplay, err := recording.OpenReplay(tracePath)
	if err != nil {
		return replay.Prepared{}, fmt.Errorf("%w: open audio trace: %v", replay.ErrBundleIncomplete, err)
	}
	audioReplay.Clock = deterministic
	capture, toolExecutor, wireTypes, wireCount, toolCount, err := deriveEvidence(events, request)
	if err != nil {
		return replay.Prepared{}, err
	}
	scope := deriveScope(events)
	dialer, err := gwtesting.NewReplayWebSocketDialerFromCapture(capture)
	if err != nil {
		return replay.Prepared{}, fmt.Errorf("%w: construct replay dialer: %v", replay.ErrBundleMismatch, err)
	}
	state := &replayState{expected: wireCount, messageTypes: wireTypes}
	var trackedDialer transport.Dialer = &trackingDialer{inner: dialer, state: state}
	return replay.NewPrepared(capture, trackedDialer, toolExecutor, audioReplay, deterministic, scope, wireCount, toolCount, func() error {
		if err := state.validate(); err != nil {
			return err
		}
		return toolExecutor.validateComplete()
	}), nil
}

func deriveScope(events []recording.Event) replay.EvidenceScope {
	scope := replay.EvidenceScope{}
	for _, event := range events {
		if event.Kind == "audio" {
			scope.RecordedPCM = true
			if event.Tap == "speaker_rendered" {
				scope.RecordedRender = true
			}
		}
		if event.Kind == "runtime" && event.RuntimeKind == "audio_render_tap_unavailable" {
			scope.RenderTapUnavailable = true
		}
	}
	// Prepare cannot perform device scheduling or DSP. Protocol/tool replay is
	// the only execution scope this service itself can claim.
	scope.Protocol = true
	scope.Tools = true
	return scope
}

func resolveTraceDirectory(bundlePath string) (string, error) {
	if info, err := os.Stat(filepath.Join(bundlePath, "timeline.jsonl")); err == nil && !info.IsDir() {
		return bundlePath, nil
	}
	tracePath := filepath.Join(bundlePath, "audio-trace")
	if info, err := os.Stat(filepath.Join(tracePath, "timeline.jsonl")); err == nil && !info.IsDir() {
		return tracePath, nil
	}
	return "", fmt.Errorf("%w: missing timeline.jsonl in %s or %s", replay.ErrBundleIncomplete, bundlePath, tracePath)
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

func readTimeline(directory string) ([]recording.Event, error) {
	file, err := os.Open(filepath.Join(directory, "timeline.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("%w: open timeline: %v", replay.ErrBundleIncomplete, err)
	}
	defer file.Close()
	scan := bufio.NewScanner(file)
	scan.Buffer(make([]byte, 4096), 2*recording.MaxRuntimePayloadBytes)
	var events []recording.Event
	for scan.Scan() {
		var event recording.Event
		if err := json.Unmarshal(scan.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("%w: decode timeline: %v", replay.ErrBundleIncomplete, err)
		}
		if event.Sequence != uint64(len(events)+1) || event.ElapsedNS < 0 {
			return nil, fmt.Errorf("%w: timeline sequence or elapsed time is invalid", replay.ErrBundleIncomplete)
		}
		events = append(events, event)
	}
	if err := scan.Err(); err != nil {
		return nil, fmt.Errorf("%w: read timeline: %v", replay.ErrBundleIncomplete, err)
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("%w: timeline has no events", replay.ErrBundleIncomplete)
	}
	return events, nil
}

func timelineOrigin(events []recording.Event) (time.Time, error) {
	if len(events) == 0 || events[0].Timestamp == "" {
		return time.Time{}, fmt.Errorf("%w: timeline has no recording origin", replay.ErrBundleIncomplete)
	}
	origin, err := time.Parse(time.RFC3339Nano, events[0].Timestamp)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: invalid recording origin: %v", replay.ErrBundleIncomplete, err)
	}
	return origin, nil
}

func deriveEvidence(events []recording.Event, request replay.Request) (gwtesting.SessionCapture, *recordedToolExecutor, []int, int, int, error) {
	capture := gwtesting.SessionCapture{Version: gwtesting.SessionCaptureVersion, Session: gwtesting.SessionMetadata{FixtureProvenance: gwtesting.SessionFixtureProvenanceProviderRecorded}}
	var wires []gwtesting.CapturedSessionEvent
	var wireTypes []int
	tools := newRecordedToolExecutor()
	var firstSendType string
	var handshakeModel string
	var createdModel string
	var sawClosed bool
	var sawResponseDone bool
	for _, event := range events {
		if event.Kind != "runtime" {
			continue
		}
		switch event.RuntimeKind {
		case providerWireSend, providerWireReceive:
			// A provider read that returns EOF after a terminal response is the
			// normal close path for recordings without an explicit session.closed
			// frame. It is not a wire frame and must not become an empty replay
			// message. An empty failed read before a terminal response is partial
			// capture evidence and remains an error.
			if event.RuntimeKind == providerWireReceive && emptyFailedWireObservation(event) {
				if !sawResponseDone {
					return gwtesting.SessionCapture{}, nil, nil, 0, 0, fmt.Errorf("%w: provider wire receive failed before response completion", replay.ErrBundleIncomplete)
				}
				continue
			}
			if len(event.Payload) == 0 || (event.RuntimeKind == providerWireSend && !event.Clean) {
				return gwtesting.SessionCapture{}, nil, nil, 0, 0, fmt.Errorf("%w: provider wire %s has no successful payload", replay.ErrBundleIncomplete, event.RuntimeKind)
			}
			messageType, rawPayload, wireType, err := decodeWireEnvelope(event.Payload)
			if err != nil {
				return gwtesting.SessionCapture{}, nil, nil, 0, 0, fmt.Errorf("%w: provider wire %s: %v", replay.ErrBundleIncomplete, event.RuntimeKind, err)
			}
			if event.RuntimeKind == providerWireSend && firstSendType == "" {
				firstSendType = wireType
				if firstSendType == sessionUpdateType {
					handshakeModel = sessionUpdateModel(rawPayload)
				}
			}
			if event.RuntimeKind == providerWireReceive && wireType == "session.created" {
				model := sessionCreatedModel(rawPayload)
				if model != "" {
					if createdModel != "" && createdModel != model {
						return gwtesting.SessionCapture{}, nil, nil, 0, 0, fmt.Errorf("%w: captured session.created model changes from %q to %q", replay.ErrBundleMismatch, createdModel, model)
					}
					createdModel = model
				}
			}
			if wireType == sessionClosedType && event.RuntimeKind == providerWireReceive {
				sawClosed = true
			}
			if wireType == "response.done" && event.RuntimeKind == providerWireReceive {
				sawResponseDone = true
			}
			direction := gwtesting.DirectionServerToClient
			if event.RuntimeKind == providerWireSend {
				direction = gwtesting.DirectionClientToServer
			}
			ms, ok := elapsedMilliseconds(event.ElapsedNS)
			if !ok {
				return gwtesting.SessionCapture{}, nil, nil, 0, 0, fmt.Errorf("%w: provider wire timestamp overflows milliseconds", replay.ErrBundleIncomplete)
			}
			wireTypes = append(wireTypes, messageType)
			wires = append(wires, gwtesting.CapturedSessionEvent{Sequence: len(wires) + 1, Direction: direction, TimestampMs: ms, Type: wireType, PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage, Payload: append(json.RawMessage(nil), rawPayload...)})
		case toolCallKind:
			call, err := decodeToolCall(event.Payload)
			if err != nil {
				return gwtesting.SessionCapture{}, nil, nil, 0, 0, fmt.Errorf("%w: tool call: %v", replay.ErrBundleIncomplete, err)
			}
			if err := tools.addCall(call); err != nil {
				return gwtesting.SessionCapture{}, nil, nil, 0, 0, err
			}
		case toolResultKind:
			result, err := decodeToolResult(event.Payload)
			if err != nil {
				return gwtesting.SessionCapture{}, nil, nil, 0, 0, fmt.Errorf("%w: tool result: %v", replay.ErrBundleIncomplete, err)
			}
			if err := tools.addResult(result); err != nil {
				return gwtesting.SessionCapture{}, nil, nil, 0, 0, err
			}
		}
	}
	if len(wires) == 0 || firstSendType != sessionUpdateType {
		return gwtesting.SessionCapture{}, nil, nil, 0, 0, fmt.Errorf("%w: missing initial provider session.update handshake", replay.ErrBundleIncomplete)
	}
	verifiedModel := handshakeModel
	if verifiedModel == "" {
		verifiedModel = createdModel
	}
	if handshakeModel != "" && createdModel != "" && handshakeModel != createdModel {
		return gwtesting.SessionCapture{}, nil, nil, 0, 0, fmt.Errorf("%w: captured handshake model %q differs from session.created model %q", replay.ErrBundleMismatch, handshakeModel, createdModel)
	}
	if request.Model != "" {
		if verifiedModel == "" {
			return gwtesting.SessionCapture{}, nil, nil, 0, 0, fmt.Errorf("%w: requested model %q has no captured handshake model to verify", replay.ErrBundleIncomplete, request.Model)
		}
		if request.Model != verifiedModel {
			return gwtesting.SessionCapture{}, nil, nil, 0, 0, fmt.Errorf("%w: requested model %q differs from captured handshake model %q", replay.ErrBundleMismatch, request.Model, verifiedModel)
		}
	}
	// The headless runtime owns shutdown after a recorded terminal response.
	// Do not turn the absence of an explicit session.closed event into an EOF
	// immediately after the final response: the provider reader can otherwise
	// report a transport failure before the ordered MESSAGE.END reaches the
	// runtime, making replay timing dependent on goroutine scheduling. The
	// replay connection remains parked after the final record and is released
	// by the runtime's cancellation/Close path.
	// Preserve EOF for traces that actually end before a terminal response;
	// this keeps partial/failed transport evidence distinguishable from a
	// completed response followed by normal runtime shutdown.
	if !sawResponseDone && !sawClosed {
		capture.EndsWithDisconnect = true
	}
	if err := tools.validateShape(); err != nil {
		return gwtesting.SessionCapture{}, nil, nil, 0, 0, err
	}
	if request.Provider != "" {
		capture.Provider.Name = request.Provider
	}
	capture.Provider.Model = request.Model
	if capture.Provider.Model == "" {
		capture.Provider.Model = verifiedModel
	}
	capture.Records = wires
	sealed, err := gwtesting.SealSessionCapture(capture)
	if err != nil {
		return gwtesting.SessionCapture{}, nil, nil, 0, 0, fmt.Errorf("%w: seal derived capture: %v", replay.ErrBundleMismatch, err)
	}
	return sealed, tools, wireTypes, len(wires), len(tools.calls), nil
}

// emptyFailedWireObservation identifies the observation envelope emitted when
// a transport read fails without producing a frame. The envelope itself is
// non-empty (it carries message_type=0), so checking RuntimeEvent.Payload
// length alone would incorrectly feed it to decodeWireEnvelope and report a
// malformed provider message. A failed read after a recorded terminal
// response is the normal close path; before that boundary it is incomplete
// evidence and remains an error.
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

func sessionUpdateModel(payload []byte) string {
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

func sessionCreatedModel(payload []byte) string {
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

type recordedTool struct {
	call     messages.ToolCall
	response messages.ToolCallResponse
	failed   bool
	used     bool
}

type recordedToolExecutor struct {
	mu    sync.Mutex
	calls map[string]*recordedTool
}

type replayState struct {
	mu           sync.Mutex
	expected     int
	consumed     int
	dialed       bool
	err          error
	messageTypes []int
}

func (s *replayState) expectedTypeLocked() int {
	if s.consumed >= len(s.messageTypes) {
		return 0
	}
	return s.messageTypes[s.consumed]
}

func (s *replayState) validate() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if !s.dialed || s.consumed != s.expected {
		return fmt.Errorf("%w: provider wire consumed %d/%d", replay.ErrBundleIncomplete, s.consumed, s.expected)
	}
	return nil
}

type trackingDialer struct {
	inner transport.Dialer
	state *replayState
}

func (d *trackingDialer) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	d.state.mu.Lock()
	if d.state.dialed {
		err := fmt.Errorf("%w: replay bundle permits one session connection", replay.ErrBundleMismatch)
		d.state.err = err
		d.state.mu.Unlock()
		return nil, err
	}
	d.state.dialed = true
	d.state.mu.Unlock()
	conn, err := d.inner.Dial(endpoint, headers)
	if err != nil {
		d.state.mu.Lock()
		d.state.err = err
		d.state.mu.Unlock()
		return nil, err
	}
	return &trackingConn{inner: conn, state: d.state}, nil
}

type trackingConn struct {
	inner transport.Conn
	state *replayState
}

func (c *trackingConn) ReadMessage() (int, []byte, error) {
	messageType, payload, err := c.inner.ReadMessage()
	if err != nil {
		c.state.mu.Lock()
		if c.state.consumed < c.state.expected {
			c.state.err = fmt.Errorf("%w: %v", replay.ErrBundleIncomplete, err)
		}
		c.state.mu.Unlock()
		return messageType, payload, err
	}
	c.state.mu.Lock()
	if expected := c.state.expectedTypeLocked(); expected != 0 && messageType != expected {
		// The gateway replay connection historically reports text for every
		// received frame. Restore a recorded binary type at this seam while
		// still rejecting any other transport-type divergence.
		if expected == 2 && messageType == 1 {
			messageType = expected
		} else {
			err := fmt.Errorf("%w: received message type %d, expected %d", replay.ErrBundleMismatch, messageType, expected)
			c.state.err = err
			c.state.mu.Unlock()
			return messageType, payload, err
		}
	}
	c.state.consumed++
	c.state.mu.Unlock()
	return messageType, payload, nil
}

func (c *trackingConn) WriteMessage(messageType int, payload []byte) error {
	c.state.mu.Lock()
	expected := c.state.expectedTypeLocked()
	c.state.mu.Unlock()
	if expected != 0 && messageType != expected {
		err := fmt.Errorf("%w: sent message type %d, expected %d", replay.ErrBundleMismatch, messageType, expected)
		c.state.mu.Lock()
		c.state.err = err
		c.state.mu.Unlock()
		return err
	}
	if err := c.inner.WriteMessage(messageType, payload); err != nil {
		wrapped := fmt.Errorf("%w: %v", replay.ErrBundleMismatch, err)
		c.state.mu.Lock()
		c.state.err = wrapped
		c.state.mu.Unlock()
		return wrapped
	}
	c.state.mu.Lock()
	c.state.consumed++
	c.state.mu.Unlock()
	return nil
}

func (c *trackingConn) Close() error {
	err := c.inner.Close()
	if err != nil {
		c.state.mu.Lock()
		c.state.err = err
		c.state.mu.Unlock()
	}
	return err
}

func newRecordedToolExecutor() *recordedToolExecutor {
	return &recordedToolExecutor{calls: make(map[string]*recordedTool)}
}

func (e *recordedToolExecutor) addCall(call messages.ToolCall) error {
	if call.ID == "" || call.Name == "" || !json.Valid([]byte(call.Arguments)) {
		return fmt.Errorf("%w: invalid call %q", replay.ErrBundleIncomplete, call.ID)
	}
	if _, exists := e.calls[call.ID]; exists {
		return fmt.Errorf("%w: duplicate tool call %q", replay.ErrBundleMismatch, call.ID)
	}
	e.calls[call.ID] = &recordedTool{call: call}
	return nil
}

func (e *recordedToolExecutor) addResult(result decodedToolResult) error {
	entry, exists := e.calls[result.callID]
	if !exists {
		return fmt.Errorf("%w: result for unknown call %q", replay.ErrBundleMismatch, result.callID)
	}
	if entry.response.ToolCallID != "" || entry.failed {
		return fmt.Errorf("%w: duplicate tool result %q", replay.ErrBundleMismatch, result.callID)
	}
	if result.name != entry.call.Name {
		return fmt.Errorf("%w: call %q name %q != %q", replay.ErrBundleMismatch, result.callID, result.name, entry.call.Name)
	}
	result.response.ToolCallID = result.callID
	if result.response.Name == "" {
		result.response.Name = result.name
	}
	entry.response, entry.failed = result.response, result.failed
	return nil
}

func (e *recordedToolExecutor) validateShape() error {
	for id, entry := range e.calls {
		if entry.response.ToolCallID == "" && !entry.failed {
			return fmt.Errorf("%w: tool call %q has no result", replay.ErrBundleIncomplete, id)
		}
	}
	return nil
}

func (e *recordedToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	if err := contextError(ctx); err != nil {
		return messages.ToolCallResponse{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	entry, ok := e.calls[call.ID]
	if !ok || entry.call.Name != call.Name || !sameJSON(entry.call.Arguments, call.Arguments) {
		return messages.ToolCallResponse{}, fmt.Errorf("%w: call id=%q name=%q", replay.ErrToolMismatch, call.ID, call.Name)
	}
	if entry.used {
		return messages.ToolCallResponse{}, fmt.Errorf("%w: duplicate call %q", replay.ErrToolMismatch, call.ID)
	}
	entry.used = true
	if entry.failed {
		return entry.response, fmt.Errorf("%w: call %q", replay.ErrToolFailure, call.ID)
	}
	return entry.response, nil
}

func (e *recordedToolExecutor) validateComplete() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, entry := range e.calls {
		if !entry.used {
			return fmt.Errorf("%w: tool call %q was not consumed", replay.ErrBundleIncomplete, id)
		}
	}
	return nil
}

func sameJSON(expected, actual string) bool {
	var left, right any
	return json.Unmarshal([]byte(expected), &left) == nil && json.Unmarshal([]byte(actual), &right) == nil && jsonEqual(left, right)
}

func jsonEqual(left, right any) bool {
	return bytes.Equal(mustJSON(left), mustJSON(right))
}

func mustJSON(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}

type decodedToolResult struct {
	callID, name string
	response     messages.ToolCallResponse
	failed       bool
}

func decodeToolCall(payload []byte) (messages.ToolCall, error) {
	var raw struct {
		ID             string `json:"ID"`
		Name           string `json:"Name"`
		Arguments      string `json:"Arguments"`
		LowerID        string `json:"id"`
		LowerName      string `json:"name"`
		LowerArguments string `json:"arguments"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return messages.ToolCall{}, err
	}
	if raw.ID == "" {
		raw.ID = raw.LowerID
	}
	if raw.Name == "" {
		raw.Name = raw.LowerName
	}
	if raw.Arguments == "" {
		raw.Arguments = raw.LowerArguments
	}
	return messages.ToolCall{ID: raw.ID, Name: raw.Name, Arguments: raw.Arguments}, nil
}

func decodeToolResult(payload []byte) (decodedToolResult, error) {
	var raw struct {
		CallID   string `json:"call_id"`
		Name     string `json:"name"`
		Failed   bool   `json:"failed"`
		Response struct {
			ToolCallID      string `json:"ToolCallID"`
			LowerToolCallID string `json:"tool_call_id"`
			Name            string `json:"Name"`
			LowerName       string `json:"name"`
			Content         string `json:"Content"`
			LowerContent    string `json:"content"`
		} `json:"response"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return decodedToolResult{}, err
	}
	if raw.CallID == "" {
		return decodedToolResult{}, errors.New("call_id is empty")
	}
	if raw.Response.ToolCallID == "" {
		raw.Response.ToolCallID = raw.Response.LowerToolCallID
	}
	if raw.Response.Name == "" {
		raw.Response.Name = raw.Response.LowerName
	}
	if raw.Response.Content == "" {
		raw.Response.Content = raw.Response.LowerContent
	}
	return decodedToolResult{callID: raw.CallID, name: raw.Name, failed: raw.Failed, response: messages.ToolCallResponse{ToolCallID: raw.Response.ToolCallID, Name: raw.Response.Name, Content: raw.Response.Content}}, nil
}

var _ messages.ToolExecutor = (*recordedToolExecutor)(nil)
