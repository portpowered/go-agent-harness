package service

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/internal/catalog"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// TestBuildSessionRoutesProviderDiagnosticsToInjectedLogger proves realtime
// provider diagnostics reach the host logger instead of a discarding default.
func TestBuildSessionRoutesProviderDiagnosticsToInjectedLogger(t *testing.T) {
	for _, tc := range []struct{ provider, model, want string }{
		{"openai", providers.OpenAIRealtimeLegacyModel, "openai realtime: websocket connected"},
		{"grok", "grok-realtime", "grok: websocket connected"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			logger := &messageLogger{}
			service := New(nil, logger, clock.Real{}, nil, catalog.New(), nil)
			inferencer, err := service.BuildSession(t.Context(), providers.SessionConfig{
				Provider: tc.provider, Model: tc.model, APIKey: "test-key", WebSocketDialer: closedConnDialer{},
			})
			if err != nil {
				t.Fatalf("BuildSession: %v", err)
			}
			if session, err := inferencer.ConnectSession(t.Context()); err == nil {
				if closeErr := session.Close(); closeErr != nil {
					t.Logf("close session: %v", closeErr)
				}
			}
			if !logger.contains(tc.want) {
				t.Fatalf("injected logger messages = %q, want %q", logger.snapshot(), tc.want)
			}
		})
	}
}

type messageLogger struct {
	mu       sync.Mutex
	messages []string
}

func (l *messageLogger) record(msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.messages = append(l.messages, msg)
}

func (l *messageLogger) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.messages...)
}

func (l *messageLogger) contains(want string) bool {
	for _, msg := range l.snapshot() {
		if strings.Contains(msg, want) {
			return true
		}
	}
	return false
}

func (l *messageLogger) Debug(msg string, _ ...logging.Field) { l.record(msg) }
func (l *messageLogger) Info(msg string, _ ...logging.Field)  { l.record(msg) }
func (l *messageLogger) Warn(msg string, _ ...logging.Field)  { l.record(msg) }
func (l *messageLogger) Error(msg string, _ ...logging.Field) { l.record(msg) }
func (l *messageLogger) Fatal(msg string, _ ...logging.Field) { l.record(msg) }
func (l *messageLogger) Panic(msg string, _ ...logging.Field) { l.record(msg) }

type closedConnDialer struct{}

func (closedConnDialer) Dial(string, map[string]string) (transport.Conn, error) {
	return closedConn{}, nil
}

type closedConn struct{}

func (closedConn) ReadMessage() (int, []byte, error) { return 0, nil, errors.New("closed") }
func (closedConn) WriteMessage(int, []byte) error    { return errors.New("closed") }
func (closedConn) Close() error                      { return nil }
