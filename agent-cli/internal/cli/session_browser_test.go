package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
	"github.com/spf13/cobra"
)

func TestSessionCommandBrowserFlagsExposeC0Surface(t *testing.T) {
	command := NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, nil).Generate()
	var help bytes.Buffer
	command.SetOut(&help)
	command.SetArgs([]string{"--help"})

	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("session --help: %v", err)
	}
	for _, name := range sessionBrowserFlagNames {
		if command.Flags().Lookup(name) == nil {
			t.Errorf("C0 flag --%s is not registered", name)
		}
		if !strings.Contains(help.String(), "--"+name) {
			t.Errorf("session help does not contain --%s", name)
		}
	}
	if !strings.Contains(help.String(), "webmcp") {
		t.Fatalf("session help does not name the WebMCP capability:\n%s", help.String())
	}
	if strings.Contains(help.String(), "--webmcp-") {
		t.Fatalf("session help exposes superseded --webmcp-* aliases:\n%s", help.String())
	}
}

func TestSessionCommandInputAudioTranscriptionHelp(t *testing.T) {
	command := NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, nil).Generate()
	var help bytes.Buffer
	command.SetOut(&help)
	command.SetArgs([]string{"--help"})

	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("session --help: %v", err)
	}
	if command.Flags().Lookup("no-input-transcription") == nil {
		t.Fatal("session command did not register --no-input-transcription")
	}
	for _, want := range []string{"enabled by default only for live OpenAI sessions that accept audio input", "--no-input-transcription", "Replay always follows its recorded session.update handshake"} {
		if !strings.Contains(help.String(), want) {
			t.Fatalf("session help does not document %q:\n%s", want, help.String())
		}
	}
}

func TestResolveSessionBrowserConfigAppliesCLIOverYAMLAndEnvironment(t *testing.T) {
	configDir := t.TempDir()
	configYAML := `
browser:
  tools:
    enabled: false
    backend: webmcp
  connection:
    cdp_url: http://file.example:9222
    ws_endpoint: ws://file.example/devtools/browser/id
    allow_remote_cdp: true
  selection:
    browser: file-browser
    tab: file-tab
    persist: true
  policy:
    allowed_origins: [https://file.example]
    denied_origins: [https://denied.file.example]
    approval: always
  limits:
    invocation_timeout: 5s
    max_input_bytes: 11
    max_result_bytes: 12
  recording:
    enabled: true
  replay:
    path: /file/replay.jsonl
    strict: true
`
	if err := os.WriteFile(filepath.Join(configDir, config.ConfigFileName), []byte(configYAML), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("AGENT_BROWSER__CONNECTION__CDP_URL", "http://env.example:9222")
	t.Setenv("AGENT_BROWSER__SELECTION__TAB", "env-tab")
	t.Setenv("AGENT_BROWSER__POLICY__APPROVAL", "never")
	t.Setenv("AGENT_BROWSER__LIMITS__MAX_RESULT_BYTES", "22")

	command := &cobra.Command{Use: "test"}
	values := flags.NewBrowserFlags()
	registerSessionBrowserFlags(command, values)
	if err := command.Flags().Parse([]string{
		"--browser-tools=webmcp",
		"--browser-cdp-url", "http://cli.example:9222",
		"--browser-allow-remote-cdp=false",
		"--browser-persist-selection=false",
		"--browser-allowed-origin", "https://cli-a.example",
		"--browser-allowed-origin", "https://cli-b.example",
		"--browser-invocation-timeout", "9s",
		"--browser-max-result-bytes", "99",
		"--browser-record=false",
		"--browser-replay", "/cli/replay.jsonl",
		"--browser-replay-strict=false",
	}); err != nil {
		t.Fatalf("parse browser flags: %v", err)
	}

	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = configDir
	resolved, err := resolveSessionBrowserConfig(globalFlags, command, values)
	if err != nil {
		t.Fatalf("resolveSessionBrowserConfig(): %v", err)
	}
	if resolved.ConfigDir != configDir {
		t.Fatalf("resolved config directory = %q, want %q", resolved.ConfigDir, configDir)
	}
	got := resolved.Browser
	if !got.BrowserBackendEnabled() || got.Tools.Backend != config.BrowserToolsBackendWebMCP {
		t.Fatalf("CLI tools activation = %+v, want enabled WebMCP", got.Tools)
	}
	if got.Connection.CDPURL != "http://cli.example:9222" || got.Connection.AllowRemoteCDP {
		t.Fatalf("CLI connection overrides = %+v", got.Connection)
	}
	if got.Connection.WSEndpoint != "ws://file.example/devtools/browser/id" {
		t.Fatalf("unrelated YAML connection value changed: %+v", got.Connection)
	}
	if got.Selection.Tab != "env-tab" || got.Selection.Browser != "file-browser" || got.Selection.Persist {
		t.Fatalf("selection precedence = %+v", got.Selection)
	}
	if !strings.EqualFold(got.Policy.Approval, "never") || len(got.Policy.AllowedOrigins) != 2 || got.Policy.AllowedOrigins[0] != "https://cli-a.example" || got.Policy.DeniedOrigins[0] != "https://denied.file.example" {
		t.Fatalf("policy precedence = %+v", got.Policy)
	}
	if got.Limits.InvocationTimeout != 9*time.Second || got.Limits.MaxInputBytes != 11 || got.Limits.MaxResultBytes != 99 {
		t.Fatalf("limits precedence = %+v", got.Limits)
	}
	if got.Recording.Enabled || got.Replay.Path != "/cli/replay.jsonl" || got.Replay.Strict {
		t.Fatalf("recording/replay precedence = %+v/%+v", got.Recording, got.Replay)
	}
}

func TestSessionBrowserFlagsRejectInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want error
	}{
		{name: "unknown backend", args: []string{"--browser-tools=chrome"}, want: ErrInvalidBrowserToolsBackend},
		{name: "case sensitive backend", args: []string{"--browser-tools=WebMCP"}, want: ErrInvalidBrowserToolsBackend},
		{name: "permissive bool spelling", args: []string{"--browser-allow-remote-cdp=1"}},
		{name: "negative size", args: []string{"--browser-max-input-bytes=-1"}},
		{name: "hex size", args: []string{"--browser-max-result-bytes=0x10"}},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			command := NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, nil).Generate()
			command.SetArgs(testCase.args)
			err := command.ExecuteContext(context.Background())
			if err == nil {
				t.Fatal("invalid browser input returned nil")
			}
			if testCase.want != nil && !errors.Is(err, testCase.want) {
				t.Fatalf("error %v does not unwrap to %v", err, testCase.want)
			}
		})
	}
}

func TestSessionBrowserNonAdmissionReturnsHelpWithoutSetup(t *testing.T) {
	tests := []struct {
		name       string
		configYAML string
		args       []string
	}{
		{
			name: "endpoint only",
			args: []string{"--browser-cdp-url", "http://127.0.0.1:9222"},
		},
		{
			name: "managed control only",
			args: []string{"--browser-headless"},
		},
		{
			name: "config only",
			configYAML: `
browser:
  tools:
    enabled: true
    backend: webmcp
`,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			configDir := t.TempDir()
			if testCase.configYAML != "" {
				if err := os.WriteFile(filepath.Join(configDir, config.ConfigFileName), []byte(testCase.configYAML), 0600); err != nil {
					t.Fatalf("write config: %v", err)
				}
			}
			globalFlags := flags.NewGlobalFlags()
			globalFlags.ConfigDirPath = configDir
			inferencer := &cliSideEffectSessionInferencer{}
			toolCapabilityCalls := 0
			owner := NewSessionCommandWithRuntimeAndDeviceRegistryAndToolCapabilities(
				flags.NewAskFlags(),
				globalFlags,
				nil,
				inferencer,
				nil,
				nil,
				func(*config.Config) (SessionToolCapabilities, error) {
					toolCapabilityCalls++
					return SessionToolCapabilities{}, nil
				},
				nil,
			)
			command := owner.Generate()
			var out bytes.Buffer
			command.SetOut(&out)
			command.SetArgs(testCase.args)

			if err := command.ExecuteContext(context.Background()); err != nil {
				t.Fatalf("inert browser command: %v", err)
			}
			if !strings.Contains(out.String(), "Usage:") || !strings.Contains(out.String(), "--browser-tools") {
				t.Fatalf("inert browser command did not print session help:\n%s", out.String())
			}
			if inferencer.connects != 0 || toolCapabilityCalls != 0 {
				t.Fatalf("inert browser command performed setup: connects=%d tools=%d", inferencer.connects, toolCapabilityCalls)
			}
		})
	}
}

func TestSessionManagedBrowserFlagsApplyPrecedenceAndSelectMode(t *testing.T) {
	configDir := t.TempDir()
	configYAML := `
browser:
  tools:
    enabled: true
    backend: webmcp
  managed:
    headless: true
    open: https://file.example/start
    close_on_exit: false
`
	if err := os.WriteFile(filepath.Join(configDir, config.ConfigFileName), []byte(configYAML), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("AGENT_BROWSER__MANAGED__OPEN", "https://env.example/start")

	command := &cobra.Command{Use: "test"}
	values := flags.NewBrowserFlags()
	registerSessionBrowserFlags(command, values)
	if err := command.Flags().Parse([]string{
		"--browser-headless=false",
		"--browser-open", "https://cli.example/start",
		"--browser-close-on-exit=true",
	}); err != nil {
		t.Fatalf("parse managed browser flags: %v", err)
	}
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = configDir
	resolved, err := resolveSessionBrowserConfig(globalFlags, command, values)
	if err != nil {
		t.Fatalf("resolveSessionBrowserConfig(): %v", err)
	}
	if !resolved.Browser.BrowserBackendEnabled() {
		t.Fatalf("managed flag overrides unexpectedly disabled browser: %+v", resolved.Browser.Tools)
	}
	if resolved.Browser.Managed.Headless || resolved.Browser.Managed.Open != "https://cli.example/start" || !resolved.Browser.Managed.CloseOnExit {
		t.Fatalf("managed precedence = %+v", resolved.Browser.Managed)
	}
	if resolved.Browser.ManagedStartupURL() != "https://cli.example/start" {
		t.Fatalf("managed startup URL = %q", resolved.Browser.ManagedStartupURL())
	}
	if !resolved.Browser.UsesManagedBrowser() || resolved.Browser.UsesExternalBrowser() {
		t.Fatalf("endpoint-free browser mode = %q, want managed", resolved.Browser.ConnectionMode())
	}

	values = flags.NewBrowserFlags()
	command = &cobra.Command{Use: "test"}
	registerSessionBrowserFlags(command, values)
	if err := command.Flags().Parse([]string{"--browser-open", "https://one.example", "--browser-open", "https://two.example"}); err == nil || !strings.Contains(err.Error(), "at most one startup URL") {
		t.Fatalf("repeated --browser-open error = %v, want single-URL usage error", err)
	}
}

func TestSessionManagedBrowserRejectsInvalidStartupURLBeforeSetup(t *testing.T) {
	configDir := t.TempDir()
	command := &cobra.Command{Use: "test"}
	values := flags.NewBrowserFlags()
	registerSessionBrowserFlags(command, values)
	if err := command.Flags().Parse([]string{"--browser-open", "not-a-url"}); err != nil {
		t.Fatalf("parse invalid startup URL flag: %v", err)
	}
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = configDir
	_, err := resolveSessionBrowserConfig(globalFlags, command, values)
	if err == nil || !strings.Contains(err.Error(), "browser.managed.open") || !strings.Contains(err.Error(), "valid startup URL") {
		t.Fatalf("invalid startup URL resolution error = %v", err)
	}
}

func TestSessionBrowserToolsExplicitlyAdmitsInjectedLiveSession(t *testing.T) {
	configDir := t.TempDir()
	configYAML := `
model:
  provider: grok
  grok:
    model: grok-realtime-test
    api_key: test-key
browser:
  tools:
    enabled: false
    backend: webmcp
`
	if err := os.WriteFile(filepath.Join(configDir, config.ConfigFileName), []byte(configYAML), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = configDir
	inferencer := newBrowserAdmissionInferencer()
	defer inferencer.Close()
	deviceRegistry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("new virtual device registry: %v", err)
	}
	toolCapabilityCalls := 0
	capabilityCloseCalls := 0
	var resolvedBrowser config.BrowserConfig
	owner := NewSessionCommandWithRuntimeAndDeviceRegistryAndToolCapabilities(
		flags.NewAskFlags(),
		globalFlags,
		nil,
		inferencer,
		nil,
		nil,
		func(cfg *config.Config) (SessionToolCapabilities, error) {
			toolCapabilityCalls++
			resolvedBrowser = cfg.Browser
			return SessionToolCapabilities{
				Close: func() error {
					capabilityCloseCalls++
					return nil
				},
			}, nil
		},
		deviceRegistry,
	)
	command := owner.Generate()
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetArgs([]string{"--browser-tools=webmcp"})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := command.ExecuteContext(ctx); err != nil {
		t.Fatalf("explicit browser live session: %v", err)
	}
	if toolCapabilityCalls != 1 || !resolvedBrowser.BrowserBackendEnabled() {
		t.Fatalf("resolved browser capability = %+v after %d calls, want enabled WebMCP once", resolvedBrowser, toolCapabilityCalls)
	}
	if inferencer.connects != 1 {
		t.Fatalf("live session connections = %d, want 1", inferencer.connects)
	}
	if capabilityCloseCalls != 1 {
		t.Fatalf("transferred capability close calls = %d, want one", capabilityCloseCalls)
	}
	if strings.Contains(out.String(), "Usage:") || strings.Contains(out.String(), "requires --record") {
		t.Fatalf("explicit browser activation fell back to help/record validation:\n%s", out.String())
	}
}

func TestSessionBrowserToolsClosesTransferredCapabilityOnPlanningFailure(t *testing.T) {
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	closeCalls := 0
	owner := NewSessionCommandWithRuntimeAndDeviceRegistryAndToolCapabilities(
		flags.NewAskFlags(),
		globalFlags,
		nil,
		nil,
		nil,
		nil,
		func(*config.Config) (SessionToolCapabilities, error) {
			return SessionToolCapabilities{Close: func() error {
				closeCalls++
				return nil
			}}, nil
		},
		nil,
	)
	command := owner.Generate()
	command.SetArgs([]string{"--browser-tools=webmcp", "--provider=unsupported-provider"})

	err := command.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), `unsupported realtime session provider "unsupported-provider"`) || !strings.Contains(err.Error(), `supported providers are "openai" and "grok"`) {
		t.Fatalf("planning error = %v, want unsupported browser live provider error", err)
	}
	if closeCalls != 1 {
		t.Fatalf("planning-failure capability close calls = %d, want one", closeCalls)
	}
}

// TestSessionManagedBrowserStartupFailureStopsBeforeProvider locks the exact
// interactive command boundary: a managed browser must be visible and usable
// before the realtime provider starts. Silently retaining a failed browser
// capability leaves the model with tools that can never open a tab.
func TestSessionManagedBrowserStartupFailureStopsBeforeProvider(t *testing.T) {
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	inferencer := newBrowserAdmissionInferencer()
	defer inferencer.Close()
	startupErr := errors.New("managed Chrome did not become ready")
	closeCalls := 0
	owner := NewSessionCommandWithRuntimeAndDeviceRegistryAndToolCapabilities(
		flags.NewAskFlags(),
		globalFlags,
		nil,
		inferencer,
		nil,
		nil,
		func(*config.Config) (SessionToolCapabilities, error) {
			return SessionToolCapabilities{
				Initialize: func(context.Context) error { return startupErr },
				Close: func() error {
					closeCalls++
					return nil
				},
			}, nil
		},
		nil,
	)
	command := owner.Generate()
	command.SetArgs([]string{
		"--browser-tools", "webmcp",
		"--record", filepath.Join(t.TempDir(), "test17.json"),
		"--model", "gpt-realtime-2.1-mini",
	})

	err := command.ExecuteContext(context.Background())
	if !errors.Is(err, startupErr) || !strings.Contains(err.Error(), "initialize session tools") {
		t.Fatalf("managed browser startup error = %v, want explicit initialization failure", err)
	}
	if inferencer.connects != 0 {
		t.Fatalf("provider connections = %d, want zero before browser readiness", inferencer.connects)
	}
	if closeCalls != 1 {
		t.Fatalf("failed capability close calls = %d, want one", closeCalls)
	}
}

type browserAdmissionInferencer struct {
	connects int
	session  *browserAdmissionSession
}

func newBrowserAdmissionInferencer() *browserAdmissionInferencer {
	return &browserAdmissionInferencer{}
}

func (i *browserAdmissionInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	i.connects++
	session := &browserAdmissionSession{
		receive: messages.NewTypedBuffer[messages.StreamMessage](16),
		done:    make(chan struct{}),
		media:   newBrowserAdmissionMedia(),
	}
	i.session = session
	session.receive.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("browser-admission", "session"),
	})
	session.receive.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValue("browser-admission", "fixture-complete"),
	})
	return session, nil
}

func (i *browserAdmissionInferencer) Close() {
	if i.session != nil {
		_ = i.session.Close()
	}
}

type browserAdmissionSession struct {
	receive   *messages.TypedBuffer[messages.StreamMessage]
	done      chan struct{}
	media     *browserAdmissionMedia
	closeOnce sync.Once
}

func (s *browserAdmissionSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	_ = ctx
	_ = msg
	return true
}

func (s *browserAdmissionSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *browserAdmissionSession) Done() <-chan struct{} { return s.done }

func (s *browserAdmissionSession) RTCMedia() rtc.MediaEndpoints {
	if s == nil || s.media == nil {
		return rtc.MediaEndpoints{}
	}
	return rtc.MediaEndpoints{Inbound: s.media, Outbound: s.media}
}

func (s *browserAdmissionSession) Close() error {
	s.closeOnce.Do(func() {
		close(s.done)
		if s.media != nil {
			_ = s.media.Close()
		}
	})
	return nil
}

type browserAdmissionMedia struct {
	closed chan struct{}
	once   sync.Once
}

func newBrowserAdmissionMedia() *browserAdmissionMedia {
	return &browserAdmissionMedia{closed: make(chan struct{})}
}

func (m *browserAdmissionMedia) ReadFrame(ctx context.Context) (rtc.PCMFrame, error) {
	select {
	case <-m.closed:
		return rtc.PCMFrame{}, rtc.ErrPeerClosed
	case <-ctx.Done():
		return rtc.PCMFrame{}, ctx.Err()
	}
}

func (m *browserAdmissionMedia) WriteFrame(ctx context.Context, _ rtc.PCMFrame) error {
	select {
	case <-m.closed:
		return rtc.ErrPeerClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *browserAdmissionMedia) Close() error {
	m.once.Do(func() { close(m.closed) })
	return nil
}

var _ rtc.InboundMedia = (*browserAdmissionMedia)(nil)
var _ rtc.OutboundMedia = (*browserAdmissionMedia)(nil)
