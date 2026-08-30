// This file contains session capture replay execution and inspection helpers used to select and validate replay behavior.
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const (
	sessionClosedEventType          = "session.closed"
	sessionUpdateEventType          = "session.update"
	conversationItemCreateEventType = "conversation.item.create"
	responseCreateEventType         = "response.create"
)

// replaySessionConfiguration is the provider-facing configuration captured in
// the first outbound session.update. The raw payload is deliberately retained
// instead of projected into the live workspace's SessionConfig: provider wire
// versions may carry fields (for example GA audio or output_modalities) that
// the shared config does not model yet.
type replaySessionConfiguration struct {
	payload []byte
	model   string
}

// loadReplaySessionConfiguration validates and extracts the authoritative
// initial provider handshake before replay starts. A replay can therefore
// fail with a capture-specific setup error rather than opening a session and
// later reporting a misleading outbound mismatch.
func loadReplaySessionConfiguration(path string) (replaySessionConfiguration, error) {
	capture, err := gwtesting.LoadSessionCapture(path)
	if err != nil {
		return replaySessionConfiguration{}, fmt.Errorf("load replay session capture %s: %w", path, err)
	}

	for _, record := range capture.Records {
		if record.Direction != gwtesting.DirectionClientToServer || record.Type != sessionUpdateEventType {
			continue
		}

		payload := replayCaptureRecordPayload(record)
		if len(payload) == 0 {
			return replaySessionConfiguration{}, fmt.Errorf(
				"replay session capture %s: initial outbound %s at sequence %d has no payload",
				path, sessionUpdateEventType, record.Sequence,
			)
		}

		var envelope struct {
			Type    string          `json:"type"`
			Session json.RawMessage `json:"session"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return replaySessionConfiguration{}, fmt.Errorf(
				"replay session capture %s: decode initial outbound %s at sequence %d: %w",
				path, sessionUpdateEventType, record.Sequence, err,
			)
		}
		if envelope.Type != sessionUpdateEventType {
			return replaySessionConfiguration{}, fmt.Errorf(
				"replay session capture %s: initial outbound %s at sequence %d has payload type %q",
				path, sessionUpdateEventType, record.Sequence, envelope.Type,
			)
		}
		if len(envelope.Session) == 0 || string(envelope.Session) == "null" {
			return replaySessionConfiguration{}, fmt.Errorf(
				"replay session capture %s: initial outbound %s at sequence %d is missing the session configuration",
				path, sessionUpdateEventType, record.Sequence,
			)
		}

		var session map[string]json.RawMessage
		if err := json.Unmarshal(envelope.Session, &session); err != nil {
			return replaySessionConfiguration{}, fmt.Errorf(
				"replay session capture %s: decode session configuration at sequence %d: %w",
				path, record.Sequence, err,
			)
		}
		if len(session) == 0 {
			return replaySessionConfiguration{}, fmt.Errorf(
				"replay session capture %s: initial outbound %s at sequence %d has an empty session configuration",
				path, sessionUpdateEventType, record.Sequence,
			)
		}

		model := ""
		if rawModel, ok := session["model"]; ok {
			if err := json.Unmarshal(rawModel, &model); err != nil {
				return replaySessionConfiguration{}, fmt.Errorf(
					"replay session capture %s: session.model at sequence %d must be a string: %w",
					path, record.Sequence, err,
				)
			}
			model = strings.TrimSpace(model)
		}

		return replaySessionConfiguration{
			payload: append([]byte(nil), payload...),
			model:   model,
		}, nil
	}

	return replaySessionConfiguration{}, fmt.Errorf(
		"replay session capture %s: missing initial outbound %s configuration",
		path, sessionUpdateEventType,
	)
}

// replayCapturedPrompt is the one prompt shape that the ordinary OpenAI
// session loop can reproduce without inventing a wire event: a single user
// input_text item followed by one response.create. Keeping this as an
// explicit, narrow shape preserves the established behavior for audio,
// image, tool, and multi-turn captures.
type replayCapturedPrompt struct {
	text string
}

// loadReplaySessionPrompt finds the initial text turn in a raw capture. It is
// intentionally called only for a bare replay. Explicit prompts must continue
// to reach the strict replay dialer so changed, missing, or extra outbound
// frames retain their existing mismatch errors.
func loadReplaySessionPrompt(path string) (*replayCapturedPrompt, error) {
	capture, err := gwtesting.LoadSessionCapture(path)
	if err != nil {
		return nil, fmt.Errorf("load replay session capture %s: %w", path, err)
	}

	clientActions := make([]gwtesting.CapturedSessionEvent, 0, len(capture.Records))
	for _, record := range capture.Records {
		if record.Direction != gwtesting.DirectionClientToServer || record.Type == sessionUpdateEventType {
			continue
		}
		clientActions = append(clientActions, record)
	}

	var prompt *replayCapturedPrompt
	promptPosition := -1
	for position, record := range clientActions {
		if record.Type != conversationItemCreateEventType {
			continue
		}
		text, isTextPrompt, err := parseReplayTextPrompt(path, record)
		if err != nil {
			return nil, err
		}
		if !isTextPrompt {
			continue
		}
		if prompt != nil {
			return nil, fmt.Errorf(
				"replay session capture %s has ambiguous recorded text prompts at sequences %d and %d",
				path, clientActions[promptPosition].Sequence, record.Sequence,
			)
		}
		prompt = &replayCapturedPrompt{text: text}
		promptPosition = position
	}
	if prompt == nil {
		return nil, nil
	}
	if promptPosition != 0 {
		return nil, fmt.Errorf(
			"replay session capture %s has an ambiguous recorded prompt at sequence %d: it must be the first client action after session.update",
			path, clientActions[promptPosition].Sequence,
		)
	}
	if promptPosition+1 >= len(clientActions) || clientActions[promptPosition+1].Type != responseCreateEventType {
		return nil, fmt.Errorf(
			"replay session capture %s has an incomplete recorded prompt at sequence %d: expected the next client action to be %s",
			path, clientActions[promptPosition].Sequence, responseCreateEventType,
		)
	}
	if promptPosition+2 != len(clientActions) {
		return nil, fmt.Errorf(
			"replay session capture %s has an ambiguous recorded prompt at sequence %d: expected only %s after it",
			path, clientActions[promptPosition].Sequence, responseCreateEventType,
		)
	}
	if err := validateReplayEventPayload(path, clientActions[promptPosition+1], responseCreateEventType); err != nil {
		return nil, err
	}
	return prompt, nil
}

func parseReplayTextPrompt(path string, record gwtesting.CapturedSessionEvent) (string, bool, error) {
	payload := replayCaptureRecordPayload(record)
	if len(payload) == 0 {
		return "", false, fmt.Errorf(
			"replay session capture %s: recorded %s at sequence %d has no payload",
			path, conversationItemCreateEventType, record.Sequence,
		)
	}

	var envelope struct {
		Type string          `json:"type"`
		Item json.RawMessage `json:"item"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return "", false, fmt.Errorf(
			"replay session capture %s: decode recorded %s at sequence %d: %w",
			path, conversationItemCreateEventType, record.Sequence, err,
		)
	}
	if envelope.Type != conversationItemCreateEventType {
		return "", false, fmt.Errorf(
			"replay session capture %s: recorded %s at sequence %d has payload type %q",
			path, conversationItemCreateEventType, record.Sequence, envelope.Type,
		)
	}
	if len(envelope.Item) == 0 || string(envelope.Item) == "null" {
		return "", false, fmt.Errorf(
			"replay session capture %s: recorded %s at sequence %d is missing its item",
			path, conversationItemCreateEventType, record.Sequence,
		)
	}

	var item struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string  `json:"type"`
			Text *string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(envelope.Item, &item); err != nil {
		return "", false, fmt.Errorf(
			"replay session capture %s: decode recorded conversation item at sequence %d: %w",
			path, record.Sequence, err,
		)
	}
	// Function-call outputs and other user content are valid session actions,
	// but they are not reproducible from the plain text prompt loop.
	if item.Type != "message" || item.Role != "user" {
		return "", false, nil
	}
	if len(item.Content) == 0 {
		return "", false, fmt.Errorf(
			"replay session capture %s: recorded user message at sequence %d has no content",
			path, record.Sequence,
		)
	}
	for _, part := range item.Content {
		if part.Type != "input_text" {
			return "", false, nil
		}
	}
	if len(item.Content) != 1 || item.Content[0].Text == nil {
		return "", false, fmt.Errorf(
			"replay session capture %s: recorded user message at sequence %d must contain exactly one input_text part with text",
			path, record.Sequence,
		)
	}
	return *item.Content[0].Text, true, nil
}

func validateReplayEventPayload(path string, record gwtesting.CapturedSessionEvent, eventType string) error {
	payload := replayCaptureRecordPayload(record)
	if len(payload) == 0 {
		return fmt.Errorf(
			"replay session capture %s: recorded %s at sequence %d has no payload",
			path, eventType, record.Sequence,
		)
	}
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return fmt.Errorf(
			"replay session capture %s: decode recorded %s at sequence %d: %w",
			path, eventType, record.Sequence, err,
		)
	}
	if envelope.Type != eventType {
		return fmt.Errorf(
			"replay session capture %s: recorded %s at sequence %d has payload type %q",
			path, eventType, record.Sequence, envelope.Type,
		)
	}
	return nil
}

func replayCaptureRecordPayload(record gwtesting.CapturedSessionEvent) []byte {
	if len(record.Payload) > 0 {
		return record.Payload
	}
	return record.Data
}

// replayInitialSessionUpdateDialer lets the provider keep its normal session
// implementation while replacing only the first generated handshake with the
// capture's raw configuration. The wrapped replay dialer still strictly
// validates that replacement and every later outbound frame.
type replayInitialSessionUpdateDialer struct {
	inner   transport.Dialer
	payload []byte
}

var _ transport.Dialer = (*replayInitialSessionUpdateDialer)(nil)

func newReplayInitialSessionUpdateDialer(inner transport.Dialer, configuration replaySessionConfiguration) transport.Dialer {
	return &replayInitialSessionUpdateDialer{
		inner:   inner,
		payload: append([]byte(nil), configuration.payload...),
	}
}

func (d *replayInitialSessionUpdateDialer) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	if d == nil || d.inner == nil {
		return nil, fmt.Errorf("replay initial session update dialer requires an inner dialer")
	}
	conn, err := d.inner.Dial(endpoint, headers)
	if err != nil {
		return nil, err
	}
	return &replayInitialSessionUpdateConn{
		inner:   conn,
		payload: append([]byte(nil), d.payload...),
	}, nil
}

type replayInitialSessionUpdateConn struct {
	inner       transport.Conn
	payload     []byte
	writeMu     sync.Mutex
	handshakeOn bool
}

var _ transport.Conn = (*replayInitialSessionUpdateConn)(nil)

func (c *replayInitialSessionUpdateConn) ReadMessage() (int, []byte, error) {
	return c.inner.ReadMessage()
}

func (c *replayInitialSessionUpdateConn) WriteMessage(messageType int, payload []byte) error {
	// ConnectSession sends the provider-specific initial session.update before
	// starting any provider write loop. Serializing writes here also preserves
	// that guarantee for custom test transports.
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if !c.handshakeOn {
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return fmt.Errorf("replay provider initial event is not valid JSON: %w", err)
		}
		if envelope.Type != sessionUpdateEventType {
			return fmt.Errorf("replay provider expected initial %s, got %q", sessionUpdateEventType, envelope.Type)
		}
		payload = append([]byte(nil), c.payload...)
		c.handshakeOn = true
	}
	return c.inner.WriteMessage(messageType, payload)
}

func (c *replayInitialSessionUpdateConn) Close() error {
	return c.inner.Close()
}

func replaySessionCapture(ctx context.Context, out io.Writer, path string) error {
	renderer := newSessionReplayRenderer(out)
	replayer, err := gwtesting.NewSessionReplayer(path, gwtesting.WithReplayOutboundValidation(false), gwtesting.WithReplayContext(ctx))
	if err != nil {
		return fmt.Errorf("replay session capture %s: %w", path, err)
	}
	for {
		select {
		case <-ctx.Done():
			_ = replayer.Close()
			return ctx.Err()
		case <-replayer.Done():
			return drainSessionReplayMessages(renderer, replayer)
		case msg, ok := <-replayer.Receive().Chan():
			if !ok {
				continue
			}
			if err := writeSessionReplayMessage(renderer, msg); err != nil {
				return err
			}
		}
	}
}

func drainSessionReplayMessages(out io.Writer, replayer *gwtesting.SessionReplayer) error {
	for {
		msg, ok := replayer.Receive().Read()
		if !ok {
			return nil
		}
		if err := writeSessionReplayMessage(out, msg); err != nil {
			return err
		}
	}
}

func grokReplayCaptureHasSessionClose(path string) bool {
	capture, err := gwtesting.LoadSessionCapture(path)
	if err != nil {
		return false
	}
	for _, record := range capture.Records {
		if record.Direction == gwtesting.DirectionServerToClient && record.Type == "session.closed" {
			return true
		}
	}
	return false
}

func usesWebSocketCapture(path string) bool {
	capture, err := gwtesting.LoadSessionCapture(path)
	if err != nil {
		return false
	}
	for _, record := range capture.Records {
		if record.PayloadType == gwtesting.SessionPayloadTypeWebSocketMessage {
			return true
		}
	}
	return false
}

func usesOpenAIWebSocketCapture(path string) bool {
	capture, err := gwtesting.LoadSessionCapture(path)
	if err != nil {
		return false
	}
	return strings.EqualFold(capture.Provider.Name, sessionProviderOpenAI)
}

func captureHasEvent(path string, eventType string) bool {
	capture, err := gwtesting.LoadSessionCapture(path)
	if err != nil {
		return false
	}
	for _, record := range capture.Records {
		if record.Type == eventType {
			return true
		}
	}
	return false
}
