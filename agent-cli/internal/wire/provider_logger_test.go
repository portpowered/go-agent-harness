package wire

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/observability"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// TestComposedProviderServiceLogsRealtimeDiagnostics proves the host logger
// composed into the provider service receives realtime provider diagnostics.
func TestComposedProviderServiceLogsRealtimeDiagnostics(t *testing.T) {
	var (
		mu      sync.Mutex
		records []observability.LogRecord
	)
	logger := observability.LoggerFunc(func(_ context.Context, record observability.LogRecord) error {
		mu.Lock()
		defer mu.Unlock()
		records = append(records, record)
		return nil
	})
	service, err := provideProviderService(clock.Real{}, nil, nil, nil, provideProviderLogger(logger))
	if err != nil {
		t.Fatal(err)
	}
	inferencer, err := service.BuildSession(t.Context(), providers.SessionConfig{
		Provider: "openai", Model: providers.OpenAIRealtimeLegacyModel, APIKey: "test-key", WebSocketDialer: failingConnDialer{},
	})
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	if session, err := inferencer.ConnectSession(t.Context()); err == nil {
		_ = session.Close()
	}
	mu.Lock()
	defer mu.Unlock()
	for _, record := range records {
		if record.Level == "info" && record.Message == "openai realtime: websocket connected" && record.Fields["endpoint"] != "" {
			return
		}
	}
	t.Fatalf("host log records = %+v, want provider websocket diagnostic", records)
}

type failingConnDialer struct{}

func (failingConnDialer) Dial(string, map[string]string) (transport.Conn, error) {
	return failingConn{}, nil
}

type failingConn struct{}

func (failingConn) ReadMessage() (int, []byte, error) { return 0, nil, errors.New("closed") }
func (failingConn) WriteMessage(int, []byte) error    { return errors.New("closed") }
func (failingConn) Close() error                      { return nil }
