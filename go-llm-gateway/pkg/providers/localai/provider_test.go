package localai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/capabilities"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

func TestProviderIdentityCapabilitiesAndGatewayContract(t *testing.T) {
	conn := newTestConn(map[string]any{
		"type": "session.created", "session": map[string]any{"id": "sess-localai", "model": WireModel},
	})
	dialer := &testDialer{conn: conn}
	p := New(WithBaseURL("ws://localai.test/v1/realtime"), WithWebSocketDialer(dialer))

	if p.Name() != ProviderName || p.Model() != ModelID {
		t.Fatalf("identity = (%q, %q), want (%q, %q)", p.Name(), p.Model(), ProviderName, ModelID)
	}
	caps := p.Capabilities()
	if caps.Provider != ProviderName || caps.Metadata["wireModel"] != WireModel || caps.Metadata["credentialRequired"] != "false" {
		t.Fatalf("LocalAI metadata = %+v", caps)
	}
	for name, feature := range map[string]capabilities.FeatureCapability{
		"sessions": caps.Session.Sessions, "audio input": caps.Session.AudioInput, "audio output": caps.Session.AudioOutput,
	} {
		if feature.State != capabilities.CapabilityStateSupported {
			t.Errorf("%s state = %q, want supported", name, feature.State)
		}
	}

	sessionGateway, err := gateway.NewSessionGateway(gateway.WithSessionProvider(p))
	if err != nil {
		t.Fatalf("NewSessionGateway: %v", err)
	}
	session, err := sessionGateway.ConnectSession(context.Background(), models.SessionConfig{
		Model: ModelID, Modalities: []models.SessionModality{models.SessionModalityAudio},
		InputAudioFormat: models.AudioFormatPCM16, OutputAudioFormat: models.AudioFormatPCM16,
	})
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	if dialer.url != "ws://localai.test/v1/realtime?model=gpt-realtime" {
		t.Fatalf("dial URL = %q", dialer.url)
	}
	if len(dialer.headers) != 0 {
		t.Fatalf("LocalAI dial headers = %v, want no credentials", dialer.headers)
	}
	first := conn.clientMessages[0]
	var event map[string]json.RawMessage
	if err := json.Unmarshal(first, &event); err != nil {
		t.Fatalf("session update JSON: %v", err)
	}
	var payload struct {
		Session struct {
			Model string `json:"model"`
		} `json:"session"`
	}
	if err := json.Unmarshal(first, &payload); err != nil || payload.Session.Model != WireModel {
		t.Fatalf("session update model = %q, want %q", payload.Session.Model, WireModel)
	}
	if event["type"] == nil {
		t.Fatal("session update omitted event type")
	}
}

func TestResolveBaseURLPrecedence(t *testing.T) {
	tests := []struct {
		name, invocation, configuration, environment, want string
	}{
		{name: "default", want: DefaultEndpoint},
		{name: "environment", environment: "ws://env.test/realtime?keep=env", want: "ws://env.test/realtime?keep=env&model=gpt-realtime"},
		{name: "configuration wins over environment", configuration: "ws://config.test/realtime", environment: "ws://env.test/realtime", want: "ws://config.test/realtime?model=gpt-realtime"},
		{name: "invocation wins over configuration and environment", invocation: "ws://invoke.test/realtime?keep=1", configuration: "ws://config.test/realtime", environment: "ws://env.test/realtime", want: "ws://invoke.test/realtime?keep=1&model=gpt-realtime"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dialer := &testDialer{err: errors.New("closed for precedence test")}
			p := New(WithBaseURL(tt.invocation), WithConfigBaseURL(tt.configuration), WithEnvironmentBaseURL(tt.environment), WithWebSocketDialer(dialer))
			_, err := p.ConnectSession(context.Background(), models.SessionConfig{Model: ModelID})
			if err == nil || dialer.url != tt.want {
				t.Fatalf("error = %v, dial URL = %q, want %q", err, dialer.url, tt.want)
			}
		})
	}
}

func TestConnectionErrorIsTypedFastAndSecretFree(t *testing.T) {
	secret := "localai-test-secret-should-never-escape"
	t.Setenv("AGENT_MODEL__LOCALAI__API_KEY", secret)
	endpoint := "ws://127.0.0.1:1/v1/realtime"
	dialer := &testDialer{err: errors.New("connection refused")}
	logger := &testLogger{}
	p := New(WithEnvironmentBaseURL(endpoint), WithWebSocketDialer(dialer), WithLogger(logger))

	started := time.Now()
	_, err := p.ConnectSession(context.Background(), models.SessionConfig{Model: ModelID})
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatalf("closed endpoint took %s", elapsed)
	}
	var connectionErr *ConnectionError
	if !errors.As(err, &connectionErr) {
		t.Fatalf("error = %T %v, want *ConnectionError", err, err)
	}
	if !errors.Is(err, ErrConnection) || !errors.Is(err, providers.ErrTransport) || !strings.Contains(err.Error(), endpoint) || !strings.Contains(err.Error(), "start LocalAI") {
		t.Fatalf("connection error = %v", err)
	}
	assertErrorChainDoesNotContain(t, err, secret)
	for _, field := range logger.fields {
		if strings.Contains(field, secret) {
			t.Fatalf("logger leaked secret: %q", field)
		}
	}
	if len(dialer.headers) != 0 {
		t.Fatalf("dialer received credentials: %v", dialer.headers)
	}
}

func TestRealtimeAudioIsDecodedBySharedAdapter(t *testing.T) {
	audio := []byte{0x10, 0x00, 0x20, 0x00}
	conn := newTestConn(
		map[string]any{"type": "session.created", "session_id": "sess-audio", "model": WireModel},
		map[string]any{"type": "response.output_audio.delta", "delta": base64.StdEncoding.EncodeToString(audio), "format": "pcm16"},
		map[string]any{"type": "response.output_audio.done"},
	)
	p := New(WithBaseURL("ws://audio.test/realtime"), WithWebSocketDialer(&testDialer{conn: conn}))
	session, err := p.ConnectSession(context.Background(), models.SessionConfig{Model: ModelID})
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for {
		msg, ok := session.Receive().ReadBlockingContext(ctx)
		if !ok {
			t.Fatal("timed out waiting for decoded audio")
		}
		if msg.Type != messages.StreamTypeAudioDelta {
			continue
		}
		got, ok := msg.Value.(*messages.AudioDeltaValue)
		if !ok || string(got.Content) != string(audio) || got.MediaType != "audio/pcm" {
			t.Fatalf("decoded audio = %#v, want bytes and audio/pcm", msg.Value)
		}
		return
	}
}

func TestUnsupportedModelDoesNotDial(t *testing.T) {
	dialer := &testDialer{err: errors.New("dial should not run")}
	_, err := New(WithWebSocketDialer(dialer)).ConnectSession(context.Background(), models.SessionConfig{Model: "localai/other"})
	if err == nil || !errors.Is(err, providers.ErrUnsupportedRequest) || dialer.calls != 0 {
		t.Fatalf("error = %v, calls = %d", err, dialer.calls)
	}
}

func assertErrorChainDoesNotContain(t *testing.T, err error, secret string) {
	t.Helper()
	seen := map[error]bool{}
	var visit func(error)
	visit = func(current error) {
		if current == nil || seen[current] {
			return
		}
		seen[current] = true
		if strings.Contains(current.Error(), secret) {
			t.Fatalf("secret leaked from %T: %q", current, current.Error())
		}
		if joined, ok := current.(interface{ Unwrap() []error }); ok {
			for _, cause := range joined.Unwrap() {
				visit(cause)
			}
		}
		if single, ok := current.(interface{ Unwrap() error }); ok {
			visit(single.Unwrap())
		}
	}
	visit(err)
}

type testDialer struct {
	conn    *testConn
	err     error
	url     string
	headers map[string]string
	calls   int
}

func (d *testDialer) Dial(endpoint string, headers map[string]string) (WebSocketConn, error) {
	d.calls++
	d.url = endpoint
	d.headers = headers
	if d.err != nil {
		return nil, d.err
	}
	return d.conn, nil
}

type testConn struct {
	mu             sync.Mutex
	serverMessages [][]byte
	clientMessages [][]byte
	closed         bool
	readBlock      chan struct{}
	closeOnce      sync.Once
}

func newTestConn(events ...map[string]any) *testConn {
	c := &testConn{readBlock: make(chan struct{})}
	for _, event := range events {
		data, _ := json.Marshal(event)
		c.serverMessages = append(c.serverMessages, data)
	}
	return c
}

func (c *testConn) ReadMessage() (int, []byte, error) {
	for {
		c.mu.Lock()
		if len(c.serverMessages) > 0 {
			data := c.serverMessages[0]
			c.serverMessages = c.serverMessages[1:]
			c.mu.Unlock()
			return 1, data, nil
		}
		if c.closed {
			c.mu.Unlock()
			return 0, nil, io.EOF
		}
		c.mu.Unlock()
		<-c.readBlock
	}
}

func (c *testConn) WriteMessage(_ int, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return io.ErrClosedPipe
	}
	c.clientMessages = append(c.clientMessages, append([]byte(nil), data...))
	return nil
}

func (c *testConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.closeOnce.Do(func() { close(c.readBlock) })
	return nil
}

type testLogger struct{ fields []string }

func (l *testLogger) Debug(string, ...logging.Field) {}
func (l *testLogger) Info(string, ...logging.Field)  {}
func (l *testLogger) Warn(string, ...logging.Field)  {}
func (l *testLogger) Error(_ string, fields ...logging.Field) {
	for _, field := range fields {
		l.fields = append(l.fields, fmt.Sprint(field.Value))
	}
}
func (l *testLogger) Fatal(string, ...logging.Field) {}
func (l *testLogger) Panic(string, ...logging.Field) {}
