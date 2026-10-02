package service

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/internal/catalog"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai/chatgptauth"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexlive"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexrtc"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexrtc/fakecodex"
)

// chatGPTStore saves a ChatGPT login that needs no refresh and returns its
// path.
func chatGPTStore(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth", "chatgpt.json")
	if err := chatgptauth.NewFileStore(path).Save(chatgptauth.Credential{
		AccessToken: fakecodex.DefaultToken, RefreshToken: "refresh", AccountID: fakecodex.DefaultAccountID,
		ExpiresAt: time.Now().Add(time.Hour), LastRefresh: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestBuildSessionConnectsTheCodexRouteOnTheChatGPTLogin builds a
// gpt-live-1-codex session from the auth store alone, with no API key, and
// connects it to the fake ChatGPT backend with the client version.
func TestBuildSessionConnectsTheCodexRouteOnTheChatGPTLogin(t *testing.T) {
	network, err := fakecodex.NewVirtualNetwork()
	if err != nil {
		t.Fatal(err)
	}
	backend := fakecodex.New(fakecodex.WithPeerNetwork(network))
	server := httptest.NewServer(backend)
	t.Cleanup(func() {
		server.Close()
		if err := errors.Join(backend.Close(), network.Close()); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	service := New(nil, nil, clock.Real{}, nil, catalog.New(), nil)
	inferencer, err := service.BuildSession(ctx, providers.SessionConfig{
		Provider: providers.OpenAILiveProvider, Model: providers.OpenAILiveCodexModel,
		ChatGPTAuthPath: chatGPTStore(t), ClientVersion: "9.9.9",
		CodexTransport: &codexlive.Transport{
			BackendURL:      server.URL + "/backend-api/codex",
			SidebandBaseURL: "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/live",
			HTTPClient:      server.Client(),
			PeerSettings:    network.Client,
		},
	})
	if err != nil {
		t.Fatalf("BuildSession(gpt-live-1-codex): %v", err)
	}
	session, err := inferencer.ConnectSession(ctx)
	if err != nil {
		t.Fatalf("ConnectSession: %v (backend errors %v)", err, backend.Errors())
	}
	msg := <-session.Receive().Chan()
	if open, ok := msg.Value.(*messages.SessionOpenValue); !ok || open.SessionID != fakecodex.DefaultCallID {
		t.Fatalf("first message = %s %+v, want SESSION.OPEN for the call", msg.Type, msg.Value)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	calls := backend.Calls()
	if len(calls) != 1 || calls[0].Header.Get(codexrtc.HeaderVersion) != "9.9.9" || calls[0].Session.Model != providers.OpenAILiveCodexModel {
		t.Fatalf("calls = %+v, want one gpt-live-1-codex call with the client version", calls)
	}
}

// TestBuildSessionRefusesTheCodexRouteWithoutAChatGPTLogin pins the
// credential order: gpt-live-1-codex needs the ChatGPT store (an API key does
// not do), and has no provider capture yet.
func TestBuildSessionRefusesTheCodexRouteWithoutAChatGPTLogin(t *testing.T) {
	service := New(nil, nil, clock.Real{}, nil, catalog.New(), nil)
	missing := filepath.Join(t.TempDir(), "auth", "chatgpt.json")
	for name, tc := range map[string]struct {
		cfg  providers.SessionConfig
		want string
	}{
		"no store":          {cfg: providers.SessionConfig{APIKey: "sk-test"}, want: "yui auth chatgpt"},
		"store not written": {cfg: providers.SessionConfig{ChatGPTAuthPath: missing}, want: "yui auth chatgpt"},
		"record":            {cfg: providers.SessionConfig{ChatGPTAuthPath: chatGPTStore(t), RecordPath: "capture.json"}, want: "record or replay"},
		"replay":            {cfg: providers.SessionConfig{ChatGPTAuthPath: chatGPTStore(t), ReplayPath: "capture.json"}, want: "record or replay"},
	} {
		t.Run(name, func(t *testing.T) {
			tc.cfg.Provider, tc.cfg.Model = providers.OpenAILiveProvider, providers.OpenAILiveCodexModel
			if _, err := service.BuildSession(t.Context(), tc.cfg); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("BuildSession = %v, want %q", err, tc.want)
			}
		})
	}
	_, err := service.BuildSession(t.Context(), providers.SessionConfig{
		Provider: providers.OpenAILiveProvider, Model: providers.OpenAILiveCodexModel, ChatGPTAuthPath: chatGPTStore(t),
		CodexTransport: "not a transport",
	})
	if err == nil || !strings.Contains(err.Error(), "want *codexlive.Transport") {
		t.Fatalf("BuildSession with a foreign CodexTransport = %v", err)
	}
	_, err = service.BuildSession(t.Context(), providers.SessionConfig{
		Provider: providers.OpenAILiveProvider, Model: providers.OpenAILive1Model, ChatGPTAuthPath: chatGPTStore(t),
	})
	if err == nil || !strings.Contains(err.Error(), "requires an OpenAI API key") || !strings.Contains(err.Error(), providers.OpenAILiveCodexModel) {
		t.Fatalf("BuildSession(gpt-live-1) with only a ChatGPT login = %v, want the API-key refusal naming the codex model", err)
	}
}
