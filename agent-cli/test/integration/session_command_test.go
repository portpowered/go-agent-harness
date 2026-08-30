package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestSessionCommand_HelpDocumentsRecordReplayAndHistorySubcommands(t *testing.T) {
	agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetArgs([]string{"session", "--help"})

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute help: %v", err)
	}

	help := testWriter.StdoutString()
	for _, want := range []string{"--record", "--replay", "show", "list", "delete"} {
		if !strings.Contains(help, want) {
			t.Fatalf("session help missing %q:\n%s", want, help)
		}
	}
}

func TestSessionCommand_ReplayMissingFileReturnsActionableError(t *testing.T) {
	agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	missingPath := filepath.Join(t.TempDir(), "missing.json")
	rootCmd := agentCLI.Generate()
	rootCmd.SetArgs([]string{"session", "--replay", missingPath})

	err = rootCmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected missing replay file error")
	}
	if !strings.Contains(err.Error(), "replay session capture") || !strings.Contains(err.Error(), missingPath) {
		t.Fatalf("missing replay error should include capture path, got: %v", err)
	}
}

func TestSessionCommand_RejectsNonJSONCapturePath(t *testing.T) {
	agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	rootCmd := agentCLI.Generate()
	rootCmd.SetArgs([]string{"session", "--record", "capture.txt"})

	err = rootCmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected invalid capture extension error")
	}
	if !strings.Contains(err.Error(), "must end with .json") {
		t.Fatalf("invalid extension error should explain required suffix, got: %v", err)
	}
}

func TestSessionCommand_RecordRequiresLiveSessionProvider(t *testing.T) {
	agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	rootCmd := agentCLI.Generate()
	rootCmd.SetArgs([]string{"session", "--record", "capture.json"})

	err = rootCmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected missing provider error")
	}
	want := "--record requires --provider grok or --provider openai for live session inference"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("record error should provide full live provider guidance %q, got: %v", want, err)
	}
}

func TestSessionCommand_RecordUsesConfiguredGrokProviderAndRequiresCredentials(t *testing.T) {
	agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	configDir := t.TempDir()
	configYAML := `
model:
  provider: grok
  grok:
    model: grok-session-model
`
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configYAML), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	rootCmd := agentCLI.Generate()
	rootCmd.SetArgs([]string{"--config-dir", configDir, "session", "--record", filepath.Join(configDir, "capture.json")})

	err = rootCmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected missing Grok credential error")
	}
	if !strings.Contains(err.Error(), "grok API key is required") {
		t.Fatalf("record error should validate configured Grok credentials, got: %v", err)
	}
	if strings.Contains(err.Error(), "--provider grok") {
		t.Fatalf("configured Grok provider should avoid provider flag error, got: %v", err)
	}
}

func TestSessionCommand_OpenAIRealtimeRecordUsesInjectedSessionInferencer(t *testing.T) {
	sessionInf := &integrationScriptedSessionInferencer{
		events: []messages.StreamMessage{
			{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
			{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()},
			{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("OpenAI realtime route selected")},
			{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, Value: messages.NewTextEndValue()},
			{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
			{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("integration-session", "test complete")},
		},
	}
	agentCLI, err := wire.InitializeMockAgentCLIWithSessionInferencer(
		&mockToolExecutor{},
		&mockInferencerError{err: errors.New("stateless inferencer should not be called")},
		sessionInf,
	)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetArgs([]string{
		"session",
		"--record", filepath.Join(t.TempDir(), "openai-realtime.json"),
		"--provider", "openai",
		"--model", "gpt-realtime",
		"--api-key", "sk-test-key",
		"hello", "openai",
	})

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute OpenAI realtime session command: %v", err)
	}
	if !sessionInf.connected {
		t.Fatal("OpenAI realtime session command did not connect the injected session inferencer")
	}
	assertIntegrationSessionReceivedText(t, sessionInf, "hello openai")
	if got := testWriter.StdoutString(); !strings.Contains(got, "OpenAI realtime route selected") {
		t.Fatalf("OpenAI realtime session output missing injected session response, got:\n%s", got)
	}
}

func TestSessionCommand_OpenAIRecordRejectsNonRealtimeModel(t *testing.T) {
	agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	rootCmd := agentCLI.Generate()
	rootCmd.SetArgs([]string{
		"session",
		"--record", filepath.Join(t.TempDir(), "openai-session.json"),
		"--provider", "openai",
		"--model", "gpt-4o",
		"--api-key", "sk-test-key",
	})

	err = rootCmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected non-realtime OpenAI model error")
	}
	if !strings.Contains(err.Error(), "not realtime-capable") || !strings.Contains(err.Error(), "gpt-realtime") {
		t.Fatalf("OpenAI non-realtime model error should be actionable, got: %v", err)
	}
}

func TestSessionCommand_ReplayBypassesConfiguredGrokCredentials(t *testing.T) {
	agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	configDir := t.TempDir()
	configYAML := `
model:
  provider: grok
  grok:
    model: grok-session-model
`
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configYAML), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetArgs([]string{"--config-dir", configDir, "session", "--replay", locateSharedSessionFixture(t, "session_text_reply.session.json")})

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute replay with incomplete Grok config should bypass live credentials: %v", err)
	}

	if got := testWriter.StdoutString(); !strings.Contains(got, "Hello! How can I help you today?") {
		t.Fatalf("replay output missing text deltas, got:\n%s", got)
	}
}

func TestSessionCommand_ReplayUsesCaptureAndPrintsTextDeltas(t *testing.T) {
	agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetArgs([]string{"session", "--replay", locateSharedSessionFixture(t, "session_text_reply.session.json")})

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute replay: %v", err)
	}

	if got := testWriter.StdoutString(); !strings.Contains(got, "Hello! How can I help you today?") {
		t.Fatalf("replay output missing text deltas, got:\n%s", got)
	}
}

func TestSessionCommand_ReplayGrokWebSocketCaptureDoesNotCallLiveDialer(t *testing.T) {
	agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	capturePath := filepath.Join(t.TempDir(), "grok-websocket.session.json")
	writeGrokWebSocketCapture(t, capturePath)

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetArgs([]string{"session", "--replay", capturePath})

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute websocket replay without live credentials or network: %v", err)
	}

	if got := testWriter.StdoutString(); !strings.Contains(got, "Grok replay response") {
		t.Fatalf("replay output missing Grok wire text delta, got:\n%s", got)
	}
}

func TestSessionCommand_OpenAIRealtimeReplayWithoutVoicePreservesProviderDefault(t *testing.T) {
	agentCLI, err := wire.InitializeMockAgentCLI(
		&mockToolExecutor{},
		&mockInferencerError{err: errors.New("stateless inferencer should not be called")},
	)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetArgs([]string{
		"session",
		"--replay", locateCLIFixture(t, "openai_realtime_text.session.json"),
		"--provider", "openai",
		"--model", "gpt-realtime",
		"hello", "realtime",
	})

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute OpenAI realtime replay without live network: %v", err)
	}

	if got := testWriter.StdoutString(); !strings.Contains(got, "OpenAI replay response") {
		t.Fatalf("OpenAI replay output missing fixture transcript, got:\n%s", got)
	}
}

func TestSessionCommand_OpenAIRealtimeReplayBareUsesRecordedPromptAndReportsCompletion(t *testing.T) {
	agentCLI, err := wire.InitializeMockAgentCLI(
		&mockToolExecutor{},
		&mockInferencerError{err: errors.New("stateless inferencer should not be called")},
	)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	capturePath := filepath.Join(t.TempDir(), "openai-bare-prompt.session.json")
	writeOpenAIBarePromptCapture(t, capturePath)

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetArgs([]string{
		"session",
		"--replay", capturePath,
	})

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute bare OpenAI realtime replay without live network: %v", err)
	}

	got := testWriter.StdoutString()
	want := "recorded bare replay transcript\n[session replay complete]\n"
	if got != want {
		t.Fatalf("bare OpenAI replay output = %q, want %q", got, want)
	}
	if strings.Count(got, "[session replay complete]") != 1 {
		t.Fatalf("bare OpenAI replay should report exactly one completion marker, got:\n%s", got)
	}
	if strings.Contains(got, "[session closed: client_close]") {
		t.Fatalf("bare OpenAI replay reported a synthesized client close, got:\n%s", got)
	}
	if bytes.Contains([]byte(got), []byte{0x52, 0x49, 0x46, 0x46, 0x10, 0x20, 0x30, 0x40}) {
		t.Fatalf("bare OpenAI replay wrote recorded PCM to text output, got: %q", got)
	}
}

func TestSessionCommand_OpenAIRealtimeReplayBareEmptyPromptWithMaxDuration(t *testing.T) {
	agentCLI, err := wire.InitializeMockAgentCLI(
		&mockToolExecutor{},
		&mockInferencerError{err: errors.New("stateless inferencer should not be called")},
	)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	capturePath := filepath.Join(t.TempDir(), "openai-bare-empty-prompt.session.json")
	writeOpenAIBarePromptCaptureWithText(t, capturePath, "")

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetArgs([]string{
		"session",
		"--replay", capturePath,
		"--max-duration", "1s",
	})

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute bare empty-prompt OpenAI replay with max duration: %v", err)
	}

	got := testWriter.StdoutString()
	want := "recorded bare replay transcript\n[session replay complete]\n"
	if got != want {
		t.Fatalf("bare empty-prompt OpenAI replay output = %q, want %q", got, want)
	}
	if strings.Count(got, "[session replay complete]") != 1 {
		t.Fatalf("bare empty-prompt replay should report exactly one completion marker, got:\n%s", got)
	}
	if strings.Contains(got, "[session closed: client_close]") {
		t.Fatalf("bare empty-prompt replay reported a synthesized client close, got:\n%s", got)
	}
	if bytes.Contains([]byte(got), []byte{0x52, 0x49, 0x46, 0x46, 0x10, 0x20, 0x30, 0x40}) {
		t.Fatalf("bare empty-prompt replay wrote recorded PCM to text output, got: %q", got)
	}
}

func TestSessionCommand_OpenAIRealtimeReplayExplicitEmptyPromptRemainsStrict(t *testing.T) {
	agentCLI, err := wire.InitializeMockAgentCLI(
		&mockToolExecutor{},
		&mockInferencerError{err: errors.New("stateless inferencer should not be called")},
	)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetArgs([]string{
		"session",
		"--replay", locateCLIFixture(t, "openai_realtime_text.session.json"),
		"--prompt=",
	})

	err = rootCmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected explicit empty prompt to mismatch the recorded prompt")
	}
	if !errors.Is(err, gateway.ErrReplayMismatch) {
		t.Fatalf("expected strict replay mismatch, got: %v", err)
	}
	if strings.Contains(testWriter.StdoutString(), "[session replay complete]") {
		t.Fatalf("strict replay mismatch should not report completion, got:\n%s", testWriter.StdoutString())
	}
}

func TestSessionCommand_OpenAIRealtimeReplayVoiceMatchesCurrentWire(t *testing.T) {
	agentCLI, err := wire.InitializeMockAgentCLI(
		&mockToolExecutor{},
		&mockInferencerError{err: errors.New("stateless inferencer should not be called")},
	)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetArgs([]string{
		"session",
		"--replay", locateCLIFixture(t, "openai_realtime_text_marin.session.json"),
		"--provider", "openai",
		"--model", "gpt-realtime",
		"--voice", "marin",
		"hello", "realtime",
	})

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute OpenAI realtime replay with marin voice: %v", err)
	}
	if got := testWriter.StdoutString(); !strings.Contains(got, "OpenAI replay response") {
		t.Fatalf("OpenAI marin replay output missing fixture transcript, got:\n%s", got)
	}
}

func TestSessionCommand_OpenAIRealtimeReplayInvalidVoiceFailsBeforeFixtureLoad(t *testing.T) {
	const rejected = "not-a-voice"
	agentCLI, err := wire.InitializeMockAgentCLI(
		&mockToolExecutor{},
		&mockInferencerError{err: errors.New("stateless inferencer should not be called")},
	)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	missingCapture := filepath.Join(t.TempDir(), "not-consumed.session.json")
	rootCmd := agentCLI.Generate()
	rootCmd.SetArgs([]string{
		"session",
		"--voice", rejected,
		"--replay", missingCapture,
		"--provider", "openai",
		"--model", "gpt-realtime",
	})

	err = rootCmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected invalid OpenAI realtime voice error")
	}
	if !errors.Is(err, services.ErrInvalidOpenAIRealtimeVoice) {
		t.Fatalf("error = %v, want ErrInvalidOpenAIRealtimeVoice", err)
	}
	var typed *services.InvalidOpenAIRealtimeVoiceError
	if !errors.As(err, &typed) {
		t.Fatalf("error = %v, want InvalidOpenAIRealtimeVoiceError", err)
	}
	if typed.Voice != rejected {
		t.Fatalf("rejected voice = %q, want %q", typed.Voice, rejected)
	}
	if strings.Contains(err.Error(), missingCapture) {
		t.Fatalf("invalid voice validation loaded the replay capture: %v", err)
	}
}

func TestSessionCommand_OpenAIRealtimeReplayReportsProviderError(t *testing.T) {
	agentCLI, err := wire.InitializeMockAgentCLI(
		&mockToolExecutor{},
		&mockInferencerError{err: errors.New("stateless inferencer should not be called")},
	)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	rootCmd := agentCLI.Generate()
	rootCmd.SetArgs([]string{
		"session",
		"--replay", locateCLIFixture(t, "openai_realtime_error.session.json"),
		"--provider", "openai",
		"--model", "gpt-realtime",
		"trigger", "provider", "error",
	})

	err = rootCmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected OpenAI realtime replay provider error")
	}
	if !strings.Contains(err.Error(), "session error") || !strings.Contains(err.Error(), "Realtime fixture rejected request") {
		t.Fatalf("OpenAI replay error should report provider session error, got: %v", err)
	}
}

func TestSessionCommand_OpenAIRealtimeReplay_EndToEndSmoke(t *testing.T) {
	agentCLI, err := wire.InitializeMockAgentCLI(
		&mockToolExecutor{},
		&mockInferencerError{err: errors.New("stateless inferencer should not be called")},
	)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetArgs([]string{
		"session",
		"--replay", locateCLIFixture(t, "openai_realtime_smoke.session.json"),
		"--provider", "openai",
		"--model", "gpt-realtime",
		"run", "the", "openai", "smoke", "replay",
	})

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute OpenAI end-to-end replay without live network: %v", err)
	}

	got := testWriter.StdoutString()
	if !strings.Contains(got, "OpenAI E2E replay complete.") {
		t.Fatalf("OpenAI replay output missing smoke transcript, got:\n%s", got)
	}
	if !strings.Contains(got, "[session closed: fixture_complete]") {
		t.Fatalf("OpenAI replay output missing session close status, got:\n%s", got)
	}
	for _, want := range []string{
		"classification=provider_close",
		"terminal_reason=provider_close",
		"terminal_provenance=provider",
		"output_state=not_applicable",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("OpenAI replay output missing terminal field %q, got:\n%s", want, got)
		}
	}
}

func TestSessionCommand_ReplayGrokWebSocketCapture_EndToEndSmoke(t *testing.T) {
	agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	configDir := t.TempDir()
	configYAML := `
model:
  provider: grok
  grok:
    model: grok-replay-smoke
`
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configYAML), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	capturePath := filepath.Join(t.TempDir(), "grok-websocket-smoke.session.json")
	writeGrokWebSocketSmokeCapture(t, capturePath)

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetArgs([]string{"--config-dir", configDir, "session", "--replay", capturePath, "run", "the", "smoke", "replay"})

	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute end-to-end websocket replay without live credentials or network: %v", err)
	}

	got := testWriter.StdoutString()
	if !strings.Contains(got, "E2E Grok replay complete.") {
		t.Fatalf("replay output missing smoke transcript, got:\n%s", got)
	}
	if !strings.Contains(got, "[session closed: fixture_complete]") {
		t.Fatalf("replay output missing session close status, got:\n%s", got)
	}
	for _, want := range []string{
		// Grok closes carry the public transport classification on the typed
		// close value; the terminal reason names the provider-close teardown.
		"classification=transport",
		"terminal_reason=provider_close",
		"terminal_provenance=provider",
		"output_state=not_applicable",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Grok replay output missing terminal field %q, got:\n%s", want, got)
		}
	}
}

func TestSessionCommand_ReplayGrokWebSocketCaptureFailsOnDivergentOutbound(t *testing.T) {
	agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	capturePath := filepath.Join(t.TempDir(), "grok-websocket-smoke.session.json")
	writeGrokWebSocketSmokeCapture(t, capturePath)

	rootCmd := agentCLI.Generate()
	rootCmd.SetArgs([]string{"session", "--replay", capturePath, "wrong", "prompt"})

	start := time.Now()
	err = rootCmd.ExecuteContext(context.Background())
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected replay divergence error")
	}
	if !errors.Is(err, gateway.ErrReplayMismatch) {
		t.Fatalf("expected replay mismatch classification, got: %v", err)
	}
	// Session planning now performs a bounded display-capability admission
	// probe before the replay transport is opened. Allow that one-second probe
	// budget plus scheduler overhead while still requiring prompt divergence.
	if elapsed >= 2*time.Second {
		t.Fatalf("replay divergence should fail before the bounded session timeout; elapsed=%s", elapsed)
	}
}

func writeGrokWebSocketCapture(t *testing.T, path string) {
	t.Helper()

	records := []gwtesting.CapturedSessionEvent{
		grokWebSocketRecord(gwtesting.DirectionClientToServer, 1, `{"type":"session.update","session":{"model":"grok-replay-model"}}`),
		grokWebSocketRecord(gwtesting.DirectionServerToClient, 2, `{"type":"session.created","session_id":"sess-replay","model":"grok-replay-model"}`),
		grokWebSocketRecord(gwtesting.DirectionServerToClient, 3, `{"type":"response.created"}`),
		grokWebSocketRecord(gwtesting.DirectionServerToClient, 4, `{"type":"response.text.delta","delta":"Grok replay response"}`),
		grokWebSocketRecord(gwtesting.DirectionServerToClient, 5, `{"type":"response.done"}`),
	}
	data, err := json.MarshalIndent(gwtesting.SessionCapture{
		Version: gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{
			Name:  "grok",
			Model: "grok-replay-model",
		},
		Session: gwtesting.SessionMetadata{
			ID:           "sess-replay",
			StartedAtUTC: time.Now().UTC().Format(time.RFC3339Nano),
		},
		Records: records,
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal websocket capture: %v", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write websocket capture: %v", err)
	}
}

func writeOpenAIBarePromptCapture(t *testing.T, path string) {
	t.Helper()
	writeOpenAIBarePromptCaptureWithText(t, path, "recorded bare replay prompt")
}

func writeOpenAIBarePromptCaptureWithText(t *testing.T, path, prompt string) {
	t.Helper()

	promptJSON, err := json.Marshal(prompt)
	if err != nil {
		t.Fatalf("marshal bare OpenAI prompt: %v", err)
	}
	records := []gwtesting.CapturedSessionEvent{
		grokWebSocketRecord(gwtesting.DirectionClientToServer, 1, `{"type":"session.update","session":{"model":"gpt-realtime"}}`),
		grokWebSocketRecord(gwtesting.DirectionServerToClient, 2, `{"type":"session.created","session_id":"sess-bare-replay","model":"gpt-realtime"}`),
		grokWebSocketRecord(gwtesting.DirectionClientToServer, 3, `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":`+string(promptJSON)+`}]}}`),
		grokWebSocketRecord(gwtesting.DirectionClientToServer, 4, `{"type":"response.create"}`),
		grokWebSocketRecord(gwtesting.DirectionServerToClient, 5, `{"type":"response.created"}`),
		grokWebSocketRecord(gwtesting.DirectionServerToClient, 6, `{"type":"response.output_audio.delta","delta":"UklGRgQgMEA=","format":"pcm16"}`),
		grokWebSocketRecord(gwtesting.DirectionServerToClient, 7, `{"type":"response.output_text.delta","delta":"recorded bare replay transcript"}`),
		grokWebSocketRecord(gwtesting.DirectionServerToClient, 8, `{"type":"response.output_text.done"}`),
		grokWebSocketRecord(gwtesting.DirectionServerToClient, 9, `{"type":"response.output_audio.done"}`),
		grokWebSocketRecord(gwtesting.DirectionServerToClient, 10, `{"type":"response.done"}`),
	}
	data, err := json.MarshalIndent(gwtesting.SessionCapture{
		Version: gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{
			Name:  "openai",
			Model: "gpt-realtime",
		},
		Session: gwtesting.SessionMetadata{
			ID:           "sess-bare-replay",
			StartedAtUTC: time.Now().UTC().Format(time.RFC3339Nano),
		},
		Records: records,
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal bare OpenAI websocket capture: %v", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write bare OpenAI websocket capture: %v", err)
	}
}

func writeGrokWebSocketSmokeCapture(t *testing.T, path string) {
	t.Helper()

	records := []gwtesting.CapturedSessionEvent{
		grokWebSocketRecord(gwtesting.DirectionClientToServer, 1, `{"type":"session.update","session":{"model":"grok-replay-smoke"}}`),
		grokWebSocketRecord(gwtesting.DirectionServerToClient, 2, `{"type":"session.created","session_id":"sess-replay-smoke","model":"grok-replay-smoke"}`),
		grokWebSocketRecord(gwtesting.DirectionClientToServer, 3, `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"run the smoke replay"}]}}`),
		grokWebSocketRecord(gwtesting.DirectionServerToClient, 4, `{"type":"response.created"}`),
		grokWebSocketRecord(gwtesting.DirectionServerToClient, 5, `{"type":"response.text.delta","delta":"E2E Grok replay "}`),
		grokWebSocketRecord(gwtesting.DirectionServerToClient, 6, `{"type":"response.text.delta","delta":"complete."}`),
		grokWebSocketRecord(gwtesting.DirectionServerToClient, 7, `{"type":"response.text.done"}`),
		grokWebSocketRecord(gwtesting.DirectionServerToClient, 8, `{"type":"response.done"}`),
		grokWebSocketRecord(gwtesting.DirectionServerToClient, 9, `{"type":"session.closed","reason":"fixture_complete"}`),
	}
	data, err := json.MarshalIndent(gwtesting.SessionCapture{
		Version: gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{
			Name:  "grok",
			Model: "grok-replay-smoke",
		},
		Session: gwtesting.SessionMetadata{
			ID:                "sess-replay-smoke",
			StartedAtUTC:      time.Now().UTC().Format(time.RFC3339Nano),
			FixtureProvenance: "synthetic end-to-end smoke fixture generated by integration test",
		},
		Records: records,
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal websocket smoke capture: %v", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write websocket smoke capture: %v", err)
	}
}

func grokWebSocketRecord(direction gwtesting.SessionEventDirection, sequence int, payload string) gwtesting.CapturedSessionEvent {
	var envelope struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal([]byte(payload), &envelope)
	return gwtesting.CapturedSessionEvent{
		Sequence:    sequence,
		Direction:   direction,
		TimestampMs: int64(sequence),
		Type:        envelope.Type,
		PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
		Payload:     json.RawMessage(payload),
	}
}

type integrationScriptedSessionInferencer struct {
	events    []messages.StreamMessage
	connected bool
	sentText  chan string
}

func (s *integrationScriptedSessionInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	s.connected = true
	if s.sentText == nil {
		s.sentText = make(chan string, 8)
	}
	session := newIntegrationScriptedSession(s.sentText)
	go func() {
		session.recv.Write(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeSessionOpen,
			Value: messages.NewSessionOpenValue("integration-session", "openai"),
		})
		time.Sleep(150 * time.Millisecond)
		for _, evt := range s.events {
			session.recv.Write(ctx, evt)
		}
	}()
	return session, nil
}

func assertIntegrationSessionReceivedText(t *testing.T, sessionInf *integrationScriptedSessionInferencer, want string) {
	t.Helper()

	select {
	case got := <-sessionInf.sentText:
		if got != want {
			t.Fatalf("session received prompt = %q, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("session did not receive prompt %q", want)
	}
}

type integrationScriptedSession struct {
	recv     *messages.TypedBuffer[messages.StreamMessage]
	done     chan struct{}
	once     sync.Once
	sentText chan string
}

func newIntegrationScriptedSession(sentText chan string) *integrationScriptedSession {
	return &integrationScriptedSession{
		recv:     messages.NewTypedBuffer[messages.StreamMessage](32),
		done:     make(chan struct{}),
		sentText: sentText,
	}
}

func (s *integrationScriptedSession) Send(_ context.Context, msg messages.StreamMessage) bool {
	if v, ok := msg.Value.(*messages.TextDeltaValue); ok && v != nil {
		select {
		case s.sentText <- v.Content:
		default:
		}
	}
	return true
}

func (s *integrationScriptedSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recv
}

func (s *integrationScriptedSession) Done() <-chan struct{} {
	return s.done
}

func (s *integrationScriptedSession) Close() error {
	s.once.Do(func() {
		close(s.done)
	})
	return nil
}
