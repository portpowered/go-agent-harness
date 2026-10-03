package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai/chatgptauth"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openaichatgpt/fakechatgpt"
	live "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/fakelive"
)

// recordingLookupTool is the session's lookup_order tool.
type recordingLookupTool struct {
	mu    sync.Mutex
	calls []messages.ToolCall
}

func (r *recordingLookupTool) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	r.mu.Lock()
	r.calls = append(r.calls, call)
	r.mu.Unlock()
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: "order 42: packed, ships tomorrow"}, nil
}

func (r *recordingLookupTool) snapshot() []messages.ToolCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]messages.ToolCall(nil), r.calls...)
}

// TestSessionCommandAnswersGPTLiveDelegationsOnTheChatGPTLogin runs
// `session --provider openai-live` in process with a ChatGPT login and no
// delegation configuration. GPT-Live (the fake server) delegates; the
// delegation backend defaults to openai-chatgpt with the account's default
// model (the fake ChatGPT backend), which calls the session's lookup_order
// tool and answers; the fake GPT-Live receives the answer as a commentary
// append for that delegation before it speaks. It runs on the host clock
// like the other composed CLI tests, but nothing waits on a timer.
func TestSessionCommandAnswersGPTLiveDelegationsOnTheChatGPTLogin(t *testing.T) {
	const (
		apiKey       = "sk-live-cli"
		delegationID = "del_cli"
	)
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	backend := fakechatgpt.New(chatGPTTestToken, chatGPTTestAccount)
	backend.SetModels(`{"models":[{"slug":"gpt-account-default","visibility":"list","priority":1}]}`)
	backend.Enqueue(
		fakechatgpt.ToolCallReply("call_order", "lookup_order", `{"order":"42"}`),
		fakechatgpt.TextReply("Order 42 ships tomorrow."),
	)
	server := httptest.NewServer(backend)
	t.Cleanup(server.Close)
	configYAML := "model:\n  provider: openai\n  openai:\n    model: gpt-realtime\n    api_key: " + apiKey +
		"\n  openai_chatgpt:\n    base_url: " + server.URL + "/backend-api/codex\n"
	if err := os.WriteFile(filepath.Join(configDir, config.ConfigFileName), []byte(configYAML), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := chatgptauth.NewFileStore(config.ChatGPTAuthStorePath(configDir)).Save(chatgptauth.Credential{
		AccessToken: chatGPTTestToken, RefreshToken: "refresh", AccountID: chatGPTTestAccount,
		ExpiresAt: time.Now().Add(time.Hour), LastRefresh: time.Now(),
	}); err != nil {
		t.Fatalf("save ChatGPT login: %v", err)
	}
	audioIn := filepath.Join(root, "in.pcm")
	if err := os.WriteFile(audioIn, bytes.Repeat([]byte{0x10, 0x27, 0xf0, 0xd8}, 1200), 0o600); err != nil {
		t.Fatalf("write audio-in: %v", err)
	}
	fake := fakelive.New(fakelive.WithAPIKey(apiKey), fakelive.WithScript(
		fakelive.AwaitClient(live.TypeInputAudioAppend, 1),
		fakelive.Send(
			live.InputTranscriptDelta{EventID: "evt_in", Delta: "Where is order forty-two?", StartMS: 0, EndMS: 1500},
			live.DelegationCreated{EventID: "evt_del", OffsetMS: 1400, Delegation: live.DelegationInfo{ID: delegationID, Type: "delegation", Target: live.DelegationClient}},
		),
		fakelive.AwaitClient(live.TypeCommentaryAppend, 1),
		fakelive.Send(
			live.OutputAudioDelta{Delta: base64.StdEncoding.EncodeToString(make([]byte, 480))},
			live.OutputTranscriptDelta{Delta: "Your order ships tomorrow.", StartMS: 2000, EndMS: 2500},
			live.OutputTranscriptDelta{Delta: "Anything else?", StartMS: 3500, EndMS: 3800},
		),
	))
	tool := &recordingLookupTool{}
	capabilities := func(context.Context, *config.Config) (SessionToolCapabilities, error) {
		return SessionToolCapabilities{
			Executor:    tool,
			Definitions: []messages.ToolDefinition{{Name: "lookup_order", Description: "Look up an order by number."}},
		}, nil
	}

	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = configDir
	command := newTestSessionCommand(flags.NewAskFlags(), globalFlags, testSessionDeps{
		Dialer: fake.Dialer(), Capabilities: SessionToolCapabilitiesFactory(capabilities),
	}).Generate()
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"--provider", config.ProviderOpenAILive, "--model", live.Model1, "--audio-in", audioIn})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err := command.ExecuteContext(ctx); err != nil {
		t.Fatalf("session --provider openai-live: %v\nstdout=%q\nstderr=%q\nfake errors=%v", err, stdout.String(), stderr.String(), fake.Errors())
	}

	if calls := tool.snapshot(); len(calls) != 1 || calls[0].Name != "lookup_order" || calls[0].Arguments != `{"order":"42"}` {
		t.Fatalf("tool calls = %+v, want one lookup_order for order 42", calls)
	}
	var commentary []live.CommentaryAppend
	for _, event := range fake.ClientEvents() {
		if appended, ok := event.(live.CommentaryAppend); ok {
			commentary = append(commentary, appended)
		}
	}
	if len(commentary) != 1 || commentary[0].DelegationID == nil || *commentary[0].DelegationID != delegationID || commentary[0].Content != "Order 42 ships tomorrow." {
		t.Fatalf("commentary appends = %+v, want the backend's answer for %s", commentary, delegationID)
	}
	assertBackendUsedTheLogin(t, backend)
	if errs := fake.Errors(); len(errs) != 0 {
		t.Fatalf("fake GPT-Live errors: %v", errs)
	}
}

// assertBackendUsedTheLogin checks that both backend turns ran on the
// ChatGPT login with the account's default model and offered the tool.
func assertBackendUsedTheLogin(t *testing.T, backend *fakechatgpt.Server) {
	t.Helper()
	turns := 0
	for _, req := range backend.Requests() {
		if !strings.HasSuffix(req.Path, "/responses") {
			continue
		}
		turns++
		var body struct {
			Model string `json:"model"`
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(req.Body, &body); err != nil {
			t.Fatalf("decode responses request: %v", err)
		}
		if req.Header.Get("Authorization") != "Bearer "+chatGPTTestToken || body.Model != "gpt-account-default" {
			t.Fatalf("backend request model %q auth %q, want the account default on the ChatGPT login", body.Model, req.Header.Get("Authorization"))
		}
		if len(body.Tools) != 1 || body.Tools[0].Name != "lookup_order" {
			t.Fatalf("backend tools = %+v, want the session's lookup_order", body.Tools)
		}
	}
	if turns != 2 {
		t.Fatalf("backend turns = %d, want the tool call and the answer", turns)
	}
}
