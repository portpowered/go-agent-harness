package localai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/capabilities"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
)

func TestProviderContractThroughGateway(t *testing.T) {
	conn := newTestConn(map[string]any{"type": "session.created", "session": map[string]any{"model": WireModel}})
	dialer := &testDialer{conn: conn}
	p := New(WithBaseURL("ws://localai.test/v1/realtime"), WithWebSocketDialer(dialer))
	if p.Name() != ProviderName || p.Model() != ModelID {
		t.Fatalf("identity = (%q, %q)", p.Name(), p.Model())
	}
	caps := p.Capabilities()
	if caps.Provider != ProviderName || caps.Metadata["wireModel"] != WireModel || caps.Metadata["credentialRequired"] != "false" {
		t.Fatalf("metadata = %+v", caps)
	}
	for _, feature := range []capabilities.FeatureCapability{caps.Session.Sessions, caps.Session.AudioInput, caps.Session.AudioOutput} {
		if feature.State != capabilities.CapabilityStateSupported {
			t.Fatalf("capability = %q, want supported", feature.State)
		}
	}
	sessionGateway, err := gateway.NewSessionGateway(gateway.WithSessionProvider(p))
	if err != nil {
		t.Fatalf("NewSessionGateway: %v", err)
	}
	session, err := sessionGateway.ConnectSession(context.Background(), models.SessionConfig{Model: ModelID})
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()
	if dialer.url != "ws://localai.test/v1/realtime?model=gpt-realtime" || len(dialer.headers) != 0 {
		t.Fatalf("dial = (%q, %v), want endpoint without credentials", dialer.url, dialer.headers)
	}
	var update struct {
		Session struct {
			Model string `json:"model"`
		} `json:"session"`
	}
	if len(conn.client) == 0 || json.Unmarshal(conn.client[0], &update) != nil || update.Session.Model != WireModel {
		t.Fatalf("session update model = %q, want %q", update.Session.Model, WireModel)
	}
}

func TestEndpointPrecedence(t *testing.T) {
	tests := []struct{ name, invocation, configuration, environment, want string }{
		{"default", "", "", "", DefaultEndpoint},
		{"environment", "", "", "ws://env.test/realtime?keep=env", "ws://env.test/realtime?keep=env&model=gpt-realtime"},
		{"configuration", "", "ws://config.test/realtime", "ws://env.test/realtime", "ws://config.test/realtime?model=gpt-realtime"},
		{"invocation", "ws://invoke.test/realtime?keep=1", "ws://config.test/realtime", "ws://env.test/realtime", "ws://invoke.test/realtime?keep=1&model=gpt-realtime"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New(WithBaseURL(tt.invocation), WithConfig(Config{BaseURL: tt.configuration}), WithEnvironmentBaseURL(tt.environment))
			got, err := p.endpoint()
			if err != nil || got != tt.want {
				t.Fatalf("endpoint = (%q, %v), want %q", got, err, tt.want)
			}
		})
	}
}

func TestConnectionErrorIsFastTypedAndSecretFree(t *testing.T) {
	secret := "localai-test-secret-should-never-escape"
	t.Setenv("AGENT_MODEL__LOCALAI__API_KEY", secret)
	endpoint := "ws://127.0.0.1:1/v1/realtime"
	logger, dialer := &testLogger{}, &testDialer{err: errors.New("connection refused")}
	p := New(WithEnvironmentBaseURL(endpoint), WithWebSocketDialer(dialer), WithLogger(logger))
	started := time.Now()
	_, err := p.ConnectSession(context.Background(), models.SessionConfig{Model: ModelID})
	if time.Since(started) >= 2*time.Second {
		t.Fatal("closed endpoint was not fast")
	}
	var connectionErr *ConnectionError
	if !errors.As(err, &connectionErr) || !errors.Is(err, ErrConnection) || !errors.Is(err, providers.ErrTransport) || !strings.Contains(err.Error(), endpoint) || !strings.Contains(err.Error(), "start LocalAI") {
		t.Fatalf("connection error = %T %v", err, err)
	}
	if strings.Contains(errorText(err), secret) {
		t.Fatalf("error chain leaked credential: %v", err)
	}
	for _, value := range logger.values {
		if strings.Contains(value, secret) {
			t.Fatalf("logger leaked credential: %q", value)
		}
	}
	if len(dialer.headers) != 0 {
		t.Fatalf("dialer received credentials: %v", dialer.headers)
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range joined.Unwrap() {
			text += errorText(cause)
		}
	}
	if cause, ok := err.(interface{ Unwrap() error }); ok {
		text += errorText(cause.Unwrap())
	}
	return text
}

type testDialer struct {
	conn    *testConn
	err     error
	url     string
	headers map[string]string
}

func (d *testDialer) Dial(endpoint string, headers map[string]string) (openai.WebSocketConn, error) {
	d.url, d.headers = endpoint, headers
	if d.err != nil {
		return nil, d.err
	}
	return d.conn, nil
}

type testConn struct {
	server    []byte
	client    [][]byte
	wait      chan struct{}
	closeOnce sync.Once
}

func newTestConn(event map[string]any) *testConn {
	data, _ := json.Marshal(event)
	return &testConn{server: data, wait: make(chan struct{})}
}
func (c *testConn) ReadMessage() (int, []byte, error) {
	if c.server != nil {
		data := c.server
		c.server = nil
		return 1, data, nil
	}
	<-c.wait
	return 0, nil, io.EOF
}
func (c *testConn) WriteMessage(_ int, data []byte) error {
	c.client = append(c.client, data)
	return nil
}
func (c *testConn) Close() error { c.closeOnce.Do(func() { close(c.wait) }); return nil }

type testLogger struct{ values []string }

func (l *testLogger) Debug(string, ...logging.Field) {}
func (l *testLogger) Info(string, ...logging.Field)  {}
func (l *testLogger) Warn(string, ...logging.Field)  {}
func (l *testLogger) Fatal(string, ...logging.Field) {}
func (l *testLogger) Panic(string, ...logging.Field) {}
func (l *testLogger) Error(_ string, fields ...logging.Field) {
	for _, field := range fields {
		if value, ok := field.Value.(string); ok {
			l.values = append(l.values, value)
		}
	}
}
