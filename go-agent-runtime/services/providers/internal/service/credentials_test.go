package service

import (
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func TestHostedProviderAdmissionRejectsMissingCredential(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil)
	for _, endpoint := range []string{"", "https://api.openai.com/v1", "https://openrouter.ai/api/v1"} {
		t.Run(endpoint, func(t *testing.T) {
			if _, err := service.Build(t.Context(), providers.Config{Provider: "openai", BaseURL: endpoint}); err == nil || !strings.Contains(err.Error(), "API key") {
				t.Fatalf("Build missing credential: %v", err)
			}
			if _, err := service.BuildSession(t.Context(), providers.SessionConfig{Provider: "openai", Model: "realtime", BaseURL: endpoint}); err == nil || !strings.Contains(err.Error(), "openai realtime api key is missing") {
				t.Fatalf("BuildSession missing credential: %v", err)
			}
		})
	}
}

func TestProviderAdmissionAllowsExplicitAnonymousEndpoint(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil)
	if _, err := service.Build(t.Context(), providers.Config{Provider: "local", BaseURL: "http://localhost:8080/v1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.BuildSession(t.Context(), providers.SessionConfig{Provider: "local", Model: "local", RealtimeURL: "ws://localhost:8080/realtime"}); err != nil {
		t.Fatal(err)
	}
	if err := validateSessionCredential(providers.SessionConfig{ReplayPath: "offline-capture.json"}, "openai"); err != nil {
		t.Fatal(err)
	}
}

func TestProviderAdmissionRejectsNilContext(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil)
	//lint:ignore SA1012 Exercise the public admission contract's nil-context rejection.
	if _, err := service.Build(nil, providers.Config{}); err == nil {
		t.Fatal("nil context accepted")
	}
}
