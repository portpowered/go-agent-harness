package cli

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	providerswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/wire"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai/chatgptauth"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openaichatgpt/fakechatgpt"
)

const (
	chatGPTTestToken   = "chatgpt-access-token"
	chatGPTTestAccount = "acct-cli"
)

// chatGPTCLI is a CLI wired like the shipped binary for ask and chat: the
// real host resolver and the real provider service, pointed at a fake
// ChatGPT backend with --base-url.
type chatGPTCLI struct {
	agentCLI  *AgentCLI
	configDir string
	fake      *fakechatgpt.Server
	baseURL   string
}

func newChatGPTCLI(t *testing.T) *chatGPTCLI {
	t.Helper()
	configDir := t.TempDir()
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = configDir
	globalFlags.WorkDirPath = t.TempDir()
	textService := sessionwire.NewService(sessionwire.Dependencies{
		ToolExecutor:    chatTestToolExecutor{},
		Resolver:        services.NewSessionResolverWithStoreFactory(globalFlags, testFileStoreFactory()),
		ProviderService: providerswire.NewService(providerswire.Dependencies{}),
	})
	agentCLI, _, _ := newTestAgentCLIWithService(globalFlags, textService)
	fake := fakechatgpt.New(chatGPTTestToken, chatGPTTestAccount)
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	return &chatGPTCLI{agentCLI: agentCLI, configDir: configDir, fake: fake, baseURL: server.URL + "/backend-api/codex"}
}

// signIn stores a ChatGPT login as `yui auth chatgpt` would.
func (c *chatGPTCLI) signIn(t *testing.T) {
	t.Helper()
	store := chatgptauth.NewFileStore(config.ChatGPTAuthStorePath(c.configDir))
	if err := store.Save(chatgptauth.Credential{
		AccessToken: chatGPTTestToken, RefreshToken: "refresh", AccountID: chatGPTTestAccount,
		ExpiresAt: time.Now().Add(time.Hour), LastRefresh: time.Now(),
	}); err != nil {
		t.Fatalf("save ChatGPT login: %v", err)
	}
}

func (c *chatGPTCLI) requestModels(t *testing.T) []string {
	t.Helper()
	var models []string
	for _, req := range c.fake.Requests() {
		if !strings.HasSuffix(req.Path, "/responses") {
			continue
		}
		var body struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(req.Body, &body); err != nil {
			t.Fatalf("decode responses request: %v", err)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer "+chatGPTTestToken {
			t.Fatalf("Authorization = %q, want the stored ChatGPT token", got)
		}
		models = append(models, body.Model)
	}
	return models
}

func TestAskWithOpenAIChatGPTUsesTheLoginAndAccountDefaultModel(t *testing.T) {
	c := newChatGPTCLI(t)
	c.signIn(t)
	c.fake.SetModels(`{"models":[{"slug":"gpt-account-default","visibility":"list","priority":1}]}`)
	c.fake.Enqueue(fakechatgpt.TextReply("hello from chatgpt"))

	got := executeRoot(t, c.agentCLI, []string{"ask", "--provider", "openai-chatgpt", "--base-url", c.baseURL, "hi"}, "")
	if got.err != nil {
		t.Fatalf("ask: %v (stderr %q)", got.err, got.stderr)
	}
	if !strings.Contains(got.stdout, "hello from chatgpt") {
		t.Fatalf("stdout = %q, want the ChatGPT reply", got.stdout)
	}
	if models := c.requestModels(t); len(models) != 1 || models[0] != "gpt-account-default" {
		t.Fatalf("responses models = %v, want [gpt-account-default]", models)
	}
}

func TestChatWithOpenAIChatGPTRunsATurnOnTheLogin(t *testing.T) {
	c := newChatGPTCLI(t)
	c.signIn(t)
	c.fake.Enqueue(fakechatgpt.TextReply("chat reply"))

	got := executeInteractiveRoot(t, c.agentCLI, []string{
		"chat", "--loop", "--max-iterations", "1",
		"--provider", "openai-chatgpt", "--base-url", c.baseURL, "--model", "gpt-chosen",
	}, "task\n")
	if got.err != nil {
		t.Fatalf("chat: %v (stderr %q)", got.err, got.stderr)
	}
	if !strings.Contains(got.stdout, "chat reply") {
		t.Fatalf("stdout = %q, want the ChatGPT reply", got.stdout)
	}
	if models := c.requestModels(t); len(models) != 1 || models[0] != "gpt-chosen" {
		t.Fatalf("responses models = %v, want [gpt-chosen]", models)
	}
}

func TestOpenAIChatGPTWithoutLoginTellsTheUserToSignIn(t *testing.T) {
	tests := []struct {
		name  string
		args  func(baseURL string) []string
		input string
	}{
		{name: "ask", args: func(baseURL string) []string {
			return []string{"ask", "--provider", "openai-chatgpt", "--base-url", baseURL, "hi"}
		}},
		{name: "chat", input: "task\n", args: func(baseURL string) []string {
			return []string{"chat", "--loop", "--max-iterations", "1", "--provider", "openai-chatgpt", "--base-url", baseURL}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newChatGPTCLI(t)
			args := tt.args(c.baseURL)
			got := executeInteractiveRoot(t, c.agentCLI, args, tt.input)
			combined := got.stdout + got.stderr
			if got.err != nil {
				combined += got.err.Error()
			}
			if !strings.Contains(combined, "run `yui auth chatgpt`") {
				t.Fatalf("output = %q, err = %v; want a run `yui auth chatgpt` message", combined, got.err)
			}
			if n := len(c.fake.Requests()); n != 0 {
				t.Fatalf("backend requests = %d, want 0", n)
			}
		})
	}
}

func TestAskWithOpenAIChatGPTRunsAToolStepThroughTheLoop(t *testing.T) {
	c := newChatGPTCLI(t)
	c.signIn(t)
	c.fake.Enqueue(fakechatgpt.Reply{Events: []string{
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"enc-1"}}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"call_cli","name":"lookup","arguments":"{}"}}`,
		`{"type":"response.completed","response":{}}`,
	}}, fakechatgpt.TextReply("final answer"))

	got := executeRoot(t, c.agentCLI, []string{"ask", "--provider", "openai-chatgpt", "--base-url", c.baseURL, "--model", "gpt-tools", "go"}, "")
	if got.err != nil {
		t.Fatalf("ask: %v (stderr %q)", got.err, got.stderr)
	}
	if !strings.Contains(got.stdout, "final answer") {
		t.Fatalf("stdout = %q, want the final answer", got.stdout)
	}
	requests := c.fake.Requests()
	if len(requests) != 2 {
		t.Fatalf("backend requests = %d, want 2 (tool call, then the answer)", len(requests))
	}
	var body struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(requests[1].Body, &body); err != nil {
		t.Fatalf("decode second request: %v", err)
	}
	var kinds []string
	for _, item := range body.Input {
		kinds = append(kinds, fmt.Sprint(item["type"]))
		if item["type"] == "function_call_output" && item["call_id"] != "call_cli" {
			t.Fatalf("function_call_output call_id = %v, want call_cli", item["call_id"])
		}
		if item["type"] == "reasoning" && item["encrypted_content"] != "enc-1" {
			t.Fatalf("replayed reasoning = %v, want enc-1", item)
		}
	}
	if want := "message reasoning function_call function_call_output"; strings.Join(kinds, " ") != want {
		t.Fatalf("second request items = %v, want %s", kinds, want)
	}
}
