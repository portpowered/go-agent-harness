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
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai/chatgptauth"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexlive"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexrtc"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexrtc/fakecodex"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/quicksilver"
)

// TestSessionCommandRunsGPTLiveCodexOnAChatGPTLogin runs
// `session --provider openai-live --model gpt-live-1-codex --audio-in <file>`
// in process with a ChatGPT login in the config directory and no API key.
// The fake ChatGPT backend answers the WebRTC call over a virtual network;
// the file's audio reaches its peer as Opus, its spoken turn reaches the
// transcript, and the session ends with the session.close handshake. It runs
// on the host clock (the composed CLI has no virtual clock).
func TestSessionCommandRunsGPTLiveCodexOnAChatGPTLogin(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := chatgptauth.NewFileStore(config.ChatGPTAuthStorePath(configDir)).Save(chatgptauth.Credential{
		AccessToken: fakecodex.DefaultToken, RefreshToken: "refresh", AccountID: fakecodex.DefaultAccountID,
		ExpiresAt: time.Now().Add(time.Hour), LastRefresh: time.Now(),
	}); err != nil {
		t.Fatalf("save the fake login: %v", err)
	}
	audioIn := filepath.Join(root, "in.pcm")
	if err := os.WriteFile(audioIn, bytes.Repeat([]byte{0x10, 0x27, 0xf0, 0xd8}, 4800), 0o600); err != nil {
		t.Fatalf("write audio-in: %v", err)
	}
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
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	go answerFirstTurn(t, ctx, backend)

	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = configDir
	command := newTestSessionCommand(flags.NewAskFlags(), globalFlags, testSessionDeps{CodexTransport: &codexlive.Transport{
		BackendURL:      server.URL + "/backend-api/codex",
		SidebandBaseURL: "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/live",
		HTTPClient:      server.Client(),
		PeerSettings:    network.Client,
	}}).Generate()
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"--provider", config.ProviderOpenAILive, "--model", config.OpenAILiveChatGPTModel, "--audio-in", audioIn})
	if err := command.ExecuteContext(ctx); err != nil {
		t.Fatalf("session --provider openai-live --model gpt-live-1-codex: %v\nstdout=%q\nstderr=%q\nbackend errors=%v", err, stdout.String(), stderr.String(), backend.Errors())
	}

	if !strings.Contains(stdout.String(), "Hello from GPT-Live on ChatGPT.") {
		t.Fatalf("stdout = %q, want the assistant transcript", stdout.String())
	}
	calls := backend.Calls()
	if len(calls) != 1 || calls[0].Session.Model != quicksilver.ModelCodex || calls[0].Header.Get(codexrtc.HeaderVersion) == "" {
		t.Fatalf("calls = %+v, want one gpt-live-1-codex call with the yui version", calls)
	}
	events := backend.ClientEvents()
	if len(events) == 0 {
		t.Fatal("the backend received no client event")
	}
	if _, ok := events[len(events)-1].(quicksilver.SessionClose); !ok {
		t.Fatalf("last client event = %T, want the session.close handshake", events[len(events)-1])
	}
	if errs := backend.Errors(); len(errs) != 0 {
		t.Fatalf("backend errors: %v", errs)
	}
}

// inputFrames is how many 20 ms peer frames the audio-in file makes: 0.4 s.
// The last one leaves only when the turn ends, padded with silence, so the
// answer comes after the turn is committed.
const inputFrames = 20

// answerFirstTurn waits for the caller's whole audio turn at the backend's
// peer, then speaks on the sideband. It sends no turn.done: the segment
// closes after the quiet gap, as in the gpt-live-1 CLI test, by which time
// the CLI has marked its finite input complete.
func answerFirstTurn(t *testing.T, ctx context.Context, backend *fakecodex.Backend) {
	t.Helper()
	if err := backend.WaitSidebands(ctx, 1); err != nil {
		t.Errorf("wait for the sideband: %v", err)
		return
	}
	for range inputFrames {
		if _, err := backend.Peer().ReadFrame(ctx); err != nil {
			t.Errorf("read the caller's audio: %v", err)
			return
		}
	}
	if err := backend.Send(quicksilver.OutputTranscriptAdded{Item: quicksilver.TranscriptItem{Text: "Hello from GPT-Live on ChatGPT."}}); err != nil {
		t.Errorf("speak the turn: %v", err)
	}
}
