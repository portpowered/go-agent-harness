package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

// TestIsPassiveLiveInvocationMatrix pins the operator's flagship-path
// regression: `agent session --record <file>.json` alone (no --prompt, no
// --audio-in, no --image, no browser flag) must be recognized as an
// otherwise-bare live microphone conversation. sessionModeFlagNames
// deliberately keeps --record in the explicit-mode list (so it still routes
// to real capture-recording setup instead of a bare live session that never
// wraps a recorder), but isPassiveLiveInvocation is the narrower signal
// used to restore bare mode's implicit devices and keep-open semantics on
// top of that.
func TestIsPassiveLiveInvocationMatrix(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "record alone", args: []string{"--record", "cap.json"}, want: true},
		{name: "audio-out alone", args: []string{"--audio-out", "device.wav"}, want: true},
		{name: "trace-audio alone", args: []string{"--trace-audio"}, want: true},
		{name: "trace-audio default directory", args: []string{"--trace-audio"}, want: true},
		{name: "record audio-out and trace", args: []string{"--record", "cap.json", "--audio-out", "device.wav", "--trace-audio"}, want: true},
		{name: "audio-out with WebMCP Cast", args: []string{"--audio-out", "device.wav", "--audio-out-device", "default", "--browser-tools", "webmcp", "--web-cast"}, want: true},
		{name: "bare invocation has no record flag", args: nil, want: false},
		{name: "record-dir alone is not record-only-live", args: []string{"--record-dir", "dir"}, want: false},
		{name: "replay alone is not record-only-live", args: []string{"--replay", "cap.json"}, want: false},
		{name: "record with prompt is a scripted exchange", args: []string{"--record", "cap.json", "--prompt", "hi"}, want: false},
		{name: "record with audio-in is a scripted exchange", args: []string{"--record", "cap.json", "--audio-in", "in.wav"}, want: false},
		{name: "trace with prompt is a scripted exchange", args: []string{"--trace-audio", "--prompt", "hi"}, want: false},
		{name: "record with image is a scripted exchange", args: []string{"--record", "cap.json", "--image", "photo.png"}, want: false},
		{name: "record with browser-tools remains interactive", args: []string{"--record", "cap.json", "--browser-tools", "webmcp"}, want: true},
		{name: "record with device WAV and WebMCP Cast remains interactive", args: []string{"--record", "cap.json", "--audio-out", "device.wav", "--audio-out-device", "default", "--browser-tools", "webmcp", "--web-cast"}, want: true},
		{name: "record with external browser remains interactive", args: []string{"--record", "cap.json", "--browser-tools", "webmcp", "--browser-cdp-url", testCDPURL, "--browser-auto-select", "single"}, want: true},
		{name: "record with positional prompt words", args: []string{"--record", "cap.json", "do", "the", "thing"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			command := newTestSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), testSessionDeps{}).Generate()
			if err := command.ParseFlags(tt.args); err != nil {
				t.Fatalf("parse flags %v: %v", tt.args, err)
			}
			got := isPassiveLiveInvocation(command, command.Flags().Args(), nil)
			if got != tt.want {
				t.Fatalf("isPassiveLiveInvocation(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

// TestSessionPassiveLiveInvocationsOpenDevicesAndStayInteractive drives the
// CLI end to end (through Generate/Execute, not the services package
// directly) with an injected provider session and a virtual device registry,
// so it never touches real hardware or a live network. Each case asserts that
// the implicit microphone and speaker are opened and that the session does
// not send a close right after it opens. The cases run in parallel so their
// "stays open" observation windows overlap.
//
//   - record-only: the exact regression the operator hit. `agent session
//     --model X --record test5.json` used to run for milliseconds and stop
//     after two capture records, because --record made the invocation
//     non-bare and silently dropped both the implicit devices and the "stay
//     open for the conversation" semantics bare mode gets.
//   - recorded-webmcp-cast: the test64 command boundary. --record and
//     --audio-out are passive captures, so adding both to an interactive
//     WebMCP Cast session must not remove the audio devices or send
//     client_close immediately after session.open.
//   - unrecorded-webmcp-cast: the operator's exact command shape. --audio-out
//     is a passive copy of the audio already headed to the selected device,
//     not an input that turns an interactive browser session into a finite
//     scripted exchange; omitting --record must not remove the implicit
//     microphone or close the client immediately after session.open.
func TestSessionPassiveLiveInvocationsOpenDevicesAndStayInteractive(t *testing.T) {
	grokConfig := `
model:
  provider: grok
  grok:
    model: grok-realtime-test
    api_key: test-key
`
	openAIConfig := func(model string) string {
		return `
model:
  provider: openai
  openai:
    model: ` + model + `
    api_key: test-key
`
	}
	tests := []struct {
		name       string
		configYAML string
		args       func(dir string) []string
	}{
		{
			name:       "record-only",
			configYAML: grokConfig,
			args: func(dir string) []string {
				return []string{"--record", filepath.Join(dir, "capture.json")}
			},
		},
		{
			name:       "recorded-webmcp-cast",
			configYAML: openAIConfig("gpt-realtime"),
			args: func(dir string) []string {
				return []string{
					"--browser-tools", "webmcp",
					"--web-cast",
					"--browser-cdp-url", testCDPURL,
					"--browser-auto-select", "single",
					"--audio-out-device", "default",
					"--audio-out", filepath.Join(dir, "test64-device.wav"),
					"--record", filepath.Join(dir, "test64.json"),
				}
			},
		},
		{
			name:       "unrecorded-webmcp-cast",
			configYAML: openAIConfig("gpt-realtime-2.1"),
			args: func(dir string) []string {
				return []string{
					"--model", "gpt-realtime-2.1",
					"--browser-tools", "webmcp",
					"--web-cast",
					"--audio-out-device", "default",
					"--audio-out", filepath.Join(dir, "32.wav"),
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runPassiveLiveSessionStaysInteractive(t, tt.configYAML, tt.args(t.TempDir()))
		})
	}
}

func runPassiveLiveSessionStaysInteractive(t *testing.T, configYAML string, args []string) {
	t.Helper()
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, config.ConfigFileName), []byte(configYAML), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = configDir

	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("new virtual device registry: %v", err)
	}

	inferencer := newRecordOnlyLiveInferencer()
	owner := newTestLiveSessionCommand(flags.NewAskFlags(), globalFlags, inferencer, registry)
	command := owner.Generate()
	command.SetOut(io.Discard)
	command.SetArgs(args)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- command.ExecuteContext(ctx) }()

	select {
	case <-inferencer.opened:
	case <-ctx.Done():
		t.Fatal("passive live session never connected to the provider")
	}

	select {
	case <-inferencer.session.closeRequested:
		t.Fatal("passive live session sent a close immediately after opening; want it to stay open like a bare interactive conversation")
	case err := <-runErr:
		t.Fatalf("passive live session returned before provider close: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	if observations := registry.Observations(); observations.OpenCount != 2 {
		t.Fatalf("device observations = %+v, want the implicit microphone and speaker both opened", observations)
	}

	inferencer.endFromProvider(ctx)
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("passive live session command: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("passive live session did not return after the provider-driven close")
	}
}

// recordOnlyLiveSession is a minimal messages.Session double: it opens
// immediately, tracks whether the caller ever asked it to close, and only
// terminates when the test simulates a provider-driven close.
type recordOnlyLiveSession struct {
	receive        *messages.TypedBuffer[messages.StreamMessage]
	done           chan struct{}
	closeRequested chan struct{}
	media          *browserAdmissionMedia
	requestOnce    sync.Once
	closeOnce      sync.Once
}

// RTCMedia satisfies rtc.MediaSession so the device-binding wrapper accepts
// this double once --record's implicit device request is present. The
// microphone/speaker pumps it drives are a separate, already-exercised path;
// this test only needs them to exist, not to carry real audio.
func (s *recordOnlyLiveSession) RTCMedia() sharedaudio.MediaEndpoints {
	return sharedaudio.MediaEndpoints{Inbound: s.media, Outbound: s.media}
}

func (s *recordOnlyLiveSession) Send(_ context.Context, msg messages.StreamMessage) bool {
	if msg.Type == messages.StreamTypeSessionClose {
		s.requestOnce.Do(func() { close(s.closeRequested) })
	}
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

func (s *recordOnlyLiveSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *recordOnlyLiveSession) Done() <-chan struct{} { return s.done }

func (s *recordOnlyLiveSession) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return s.media.Close()
}

type recordOnlyLiveInferencer struct {
	opened     chan struct{}
	openedOnce sync.Once
	session    *recordOnlyLiveSession
}

func newRecordOnlyLiveInferencer() *recordOnlyLiveInferencer {
	return &recordOnlyLiveInferencer{opened: make(chan struct{})}
}

func (i *recordOnlyLiveInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session := &recordOnlyLiveSession{
		receive:        messages.NewTypedBuffer[messages.StreamMessage](16),
		done:           make(chan struct{}),
		closeRequested: make(chan struct{}),
		media:          newBrowserAdmissionMedia(),
	}
	i.session = session
	session.receive.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("record-only-live-session", "test"),
	})
	i.openedOnce.Do(func() { close(i.opened) })
	return session, nil
}

// endFromProvider simulates the far side hanging up, the way a real session
// eventually ends: it is what lets this test return control to the command
// after it has verified the session stayed open.
func (i *recordOnlyLiveInferencer) endFromProvider(ctx context.Context) {
	i.session.receive.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValue("record-only-live-session", "test complete"),
	})
}
