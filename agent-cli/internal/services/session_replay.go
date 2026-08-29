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
	sessionClosedEventType = "session.closed"
	sessionUpdateEventType = "session.update"
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

		payload := record.Payload
		if len(payload) == 0 {
			payload = record.Data
		}
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
			return drainSessionReplayMessages(out, replayer)
		case msg, ok := <-replayer.Receive().Chan():
			if !ok {
				continue
			}
			if err := writeSessionReplayMessage(out, msg); err != nil {
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
