package wire

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type providerFailureCategory string

func (e providerFailureCategory) Error() string { return string(e) }

const errSensitiveProviderFailure providerFailureCategory = "sensitive provider failure"

func TestSelfPlayServiceRedactsReturnedProviderErrorWithoutExposingCause(t *testing.T) {
	const secret = "provider-credential-that-must-not-escape"
	service := NewService(Dependencies{
		SessionService: sensitiveFailureSessionService{err: &sensitiveProviderError{secret: secret}},
		ModelCatalog:   testModelCatalog{},
		Clock:          clock.Real{},
	})
	_, err := service.Run(context.Background(), selfplay.Request{
		APIKey:      secret,
		OutputDir:   filepath.Join(t.TempDir(), "run"),
		MaxDuration: 10 * time.Second,
		MaxTurns:    1,
	})
	if err == nil || !strings.Contains(err.Error(), "[REDACTED]") || strings.Contains(err.Error(), secret) {
		t.Fatalf("provider failure = %v, want a redacted error", err)
	}
	if errors.Unwrap(err) != nil {
		t.Fatalf("returned error exposes an unwrap cause: %v", errors.Unwrap(err))
	}
	var providerErr *sensitiveProviderError
	if errors.As(err, &providerErr) {
		t.Fatalf("returned error exposes the provider error through errors.As: %#v", providerErr)
	}
	if errors.Is(err, errSensitiveProviderFailure) == false {
		t.Fatalf("returned error lost its provider category: %v", err)
	}
	if formatted := fmt.Sprintf("%#v", err); strings.Contains(formatted, secret) {
		t.Fatalf("formatted returned error leaked the credential: %s", formatted)
	}
}

type sensitiveProviderError struct{ secret string }

func (e *sensitiveProviderError) Error() string { return "provider rejected credential " + e.secret }
func (e *sensitiveProviderError) Is(target error) bool {
	return target == errSensitiveProviderFailure
}

type sensitiveFailureSessionService struct{ err error }

func (s sensitiveFailureSessionService) BuildSession(context.Context, providers.SessionConfig) (messages.SessionInferencer, error) {
	return nil, s.err
}

func TestSelfPlayServiceRedactsUnmarkedSensitiveFieldsFromStreamEvidence(t *testing.T) {
	const secret = "unmarked-stream-credential"
	provider := testSessionServiceWithStreamMessage{
		delegate: newTestSessionService(t, ""),
		message: messages.StreamMessage{
			Type:  messages.StreamTypeTextDelta,
			Role:  messages.RoleAssistant,
			Value: messages.NewTextDeltaValue(`{"api_key":"` + secret + `","status":"ready"}`),
		},
	}
	service := NewService(Dependencies{
		SessionService: provider,
		ModelCatalog:   testModelCatalog{},
		Clock:          clock.Real{},
	})
	outputDir := filepath.Join(t.TempDir(), "run")
	_, err := service.Run(context.Background(), selfplay.Request{
		APIKey:      secret,
		OutputDir:   outputDir,
		MaxDuration: 10 * time.Second,
		MaxTurns:    1,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, name := range []string{"agent-a-stream-deltas.jsonl", "agent-b-stream-deltas.jsonl"} {
		data, readErr := os.ReadFile(filepath.Join(outputDir, name))
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		if strings.Contains(string(data), secret) || !strings.Contains(string(data), "[REDACTED]") {
			t.Fatalf("%s did not redact the unmarked credential: %s", name, data)
		}
		if !strings.Contains(string(data), "ready") {
			t.Fatalf("%s dropped non-sensitive stream content: %s", name, data)
		}
	}
}

type testSessionServiceWithStreamMessage struct {
	delegate *testSessionService
	message  messages.StreamMessage
}

func (s testSessionServiceWithStreamMessage) BuildSession(ctx context.Context, config providers.SessionConfig) (messages.SessionInferencer, error) {
	inferencer, err := s.delegate.BuildSession(ctx, config)
	if err != nil {
		return nil, err
	}
	return testSessionInferencerWithStreamMessage{SessionInferencer: inferencer, message: s.message}, nil
}

type testSessionInferencerWithStreamMessage struct {
	messages.SessionInferencer
	message messages.StreamMessage
}

func (i testSessionInferencerWithStreamMessage) ConnectSession(ctx context.Context) (messages.Session, error) {
	session, err := i.SessionInferencer.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	if !session.Receive().Write(ctx, i.message) {
		return nil, ctx.Err()
	}
	return session, nil
}
