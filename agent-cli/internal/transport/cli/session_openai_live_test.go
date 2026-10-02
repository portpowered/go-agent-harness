package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	live "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/fakelive"
)

// TestSessionCommandRunsOpenAILiveAgainstTheFakeServer runs
// `session --provider openai-live --model gpt-live-1 --audio-in <file>`
// in process against the fake GPT-Live server. The session authenticates
// with the model.openai API key, streams the file as session.input_audio.append,
// renders the assistant's speech segment, and ends with the session.close
// handshake. It runs on the host clock (the composed CLI has no virtual
// clock), but nothing waits on a timer: the fake ends the first segment with
// a server-timeline gap and answers session.close at once.
func TestSessionCommandRunsOpenAILiveAgainstTheFakeServer(t *testing.T) {
	const apiKey = "sk-live-cli"
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	configYAML := "model:\n  provider: openai\n  openai:\n    model: gpt-realtime\n    api_key: " + apiKey + "\n"
	if err := os.WriteFile(filepath.Join(configDir, config.ConfigFileName), []byte(configYAML), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	audioIn := filepath.Join(root, "in.pcm")
	if err := os.WriteFile(audioIn, bytes.Repeat([]byte{0x10, 0x27, 0xf0, 0xd8}, 1200), 0o600); err != nil {
		t.Fatalf("write audio-in: %v", err)
	}
	fake := fakelive.New(fakelive.WithAPIKey(apiKey), fakelive.WithScript(
		fakelive.AwaitClient(live.TypeInputAudioAppend, 1),
		fakelive.Send(
			live.OutputAudioDelta{Delta: base64.StdEncoding.EncodeToString(make([]byte, 480))},
			live.OutputTranscriptDelta{Delta: "Hello from GPT-Live.", StartMS: 0, EndMS: 500},
			live.OutputTranscriptDelta{Delta: "Anything else?", StartMS: 1500, EndMS: 1800},
		),
	))

	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = configDir
	command := newTestSessionCommand(flags.NewAskFlags(), globalFlags, testSessionDeps{Dialer: fake.Dialer()}).Generate()
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"--provider", config.ProviderOpenAILive, "--model", live.Model1, "--audio-in", audioIn})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err := command.ExecuteContext(ctx); err != nil {
		t.Fatalf("session --provider openai-live: %v\nstdout=%q\nstderr=%q\nfake errors=%v", err, stdout.String(), stderr.String(), fake.Errors())
	}

	if !strings.Contains(stdout.String(), "Hello from GPT-Live.") {
		t.Fatalf("stdout = %q, want the assistant transcript", stdout.String())
	}
	calls := fake.DialCalls()
	if len(calls) != 1 || calls[0].Endpoint != live.DefaultEndpoint || calls[0].Headers["Authorization"] != "Bearer "+apiKey {
		t.Fatalf("dial calls = %+v, want one API-key dial of the live endpoint", calls)
	}
	events := fake.ClientEvents()
	start, ok := events[0].(live.SessionStart)
	if !ok || start.Session.Model != live.Model1 || start.Session.Audio.Format.Rate != live.RatePCM24k {
		t.Fatalf("first client event = %#v, want session.start for gpt-live-1 at 24 kHz", events[0])
	}
	if _, ok := events[len(events)-1].(live.SessionClose); !ok {
		var types []string
		for _, e := range events {
			types = append(types, e.EventType())
		}
		t.Fatalf("client events = %v, want the session.close handshake last", types)
	}
	if len(fake.InputAudio()) == 0 {
		t.Fatal("the fake received no input audio")
	}
	if errs := fake.Errors(); len(errs) != 0 {
		t.Fatalf("fake server errors: %v", errs)
	}
}
