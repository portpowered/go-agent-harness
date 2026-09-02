package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

// TestIsRecordOnlyLiveInvocationMatrix pins the operator's flagship-path
// regression: `agent session --record <file>.json` alone (no --prompt, no
// --audio-in, no --image, no browser flag) must be recognized as an
// otherwise-bare live microphone conversation. sessionModeFlagNames
// deliberately keeps --record in the explicit-mode list (so it still routes
// to real capture-recording setup instead of a bare live session that never
// wraps a recorder), but isRecordOnlyLiveInvocation is the narrower signal
// used to restore bare mode's implicit devices and keep-open semantics on
// top of that.
func TestIsRecordOnlyLiveInvocationMatrix(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "record alone", args: []string{"--record", "cap.json"}, want: true},
		{name: "bare invocation has no record flag", args: nil, want: false},
		{name: "record-dir alone is not record-only-live", args: []string{"--record-dir", "dir"}, want: false},
		{name: "replay alone is not record-only-live", args: []string{"--replay", "cap.json"}, want: false},
		{name: "record with prompt is a scripted exchange", args: []string{"--record", "cap.json", "--prompt", "hi"}, want: false},
		{name: "record with audio-in is a scripted exchange", args: []string{"--record", "cap.json", "--audio-in", "in.wav"}, want: false},
		{name: "record with image is a scripted exchange", args: []string{"--record", "cap.json", "--image", "photo.png"}, want: false},
		{name: "record with browser-tools remains interactive", args: []string{"--record", "cap.json", "--browser-tools", "webmcp"}, want: true},
		{name: "record with external browser remains interactive", args: []string{"--record", "cap.json", "--browser-tools", "webmcp", "--browser-cdp-url", "http://127.0.0.1:9222", "--browser-auto-select", "single"}, want: true},
		{name: "record with positional prompt words", args: []string{"--record", "cap.json", "do", "the", "thing"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			command := NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, nil).Generate()
			if err := command.ParseFlags(tt.args); err != nil {
				t.Fatalf("parse flags %v: %v", tt.args, err)
			}
			got := isRecordOnlyLiveInvocation(command, command.Flags().Args(), nil)
			if got != tt.want {
				t.Fatalf("isRecordOnlyLiveInvocation(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

// TestSessionRecordOnlyLiveOpensDevicesAndDoesNotSelfClose pins the exact
// regression the operator hit: `agent session --model X --record test5.json`
// used to run for milliseconds and stop after two capture records, because
// --record made the invocation non-bare and that silently dropped both the
// implicit microphone/speaker devices and the "stay open for the
// conversation" semantics bare mode gets. This drives the CLI end to end
// (through Generate/Execute, not the services package directly) with an
// injected provider session and a virtual device registry so it never
// touches real hardware or a live network, and asserts both halves of the
// fix: the shared microphone and speaker are opened, and the session does
// not send a close the instant it opens.
func TestSessionRecordOnlyLiveOpensDevicesAndDoesNotSelfClose(t *testing.T) {
	configDir := t.TempDir()
	configYAML := `
model:
  provider: grok
  grok:
    model: grok-realtime-test
    api_key: test-key
`
	if err := os.WriteFile(filepath.Join(configDir, config.ConfigFileName), []byte(configYAML), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = configDir

	registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("new virtual device registry: %v", err)
	}

	inferencer := newRecordOnlyLiveInferencer()
	owner := NewSessionCommandWithDeviceRegistry(flags.NewAskFlags(), globalFlags, nil, inferencer, registry)
	command := owner.Generate()
	command.SetOut(io.Discard)
	recordPath := filepath.Join(t.TempDir(), "capture.json")
	command.SetArgs([]string{"--record", recordPath})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- command.ExecuteContext(ctx) }()

	select {
	case <-inferencer.opened:
	case <-time.After(3 * time.Second):
		t.Fatal("record-only-live session never connected to the provider")
	}

	select {
	case <-inferencer.session.closeRequested:
		t.Fatal("record-only-live session sent a close immediately after opening; want it to stay open like a bare interactive conversation")
	case <-time.After(300 * time.Millisecond):
	}

	if observations := registry.Observations(); observations.OpenCount != 2 {
		t.Fatalf("device observations = %+v, want the implicit microphone and speaker both opened", observations)
	}

	inferencer.endFromProvider(ctx)
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("record-only-live session command: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("record-only-live session did not return after the provider-driven close")
	}
}

// TestSessionRecordedWebMCPExternalBrowserStaysInteractive reproduces the
// eac18 command boundary. --record is a side capture, so adding it to an
// interactive external-CDP WebMCP session must not remove the default audio
// devices or send client_close immediately after session.open.
func TestSessionRecordedWebMCPExternalBrowserStaysInteractive(t *testing.T) {
	configDir := t.TempDir()
	configYAML := `
model:
  provider: openai
  openai:
    model: gpt-realtime
    api_key: test-key
`
	if err := os.WriteFile(filepath.Join(configDir, config.ConfigFileName), []byte(configYAML), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = configDir

	registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("new virtual device registry: %v", err)
	}

	inferencer := newRecordOnlyLiveInferencer()
	owner := NewSessionCommandWithDeviceRegistry(flags.NewAskFlags(), globalFlags, nil, inferencer, registry)
	command := owner.Generate()
	command.SetOut(io.Discard)
	recordPath := filepath.Join(t.TempDir(), "eac18.json")
	command.SetArgs([]string{
		"--browser-tools", "webmcp",
		"--browser-cdp-url", "http://127.0.0.1:9222",
		"--browser-auto-select", "single",
		"--record", recordPath,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- command.ExecuteContext(ctx) }()

	select {
	case <-inferencer.opened:
	case <-ctx.Done():
		t.Fatal("recorded WebMCP session never connected to the provider")
	}

	select {
	case <-inferencer.session.closeRequested:
		t.Fatal("recorded WebMCP session sent client_close immediately after opening")
	case <-time.After(300 * time.Millisecond):
	}

	if observations := registry.Observations(); observations.OpenCount != 2 {
		t.Fatalf("device observations = %+v, want the implicit microphone and speaker both opened", observations)
	}

	inferencer.endFromProvider(ctx)
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("recorded WebMCP session command: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("recorded WebMCP session did not return after provider close")
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
func (s *recordOnlyLiveSession) RTCMedia() rtc.MediaEndpoints {
	return rtc.MediaEndpoints{Inbound: s.media, Outbound: s.media}
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
