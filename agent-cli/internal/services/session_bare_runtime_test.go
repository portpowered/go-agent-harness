package services_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestBareSessionCommandUsesRegistryDefaultsAndReportsListening(t *testing.T) {
	registry := newRTCDeviceRoundtripRegistry(t)
	peer := newLoopbackRTCTrackPeer(1)
	inferencer := newRuntimeRTCSessionInferencer(peer)
	t.Cleanup(func() {
		if session := inferencer.sessionValue(); session != nil {
			_ = session.Close()
		}
		_ = peer.Close()
	})

	t.Setenv("OPENAI_API_KEY", "bare-test-key")
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	command := cli.NewSessionCommandWithDeviceRegistry(flags.NewAskFlags(), globalFlags, nil, inferencer, registry).Generate()
	var output synchronizedBareSessionOutput
	command.SetOut(&output)
	command.SetArgs([]string{})

	runErr := make(chan error, 1)
	go func() { runErr <- command.ExecuteContext(context.Background()) }()

	var session *runtimeRTCSession
	select {
	case session = <-inferencer.connected:
	case <-time.After(2 * time.Second):
		t.Fatal("bare session did not connect")
	}
	if got := registry.Observations(); got.OpenCount != 2 || got.ReleaseCount != 0 {
		t.Fatalf("bare device observations before session.created = %+v, want two opens and no releases", got)
	}
	if strings.Contains(output.String(), "Listening:") {
		t.Fatalf("bare session reported listening before session.created: %q", output.String())
	}
	if !session.recv.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeSessionCreated,
		Value: messages.NewSessionCreatedValue("bare-session", "gpt-realtime-2.1-mini"),
	}) {
		t.Fatal("bare session did not accept session.created")
	}
	waitForBareSessionOutput(t, &output, "Listening:")
	text := output.String()
	for _, want := range []string{
		"provider=openai",
		"model=gpt-realtime-2.1-mini",
		"transport=ws",
		"input-device=virtual:mic-in",
		"output-device=virtual:speaker-out",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("bare startup output missing %q: %q", want, text)
		}
	}
	if strings.Contains(text, "bare-test-key") {
		t.Fatalf("bare startup output leaked the credential: %q", text)
	}

	session.finish()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("bare session after provider close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bare session did not finish after provider close")
	}
	if got := registry.Observations(); got.OpenCount != 2 || got.ReleaseCount != 2 {
		t.Fatalf("bare device observations after close = %+v, want two opens and two releases", got)
	}
}

func TestBareSessionCommandSIGINTClosesProviderAndDevicesOnce(t *testing.T) {
	registry := newRTCDeviceRoundtripRegistry(t)
	peer := newLoopbackRTCTrackPeer(1)
	inferencer := newRuntimeRTCSessionInferencer(peer)
	t.Cleanup(func() { _ = peer.Close() })

	t.Setenv("OPENAI_API_KEY", "bare-test-key")
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	command := cli.NewSessionCommandWithDeviceRegistry(flags.NewAskFlags(), globalFlags, nil, inferencer, registry).Generate()
	var output synchronizedBareSessionOutput
	command.SetOut(&output)
	command.SetArgs([]string{})

	runErr := make(chan error, 1)
	go func() { runErr <- command.ExecuteContext(context.Background()) }()

	var session *runtimeRTCSession
	select {
	case session = <-inferencer.connected:
	case <-time.After(2 * time.Second):
		t.Fatal("bare session did not connect before SIGINT")
	}
	if !session.recv.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeSessionCreated,
		Value: messages.NewSessionCreatedValue("bare-sigint-session", "gpt-realtime-2.1-mini"),
	}) {
		t.Fatal("bare SIGINT session did not accept session.created")
	}
	waitForBareSessionOutput(t, &output, "Listening:")

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("find test process: %v", err)
	}
	if err := process.Signal(os.Interrupt); err != nil {
		t.Fatalf("send SIGINT to bare session: %v", err)
	}

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("bare session SIGINT error = %v, want clean cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bare session did not terminate after one SIGINT")
	}
	if got := session.closeCalls.Load(); got != 1 {
		t.Fatalf("provider session close calls = %d, want exactly one", got)
	}
	if got := registry.Observations(); got.OpenCount != 2 || got.ReleaseCount != 2 {
		t.Fatalf("bare SIGINT device observations = %+v, want two opens and two releases", got)
	}
	text := output.String()
	if strings.Count(text, "[session terminal:") != 1 || !strings.Contains(text, "classification=user_cancelled") {
		t.Fatalf("bare SIGINT output = %q, want one user-cancelled terminal", text)
	}
}

func TestBareSessionCommandSIGINTDuringProviderConnectReleasesDevices(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	registry := newRTCDeviceRoundtripRegistry(t)
	inferencer := &bareConnectBlockingInferencer{started: make(chan struct{})}

	t.Setenv("OPENAI_API_KEY", "bare-test-key")
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	command := cli.NewSessionCommandWithDeviceRegistry(flags.NewAskFlags(), globalFlags, nil, inferencer, registry).Generate()
	command.SetOut(&bytes.Buffer{})
	command.SetArgs([]string{})

	runErr := make(chan error, 1)
	go func() { runErr <- command.ExecuteContext(context.Background()) }()
	select {
	case <-inferencer.started:
	case <-time.After(2 * time.Second):
		t.Fatal("bare provider connect did not start before SIGINT")
	}

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("find test process: %v", err)
	}
	if err := process.Signal(os.Interrupt); err != nil {
		t.Fatalf("send SIGINT during bare provider connect: %v", err)
	}

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("bare connect SIGINT error = %v, want clean cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bare provider connect did not terminate after one SIGINT")
	}
	if got := registry.Observations(); got.OpenCount != 2 || got.ReleaseCount != 2 {
		t.Fatalf("bare connect SIGINT device observations = %+v, want two opens and two releases", got)
	}
	assertGoroutinesSettled(t, baselineGoroutines, "bare connect SIGINT")
}

func TestBareSessionSetupFailureIsNotReportedAsProviderSuccess(t *testing.T) {
	wantErr := errors.New("provider setup failed")
	registry := newRTCDeviceRoundtripRegistry(t)
	inferencer := &bareFailingInferencer{err: wantErr}

	t.Setenv("OPENAI_API_KEY", "bare-test-key")
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	var output bytes.Buffer
	command := cli.NewSessionCommandWithDeviceRegistry(flags.NewAskFlags(), globalFlags, nil, inferencer, registry).Generate()
	command.SetOut(&output)
	command.SetArgs([]string{})

	err := command.ExecuteContext(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("bare setup error = %v, want provider setup failure", err)
	}
	if strings.Contains(output.String(), "provider_closed") || strings.Contains(output.String(), "classification=user_cancelled") {
		t.Fatalf("bare setup failure reported a successful/clean provider lifecycle: %q", output.String())
	}
	if got := registry.Observations(); got.OpenCount != 2 || got.ReleaseCount != 2 {
		t.Fatalf("bare setup failure device observations = %+v, want two opens and two releases", got)
	}
}

func TestBareSessionMissingKeyStopsBeforeDeviceOrProviderSetup(t *testing.T) {
	registry := newRTCDeviceRoundtripRegistry(t)
	peer := newLoopbackRTCTrackPeer(1)
	inferencer := newRuntimeRTCSessionInferencer(peer)
	t.Cleanup(func() { _ = peer.Close() })

	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("AGENT_MODEL__OPENAI__API_KEY", "")
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	command := cli.NewSessionCommandWithDeviceRegistry(flags.NewAskFlags(), globalFlags, nil, inferencer, registry).Generate()
	command.SetOut(&bytes.Buffer{})
	command.SetArgs([]string{})

	err := command.ExecuteContext(context.Background())
	var credentialErr *services.BareSessionCredentialError
	if !errors.As(err, &credentialErr) || !errors.Is(err, services.ErrBareSessionCredentialMissing) {
		t.Fatalf("bare missing-key error = %T %v, want typed credential error", err, err)
	}
	if strings.Count(err.Error(), "API key") != 1 || strings.Contains(err.Error(), "bare-test-key") {
		t.Fatalf("bare missing-key error is not one redacted actionable message: %q", err)
	}
	if inferencer.sessionValue() != nil {
		t.Fatal("bare missing-key path connected a provider session")
	}
	if got := registry.Observations(); got.OpenCount != 0 || got.ReleaseCount != 0 {
		t.Fatalf("bare missing-key device observations = %+v, want no acquisition", got)
	}
}

func TestBareSessionMissingOutputDefaultReleasesInputBeforeProviderSetup(t *testing.T) {
	registry, err := audio.NewVirtualRegistry(audio.VirtualBackendConfig{
		Devices: []audio.VirtualDeviceConfig{
			{ID: "input", Name: "Virtual Input", Direction: audio.DirectionInput},
		},
		Defaults: map[audio.Direction]string{audio.DirectionInput: "input"},
	})
	if err != nil {
		t.Fatalf("new input-only virtual registry: %v", err)
	}
	peer := newLoopbackRTCTrackPeer(1)
	inferencer := newRuntimeRTCSessionInferencer(peer)
	t.Cleanup(func() { _ = peer.Close() })

	t.Setenv("OPENAI_API_KEY", "bare-test-key")
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	command := cli.NewSessionCommandWithDeviceRegistry(flags.NewAskFlags(), globalFlags, nil, inferencer, registry).Generate()
	command.SetOut(&bytes.Buffer{})
	command.SetArgs([]string{})

	err = command.ExecuteContext(context.Background())
	if err == nil || !errors.Is(err, audio.ErrNoDefaultDevice) {
		t.Fatalf("bare missing-output-default error = %v, want no-default error", err)
	}
	var bindingErr *services.RTCDeviceBindingError
	if !errors.As(err, &bindingErr) || bindingErr.Flag != "--audio-out-device" {
		t.Fatalf("bare missing-output-default error = %v, want typed output binding error", err)
	}
	if inferencer.sessionValue() != nil {
		t.Fatal("bare missing-output-default path connected a provider session")
	}
	if got := registry.Observations(); got.OpenCount != 1 || got.ReleaseCount != 1 {
		t.Fatalf("bare missing-output-default device observations = %+v, want input open/release only", got)
	}
}

type synchronizedBareSessionOutput struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *synchronizedBareSessionOutput) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(data)
}

func (b *synchronizedBareSessionOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func waitForBareSessionOutput(t *testing.T, output *synchronizedBareSessionOutput, want string) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if strings.Contains(output.String(), want) {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %q in output: %q", want, output.String())
		case <-ticker.C:
		}
	}
}

type bareConnectBlockingInferencer struct {
	started chan struct{}
	once    sync.Once
}

func (i *bareConnectBlockingInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	i.once.Do(func() { close(i.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

type bareFailingInferencer struct {
	err error
}

func (i *bareFailingInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return nil, i.err
}
