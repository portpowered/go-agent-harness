package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai/chatgptauth"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexlive"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexrtc/fakecodex"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/quicksilver"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openaichatgpt/fakechatgpt"
)

// TestSessionCommandAnswersCodexRouteDelegationsOnTheChatGPTLogin is the
// gpt-live-1-codex route of the delegation test: one ChatGPT login signs both
// the voice call and the delegation backend. The codex route's delegation
// carries task text; the backend (openai-chatgpt, the account's default
// model) calls the session's lookup_order tool, and the fake ChatGPT voice
// backend receives the answer as a delegation.context.append on the
// speakable channel. It runs on the host clock: the route's pion peer cannot
// join a synctest bubble.
func TestSessionCommandAnswersCodexRouteDelegationsOnTheChatGPTLogin(t *testing.T) {
	const delegationID = "del_codex"
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := chatgptauth.NewFileStore(config.ChatGPTAuthStorePath(configDir)).Save(chatgptauth.Credential{
		AccessToken: fakecodex.DefaultToken, RefreshToken: "refresh", AccountID: fakecodex.DefaultAccountID,
		ExpiresAt: time.Now().Add(time.Hour), LastRefresh: time.Now(),
	}); err != nil {
		t.Fatalf("save the fake login: %v", err)
	}
	reasoning := fakechatgpt.New(fakecodex.DefaultToken, fakecodex.DefaultAccountID)
	reasoning.SetModels(`{"models":[{"slug":"gpt-account-default","visibility":"list","priority":1}]}`)
	reasoning.Enqueue(
		fakechatgpt.ToolCallReply("call_order", "lookup_order", `{"order":"42"}`),
		fakechatgpt.TextReply("Order 42 ships tomorrow."),
	)
	reasoningServer := httptest.NewServer(reasoning)
	t.Cleanup(reasoningServer.Close)
	configYAML := "model:\n  provider: openai\n  openai_chatgpt:\n    base_url: " + reasoningServer.URL + "/backend-api/codex\n"
	if err := os.WriteFile(filepath.Join(configDir, config.ConfigFileName), []byte(configYAML), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	audioIn := filepath.Join(root, "in.pcm")
	if err := os.WriteFile(audioIn, bytes.Repeat([]byte{0x10, 0x27, 0xf0, 0xd8}, 4800), 0o600); err != nil {
		t.Fatalf("write audio-in: %v", err)
	}
	network, err := fakecodex.NewVirtualNetwork()
	if err != nil {
		t.Fatal(err)
	}
	voice := fakecodex.New(fakecodex.WithPeerNetwork(network))
	voiceServer := httptest.NewServer(voice)
	t.Cleanup(func() {
		voiceServer.Close()
		if err := errors.Join(voice.Close(), network.Close()); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	answered := make(chan quicksilver.DelegationContextAppend, 1)
	go delegateAfterFirstTurn(t, ctx, voice, delegationID, answered)

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
		Capabilities: SessionToolCapabilitiesFactory(capabilities),
		CodexTransport: &codexlive.Transport{
			BackendURL:      voiceServer.URL + "/backend-api/codex",
			SidebandBaseURL: "ws" + strings.TrimPrefix(voiceServer.URL, "http") + "/v1/live",
			HTTPClient:      voiceServer.Client(),
			PeerSettings:    network.Client,
		},
	}).Generate()
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"--provider", config.ProviderOpenAILive, "--model", config.OpenAILiveChatGPTModel, "--audio-in", audioIn})
	if err := command.ExecuteContext(ctx); err != nil {
		t.Fatalf("session --model gpt-live-1-codex: %v\nstdout=%q\nstderr=%q\nvoice errors=%v", err, stdout.String(), stderr.String(), voice.Errors())
	}

	if calls := tool.snapshot(); len(calls) != 1 || calls[0].Name != "lookup_order" {
		t.Fatalf("tool calls = %+v, want one lookup_order", calls)
	}
	select {
	case got := <-answered:
		if got.DelegationItemID != delegationID || got.Channel != quicksilver.ChannelSpeakable || len(got.Content) != 1 || got.Content[0].Text != "Order 42 ships tomorrow." {
			t.Fatalf("answer = %+v, want the backend's result on the speakable channel", got)
		}
	default:
		t.Fatal("the voice backend received no delegation answer")
	}
	assertBackendUsedTheLogin(t, reasoning, fakecodex.DefaultToken)
	if first := reasoning.Requests(); len(first) == 0 || !strings.Contains(string(first[len(first)-1].Body), "Look up order 42.") {
		t.Fatal("the backend task does not carry the codex delegation's task text")
	}
	if errs := voice.Errors(); len(errs) != 0 {
		t.Fatalf("voice backend errors: %v", errs)
	}
}

// delegateAfterFirstTurn waits for the caller's audio turn, delegates the
// work with task text, waits for the speakable answer, and only then speaks,
// which ends the finite session.
func delegateAfterFirstTurn(t *testing.T, ctx context.Context, voice *fakecodex.Backend, id string, answered chan<- quicksilver.DelegationContextAppend) {
	t.Helper()
	if err := voice.WaitSidebands(ctx, 1); err != nil {
		t.Errorf("wait for the sideband: %v", err)
		return
	}
	for range inputFrames {
		if _, err := voice.Peer().ReadFrame(ctx); err != nil {
			t.Errorf("read the caller's audio: %v", err)
			return
		}
	}
	if err := voice.Send(
		quicksilver.InputTranscriptAdded{Item: quicksilver.TranscriptItem{Text: "Where is order forty-two?"}},
		quicksilver.DelegationCreated{Item: quicksilver.DelegationItem{
			ID: id, Type: quicksilver.ItemTypeDelegation, Target: quicksilver.TargetClient,
			Content: []quicksilver.ContentPart{{Type: quicksilver.PartInputText, Text: "Look up order 42."}},
		}},
	); err != nil {
		t.Errorf("delegate: %v", err)
		return
	}
	for seen := 1; ; seen++ {
		events, err := voice.WaitClientEvents(ctx, seen)
		if err != nil {
			t.Errorf("wait for the delegation answer: %v", err)
			return
		}
		if appended, ok := events[seen-1].(quicksilver.DelegationContextAppend); ok && appended.Channel == quicksilver.ChannelSpeakable {
			answered <- appended
			break
		}
	}
	if err := voice.Send(quicksilver.OutputTranscriptAdded{Item: quicksilver.TranscriptItem{Text: "Your order ships tomorrow."}}); err != nil {
		t.Errorf("speak the answer: %v", err)
	}
}
