package service

import (
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runtimeproviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/internal/catalog"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	llmproviders "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai/chatgptauth"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openaichatgpt/fakechatgpt"
)

func TestBuildOpenAIChatGPTSignsWithTheAuthStoreAndNeedsNoAPIKey(t *testing.T) {
	fake := fakechatgpt.New("chatgpt-access", "acct-1")
	fake.Enqueue(fakechatgpt.TextReply("hello from chatgpt"))
	server := httptest.NewServer(fake)
	defer server.Close()
	authPath := filepath.Join(t.TempDir(), "auth", "chatgpt.json")
	if err := chatgptauth.NewFileStore(authPath).Save(chatgptauth.Credential{
		AccessToken: "chatgpt-access", RefreshToken: "refresh", AccountID: "acct-1",
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("save credential: %v", err)
	}

	built, err := New(server.Client(), nil, clock.Real{}, nil, catalog.New(), nil).Build(t.Context(), runtimeproviders.Config{
		Provider: runtimeproviders.OpenAIChatGPTProvider, Model: "gpt-test", BaseURL: server.URL, ChatGPTAuthPath: authPath,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if built.Name() != runtimeproviders.OpenAIChatGPTProvider {
		t.Fatalf("provider name = %q", built.Name())
	}
	response, err := built.Infer(t.Context(), llmproviders.InferenceRequest{Messages: []models.Message{models.NewTextMessage(models.RoleUser, "hi")}})
	if err != nil {
		t.Fatalf("Infer() error = %v", err)
	}
	if got := response.Message.TextContent(); got != "hello from chatgpt" {
		t.Fatalf("response = %q", got)
	}
}

func TestBuildOpenAIChatGPTWithoutLoginFailsFast(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "no auth store path"},
		{name: "auth store missing", path: filepath.Join(t.TempDir(), "auth", "chatgpt.json")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(nil, nil, clock.Real{}, nil, catalog.New(), nil).Build(t.Context(), runtimeproviders.Config{
				Provider: runtimeproviders.OpenAIChatGPTProvider, ChatGPTAuthPath: tt.path,
			})
			if !errors.Is(err, chatgptauth.ErrNotLoggedIn) || !strings.Contains(err.Error(), "run `yui auth chatgpt`") {
				t.Fatalf("Build() error = %v, want a run `yui auth chatgpt` error", err)
			}
		})
	}
}
