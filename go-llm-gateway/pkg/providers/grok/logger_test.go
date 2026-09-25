package grok

import (
	"encoding/json"
	"fmt"
	"io"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
)

// testGrokSessionID is the provider session identifier used by fixtures.
const testGrokSessionID = "sess-xyz"

type stubLogger struct{}

func (stubLogger) Debug(string, ...logging.Field) {}
func (stubLogger) Info(string, ...logging.Field)  {}
func (stubLogger) Warn(string, ...logging.Field)  {}
func (stubLogger) Error(string, ...logging.Field) {}
func (stubLogger) Fatal(string, ...logging.Field) {}
func (stubLogger) Panic(string, ...logging.Field) {}

func TestNew_DefaultLoggerIsSafe(t *testing.T) {
	provider := New()
	if provider.logger == nil {
		t.Fatal("expected default logger")
	}

	provider.logger.Info("info")
	provider.logger.Warn("warn")
	provider.logger.Error("error")
}

func TestWithLoggerUsesInjectedGatewayLogger(t *testing.T) {
	logger := stubLogger{}
	provider := New(WithLogger(logger))
	if provider.logger != logger {
		t.Fatal("expected injected logger to be preserved")
	}
}

// closeForTest closes a test-owned resource and reports an unexpected close
// failure without stopping the test.
func closeForTest(t testing.TB, resource io.Closer) {
	t.Helper()
	if err := resource.Close(); err != nil {
		t.Errorf("close %T: %v", resource, err)
	}
}

// mustMarshalFixture encodes a fixture value built by the test itself; an
// encoding failure is a broken fixture, not a behavior under test.
func mustMarshalFixture(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("marshal test fixture: %v", err))
	}
	return data
}

func grokSessionForTest(t testing.TB, session messages.Session) *grokSession {
	t.Helper()
	grok, ok := session.(*grokSession)
	if !ok {
		t.Fatalf("session type = %T, want *grokSession", session)
	}
	return grok
}
