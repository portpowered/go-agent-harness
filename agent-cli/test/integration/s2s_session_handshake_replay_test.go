package integration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceTools "github.com/portpowered/go-agent-harness/agent-cli/internal/services/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const (
	sessionHandshakeReplayPrompt       = "recorded prompt"
	sessionHandshakeReplayInstructions = "recorded instructions"
	sessionHandshakeReplayTool         = "recorded_tool"
	sessionHandshakeReplayResponse     = "recorded response"
)

// TestSessionCommand_RecordThenReplayUsesCapturedHandshake records through
// the composed production CLI and then sends the resulting capture through the
// shipped production CLI. The replay deliberately changes the current
// instruction/tool inputs and omits provider credentials; the captured raw
// session.update remains authoritative while the later outbound prompt stays
// under strict replay validation.
func TestSessionCommand_RecordThenReplayUsesCapturedHandshake(t *testing.T) {
	recordPath := filepath.Join(t.TempDir(), "recorded.session.json")
	recordDialer := newHandshakeReplayDialer()

	recordTools := serviceTools.Factory(func(*config.Config) (serviceTools.Capabilities, error) {
		return serviceTools.Capabilities{Definitions: []messages.ToolDefinition{{
			Name:        sessionHandshakeReplayTool,
			Description: "recorded schema",
			Parameters: []messages.ToolParameter{{
				Name:     "value",
				Type:     "string",
				Required: true,
			}},
		}}}, nil
	})
	recordCLI, err := wire.InitializeMockAgentCLIWithPorts(
		wire.NewPortSwap(wire.PortTransportDialer, recordDialer),
		wire.NewToolServicePort(recordTools),
		wire.NewPortSwap(wire.PortInferencer, &mockInferencer{response: "unused"}),
	)
	if err != nil {
		t.Fatalf("initialize recording agent CLI: %v", err)
	}
	recordRoot := recordCLI.Generate()
	recordRoot.SetOut(io.Discard)
	recordRoot.SetErr(io.Discard)
	recordRoot.SetArgs([]string{
		"--config-dir", t.TempDir(),
		"session",
		"--record", recordPath,
		"--provider", "openai", "--model", "gpt-realtime", "--api-key", "synthetic-recording-key",
		"--system-prompt", sessionHandshakeReplayInstructions,
		sessionHandshakeReplayPrompt,
	})
	if err := recordRoot.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("record hermetic OpenAI session: %v", err)
	}

	capture, err := gatewaytesting.LoadSessionCapture(recordPath)
	if err != nil {
		t.Fatalf("load recorded capture: %v", err)
	}
	initial := firstSessionUpdateRecord(t, capture)
	if !strings.Contains(string(initial.Payload), sessionHandshakeReplayInstructions) {
		t.Fatalf("recorded initial session.update omitted instructions: %s", initial.Payload)
	}
	if !strings.Contains(string(initial.Payload), sessionHandshakeReplayTool) {
		t.Fatalf("recorded initial session.update omitted tool schema: %s", initial.Payload)
	}

	// Disable every currently configured tool and provide different live
	// instructions. Pure replay must not resolve either value into a new
	// provider handshake, and this config intentionally contains no API key.
	replayConfigDir := t.TempDir()
	writeSessionToolConfig(t, replayConfigDir, false)
	agentCLI, err := wire.InitializeAgentCLI()
	if err != nil {
		t.Fatalf("initialize production agent CLI: %v", err)
	}
	writer := NewTestWriter()
	root := agentCLI.Generate()
	root.SetOut(writer.Stdout())
	root.SetErr(writer.Stderr())
	root.SetArgs([]string{
		"--config-dir", replayConfigDir,
		"session",
		"--replay", recordPath,
		"--system-prompt", "current instructions must not replace the capture",
		sessionHandshakeReplayPrompt,
	})

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("replay recorded session through production CLI: %v\nstderr=%s", err, writer.StderrString())
	}
	if got := writer.StdoutString(); !strings.Contains(got, sessionHandshakeReplayResponse) {
		t.Fatalf("replay output missing %q: %q", sessionHandshakeReplayResponse, got)
	}
}

func firstSessionUpdateRecord(t *testing.T, capture gatewaytesting.SessionCapture) gatewaytesting.CapturedSessionEvent {
	t.Helper()
	for _, record := range capture.Records {
		if record.Direction == gatewaytesting.DirectionClientToServer && record.Type == rtEventSessionUpdate {
			return record
		}
	}
	t.Fatalf("capture has no outbound session.update: %#v", capture.Records)
	return gatewaytesting.CapturedSessionEvent{}
}

type handshakeReplayDialer struct {
	conn *handshakeReplayConn
}

func newHandshakeReplayDialer() *handshakeReplayDialer {
	return &handshakeReplayDialer{conn: &handshakeReplayConn{
		inbound: make(chan []byte, 16),
		closed:  make(chan struct{}),
	}}
}

func (d *handshakeReplayDialer) Dial(string, map[string]string) (transport.Conn, error) {
	return d.conn, nil
}

type handshakeReplayConn struct {
	inbound   chan []byte
	closed    chan struct{}
	closeOnce sync.Once
}

func (c *handshakeReplayConn) ReadMessage() (int, []byte, error) {
	select {
	case payload := <-c.inbound:
		return 1, payload, nil
	case <-c.closed:
		return 0, nil, io.EOF
	}
}

func (c *handshakeReplayConn) WriteMessage(_ int, payload []byte) error {
	select {
	case <-c.closed:
		return errors.New("handshake replay connection is closed")
	default:
	}

	var event struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return err
	}
	switch event.Type {
	case rtEventSessionUpdate:
		c.enqueue(`{"type":"session.created","session":{"id":"sess-record-replay","model":"gpt-realtime"}}`)
	case rtEventResponseCreate:
		c.enqueue(`{"type":"response.created","response":{"id":"resp-record-replay"}}`)
		c.enqueue(`{"type":"response.output_text.delta","delta":"recorded response"}`)
		c.enqueue(`{"type":"response.output_text.done"}`)
		c.enqueue(`{"type":"response.done","response":{"id":"resp-record-replay","status":"completed"}}`)
	}
	return nil
}

func (c *handshakeReplayConn) enqueue(payload string) {
	select {
	case c.inbound <- []byte(payload):
	case <-c.closed:
	}
}

func (c *handshakeReplayConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

var _ transport.Dialer = (*handshakeReplayDialer)(nil)
var _ transport.Conn = (*handshakeReplayConn)(nil)

type integrationScriptedSessionInferencer struct {
	events    []messages.StreamMessage
	connected bool
	sentText  chan string
}

func (s *integrationScriptedSessionInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	s.connected = true
	if s.sentText == nil {
		s.sentText = make(chan string, 8)
	}
	session := newIntegrationScriptedSession(s.sentText)
	go func() {
		session.recv.Write(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeSessionOpen,
			Value: messages.NewSessionOpenValue("integration-session", "openai"),
		})
		time.Sleep(150 * time.Millisecond)
		for _, evt := range s.events {
			session.recv.Write(ctx, evt)
		}
	}()
	return session, nil
}

func assertIntegrationSessionReceivedText(t *testing.T, sessionInf *integrationScriptedSessionInferencer, want string) {
	t.Helper()

	select {
	case got := <-sessionInf.sentText:
		if got != want {
			t.Fatalf("session received prompt = %q, want %q", got, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("session did not receive prompt %q within 10s", want)
	}
}

type integrationScriptedSession struct {
	recv     *messages.TypedBuffer[messages.StreamMessage]
	done     chan struct{}
	once     sync.Once
	sentText chan string
}

func newIntegrationScriptedSession(sentText chan string) *integrationScriptedSession {
	return &integrationScriptedSession{
		recv:     messages.NewTypedBuffer[messages.StreamMessage](32),
		done:     make(chan struct{}),
		sentText: sentText,
	}
}

func (s *integrationScriptedSession) Send(_ context.Context, msg messages.StreamMessage) bool {
	if v, ok := msg.Value.(*messages.TextDeltaValue); ok && v != nil {
		select {
		case s.sentText <- v.Content:
		default:
		}
	}
	return true
}

func (s *integrationScriptedSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recv
}

func (s *integrationScriptedSession) Done() <-chan struct{} {
	return s.done
}

func (s *integrationScriptedSession) Close() error {
	s.once.Do(func() {
		close(s.done)
	})
	return nil
}
