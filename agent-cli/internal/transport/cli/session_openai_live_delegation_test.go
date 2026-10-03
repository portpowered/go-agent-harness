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
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai/chatgptauth"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openaichatgpt/fakechatgpt"
	live "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/fakelive"
)

// lookupOrderTool is the session tool the delegation tests' backend calls.
const lookupOrderTool = "lookup_order"

// recordingLookupTool is the session's lookup_order tool; it answers
// content.
type recordingLookupTool struct {
	content string
	mu      sync.Mutex
	calls   []messages.ToolCall
}

func (r *recordingLookupTool) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	r.mu.Lock()
	r.calls = append(r.calls, call)
	r.mu.Unlock()
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: r.content}, nil
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
	run := runDelegationSessionCommand(t, defaultLookupResult)

	if calls := run.tool.snapshot(); len(calls) != 1 || calls[0].Name != lookupOrderTool || calls[0].Arguments != `{"order":"42"}` {
		t.Fatalf("tool calls = %+v, want one lookup_order for order 42", calls)
	}
	var commentary []live.CommentaryAppend
	for _, event := range run.fake.ClientEvents() {
		if appended, ok := event.(live.CommentaryAppend); ok {
			commentary = append(commentary, appended)
		}
	}
	if len(commentary) != 1 || commentary[0].DelegationID == nil || *commentary[0].DelegationID != delegationCLIID || commentary[0].Content != "Order 42 ships tomorrow." {
		t.Fatalf("commentary appends = %+v, want the backend's answer for %s", commentary, delegationCLIID)
	}
	assertBackendUsedTheLogin(t, run.backend, chatGPTTestToken)
	if errs := run.fake.Errors(); len(errs) != 0 {
		t.Fatalf("fake GPT-Live errors: %v", errs)
	}
}

// TestSessionCommandRecordsDelegationToolCallsWithArgumentsAndResult runs
// the same delegation with --record-dir. The delegation's tool call is
// audited like the voice loop's: the recording bundle and the trace both
// hold the tool name, the arguments and the result, the result bounded to
// 4 KiB with a truncation marker and the session key redacted. The key
// appears twice, the second time across the 4 KiB cut: redaction runs on the
// whole result before the cut, so not even its first bytes are kept. The
// delegation backend's own credentials, the ChatGPT sign-in's access and
// refresh tokens, are redacted too.
func TestSessionCommandRecordsDelegationToolCallsWithArgumentsAndResult(t *testing.T) {
	prefix := "order 42: packed; courier key " + delegationCLIKey + "; backend " + chatGPTTestToken + " " + delegationCLIRefresh + "; "
	straddle := session.LiveDelegationToolPayloadLimit - len(session.LiveDelegationToolTruncated) - 4
	result := prefix + strings.Repeat("m", straddle-len(prefix)) + delegationCLIKey + strings.Repeat(" manifest line", 100)
	recordDir := filepath.Join(t.TempDir(), "bundle")
	run := runDelegationSessionCommand(t, result, "--record-dir", recordDir)

	bundle, trace := readDelegationEvidence(t, recordDir)
	for name, records := range map[string][]string{"bundle": bundle, "trace": trace} {
		call, result := delegationToolRecord(records, "delegation_tool_call"), delegationToolRecord(records, "delegation_tool_result")
		if call == "" || result == "" {
			t.Fatalf("%s has no delegation tool call and result; records:\n%s", name, strings.Join(records, "\n"))
		}
		for _, want := range []string{lookupOrderTool, `{\"order\":\"42\"}`, delegationCLIID} {
			if !strings.Contains(call, want) || !strings.Contains(result, want) {
				t.Fatalf("%s delegation tool records lack %q:\ncall %s\nresult %s", name, want, call, result)
			}
		}
		// The bundle and the trace each use their own redaction marker.
		if !strings.Contains(result, "order 42: packed; courier key ") || !strings.Contains(result, "REDACTED") || !strings.Contains(result, "[truncated]") {
			t.Fatalf("%s delegation tool result = %s, want the redacted result cut with a truncation marker", name, result)
		}
		if strings.Contains(call+result, delegationCLIKey[:4]) || strings.Contains(call+result, chatGPTTestToken) || strings.Contains(call+result, delegationCLIRefresh) {
			t.Fatalf("%s delegation tool records leak the key or exceed the bound:\ncall %s\nresult %s", name, call, result)
		}
	}
	if errs := run.fake.Errors(); len(errs) != 0 {
		t.Fatalf("fake GPT-Live errors: %v", errs)
	}
}

const (
	delegationCLIKey    = "sk-live-cli"
	delegationCLIID     = "del_cli"
	defaultLookupResult = "order 42: packed, ships tomorrow"
	// delegationCLIRefresh is the ChatGPT sign-in's refresh token.
	delegationCLIRefresh = "chatgpt-refresh-delegation-cli"
)

// delegationCommandRun is one finished delegation session command.
type delegationCommandRun struct {
	fake    *fakelive.Server
	backend *fakechatgpt.Server
	tool    *recordingLookupTool
}

// runDelegationSessionCommand runs `session --provider openai-live` with a
// ChatGPT login against a fake GPT-Live that delegates "where is order 42"
// once; the backend calls lookup_order (answering lookupResult) and then
// answers.
func runDelegationSessionCommand(t *testing.T, lookupResult string, extraArgs ...string) delegationCommandRun {
	t.Helper()
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	backend := fakechatgpt.New(chatGPTTestToken, chatGPTTestAccount)
	backend.SetModels(`{"models":[{"slug":"gpt-account-default","visibility":"list","priority":1}]}`)
	backend.Enqueue(
		fakechatgpt.ToolCallReply("call_order", lookupOrderTool, `{"order":"42"}`),
		fakechatgpt.TextReply("Order 42 ships tomorrow."),
	)
	server := httptest.NewServer(backend)
	t.Cleanup(server.Close)
	configYAML := "model:\n  provider: openai\n  openai:\n    model: gpt-realtime\n    api_key: " + delegationCLIKey +
		"\n  openai_chatgpt:\n    base_url: " + server.URL + "/backend-api/codex\n"
	if err := os.WriteFile(filepath.Join(configDir, config.ConfigFileName), []byte(configYAML), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := chatgptauth.NewFileStore(config.ChatGPTAuthStorePath(configDir)).Save(chatgptauth.Credential{
		AccessToken: chatGPTTestToken, RefreshToken: delegationCLIRefresh, AccountID: chatGPTTestAccount,
		ExpiresAt: time.Now().Add(time.Hour), LastRefresh: time.Now(),
	}); err != nil {
		t.Fatalf("save ChatGPT login: %v", err)
	}
	audioIn := filepath.Join(root, "in.pcm")
	if err := os.WriteFile(audioIn, bytes.Repeat([]byte{0x10, 0x27, 0xf0, 0xd8}, 1200), 0o600); err != nil {
		t.Fatalf("write audio-in: %v", err)
	}
	fake := fakelive.New(fakelive.WithAPIKey(delegationCLIKey), fakelive.WithScript(
		fakelive.AwaitClient(live.TypeInputAudioAppend, 1),
		fakelive.Send(
			live.InputTranscriptDelta{EventID: "evt_in", Delta: "Where is order forty-two?", StartMS: 0, EndMS: 1500},
			live.DelegationCreated{EventID: "evt_del", OffsetMS: 1400, Delegation: live.DelegationInfo{ID: delegationCLIID, Type: "delegation", Target: live.DelegationClient}},
		),
		fakelive.AwaitClient(live.TypeCommentaryAppend, 1),
		fakelive.Send(
			live.OutputAudioDelta{Delta: base64.StdEncoding.EncodeToString(make([]byte, 480))},
			live.OutputTranscriptDelta{Delta: "Your order ships tomorrow.", StartMS: 2000, EndMS: 2500},
			live.OutputTranscriptDelta{Delta: "Anything else?", StartMS: 3500, EndMS: 3800},
		),
	))
	tool := &recordingLookupTool{content: lookupResult}
	capabilities := func(context.Context, *config.Config) (SessionToolCapabilities, error) {
		return SessionToolCapabilities{
			Executor:    tool,
			Definitions: []messages.ToolDefinition{{Name: lookupOrderTool, Description: "Look up an order by number."}},
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
	command.SetArgs(append([]string{"--provider", config.ProviderOpenAILive, "--model", live.Model1, "--audio-in", audioIn}, extraArgs...))
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err := command.ExecuteContext(ctx); err != nil {
		t.Fatalf("session --provider openai-live: %v\nstdout=%q\nstderr=%q\nfake errors=%v", err, stdout.String(), stderr.String(), fake.Errors())
	}
	return delegationCommandRun{fake: fake, backend: backend, tool: tool}
}

// assertBackendUsedTheLogin checks that both backend turns ran on the
// ChatGPT login with the account's default model and offered the tool.
func assertBackendUsedTheLogin(t *testing.T, backend *fakechatgpt.Server, token string) {
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
		if req.Header.Get("Authorization") != "Bearer "+token || body.Model != "gpt-account-default" {
			t.Fatalf("backend request model %q auth %q, want the account default on the ChatGPT login", body.Model, req.Header.Get("Authorization"))
		}
		if len(body.Tools) != 1 || body.Tools[0].Name != lookupOrderTool {
			t.Fatalf("backend tools = %+v, want the session's lookup_order", body.Tools)
		}
	}
	if turns != 2 {
		t.Fatalf("backend turns = %d, want the tool call and the answer", turns)
	}
}

// readDelegationEvidence returns the decoded payloads of the bundle's agent
// transcript and of its trace timeline.
func readDelegationEvidence(t *testing.T, dir string) (bundle, trace []string) {
	t.Helper()
	return decodedPayloads(t, filepath.Join(dir, "agent.transcript.jsonl")), decodedPayloads(t, filepath.Join(dir, "audio-trace", "timeline.jsonl"))
}

// decodedPayloads decodes the base64 payload of every JSONL record in path.
func decodedPayloads(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var payloads []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		var record struct {
			Payload string `json:"payload"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode %s record %q: %v", path, line, err)
		}
		payload, err := base64.StdEncoding.DecodeString(record.Payload)
		if err != nil {
			t.Fatalf("decode %s payload: %v", path, err)
		}
		payloads = append(payloads, string(payload))
	}
	return payloads
}

// delegationToolRecord returns the first record of the given delegation
// tool event kind.
func delegationToolRecord(records []string, kind string) string {
	for _, record := range records {
		if strings.Contains(record, `"`+kind+`"`) {
			return record
		}
	}
	return ""
}
