package wire

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

func TestSelfPlayServiceBridgesOnlyPCMAndWritesBoundedEvidence(t *testing.T) {
	provider := newTestSessionService(t, "")
	service := NewService(NewDependencies(provider, testModelCatalog{}, clock.Real{}))
	outputDir := filepath.Join(t.TempDir(), "run")
	result, err := service.Run(context.Background(), selfplay.Request{
		APIKey:      "selfplay-test-key",
		OutputDir:   outputDir,
		MaxDuration: 10 * time.Second,
		MaxTurns:    1,
	})
	assertTurnTargetResult(t, result, err)

	configs, sessions := provider.snapshot()
	assertSessionSetup(t, configs, sessions)
	assertSessionTraffic(t, sessions)
	assertOneTurnPerSide(t, outputDir)
}

func TestSelfPlayServiceHonorsConfiguredTurnTarget(t *testing.T) {
	provider := newTestSessionService(t, "")
	service := NewService(NewDependencies(provider, testModelCatalog{}, clock.Real{}))
	outputDir := filepath.Join(t.TempDir(), "run")
	result, err := service.Run(context.Background(), selfplay.Request{
		APIKey:      "selfplay-test-key",
		OutputDir:   outputDir,
		MaxDuration: 10 * time.Second,
		MaxTurns:    2,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.StopReason != selfplay.StopTurnTarget || result.Customer.CompletedTurns != 2 || result.Assistant.CompletedTurns != 2 {
		t.Fatalf("two-turn result = %#v", result)
	}
	_, sessions := provider.snapshot()
	assertSessionTraffic(t, sessions)
	assertCompletedTurnsPerSide(t, outputDir, 2)
}

func assertTurnTargetResult(t *testing.T, result selfplay.Result, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.StopReason != selfplay.StopTurnTarget || result.Customer.CompletedTurns != 1 || result.Assistant.CompletedTurns != 1 {
		t.Fatalf("turn-target result = %#v", result)
	}
	artifacts := []selfplay.ArtifactOutcome{
		result.Manifest, result.Customer.WAV, result.Customer.Diagnostics, result.Customer.StreamDeltas,
		result.Assistant.WAV, result.Assistant.Diagnostics, result.Assistant.StreamDeltas,
	}
	for _, artifact := range artifacts {
		if !artifact.Complete {
			t.Fatalf("incomplete result artifact: %#v", artifact)
		}
	}
}

func assertSessionSetup(t *testing.T, configs []providers.SessionConfig, sessions []*testSession) {
	t.Helper()
	if len(configs) != 2 || len(sessions) != 2 {
		t.Fatalf("built %d sessions with %d configs, want two", len(sessions), len(configs))
	}
	if configs[0].Provider != "openai" || configs[0].Model != "gpt-realtime" || configs[0].Instructions != "You are the customer. Speak naturally, briefly, and only as part of a spoken conversation. Ask one practical follow-up at a time. Do not call tools." || configs[1].Instructions != "You are the helpful assistant. Speak naturally, briefly, and only as part of a spoken conversation. Answer the customer's latest request and ask one concise follow-up when useful. Do not call tools." {
		t.Fatalf("default provider/model/personas = %#v", configs)
	}
	for _, config := range configs {
		if config.InputAudioFormat != models.AudioFormatPCM16 || config.OutputAudioFormat != models.AudioFormatPCM16 || config.InputAudioSampleRate != models.SampleRate24000 || config.OutputAudioSampleRate != models.SampleRate24000 || len(config.Tools) != 0 {
			t.Fatalf("session config did not enforce PCM16/24kHz and no tools: %#v", config)
		}
	}
}

func assertSessionTraffic(t *testing.T, sessions []*testSession) {
	t.Helper()
	customerSent := sessions[0].sentMessages()
	assistantSent := sessions[1].sentMessages()
	if countOutboundText(customerSent, "Hi, I need help planning a simple weekend trip.") != 1 || countOutboundText(assistantSent, "") != 0 {
		t.Fatalf("opening text isolation failed: customer=%#v assistant=%#v", customerSent, assistantSent)
	}
	if !containsOutboundAudio(customerSent, testAssistantPCM()) || !containsOutboundAudio(assistantSent, testCustomerPCM()) || containsOutboundAudio(customerSent, testCustomerPCM()) || containsOutboundAudio(assistantSent, testAssistantPCM()) {
		t.Fatalf("PCM was not bridged only in the opposite direction: customer=%#v assistant=%#v", customerSent, assistantSent)
	}
}

func assertOneTurnPerSide(t *testing.T, outputDir string) {
	t.Helper()
	assertCompletedTurnsPerSide(t, outputDir, 1)
}

func assertCompletedTurnsPerSide(t *testing.T, outputDir string, want int) {
	t.Helper()
	for _, path := range []string{"agent-a-diagnostics.jsonl", "agent-b-diagnostics.jsonl"} {
		data, err := os.ReadFile(filepath.Join(outputDir, path))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if got := strings.Count(string(data), `"event":"turn_completed"`); got != want {
			t.Fatalf("%s has %d admitted turn records, want exactly %d", path, got, want)
		}
	}
}

func TestSelfPlayServiceRejectsUnsupportedModelBeforeOpeningOutputOrSession(t *testing.T) {
	provider := newTestSessionService(t, "")
	service := NewService(NewDependencies(provider, testModelCatalog{allow: false}, clock.Real{}))
	outputDir := filepath.Join(t.TempDir(), "run")
	_, err := service.Run(context.Background(), selfplay.Request{APIKey: "selfplay-test-key", OutputDir: outputDir, Model: "not-realtime"})
	if !errors.Is(err, selfplay.ErrUnsupportedModel) {
		t.Fatalf("Run error = %v, want unsupported model", err)
	}
	if _, err := os.Stat(outputDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output target was touched before model admission: stat error = %v", err)
	}
	configs, _ := provider.snapshot()
	if len(configs) != 0 {
		t.Fatalf("opened %d sessions before model admission", len(configs))
	}
}

func TestSelfPlayServiceRedactsProviderFailureFromReturnedEvidence(t *testing.T) {
	const secret = "selfplay-test-secret"
	provider := newTestSessionService(t, "authorization: Bearer "+secret)
	service := NewService(NewDependencies(provider, testModelCatalog{}, clock.Real{}))
	outputDir := filepath.Join(t.TempDir(), "run")
	result, err := service.Run(context.Background(), selfplay.Request{
		APIKey:      secret,
		OutputDir:   outputDir,
		MaxDuration: 10 * time.Second,
		MaxTurns:    1,
	})
	if err == nil || !strings.Contains(err.Error(), "[REDACTED]") || strings.Contains(err.Error(), secret) {
		t.Fatalf("provider failure = %v, want a redacted error", err)
	}
	manifest, readErr := os.ReadFile(filepath.Join(outputDir, "run-manifest.json"))
	if readErr != nil {
		t.Fatalf("read failure manifest: %v", readErr)
	}
	if strings.Contains(string(manifest), secret) || !strings.Contains(string(manifest), "[REDACTED]") {
		t.Fatalf("failure manifest did not redact credentials: %s", manifest)
	}
	if result.StopReason != selfplay.StopFailure || !result.Manifest.Complete {
		t.Fatalf("failure result = %#v", result)
	}
	if result.Customer.Terminal != selfplay.SideFailed && result.Assistant.Terminal != selfplay.SideFailed {
		t.Fatalf("provider failure did not mark a side failed: customer=%#v assistant=%#v", result.Customer, result.Assistant)
	}
	for _, side := range []selfplay.SideResult{result.Customer, result.Assistant} {
		if strings.Contains(side.TerminalError, secret) {
			t.Fatalf("side terminal error leaked credential: %q", side.TerminalError)
		}
	}
}

func TestSelfPlayServicePreservesFirstMeaningfulFailure(t *testing.T) {
	service := NewService(NewDependencies(&firstFailureSessionService{}, testModelCatalog{}, clock.Real{}))
	result, err := service.Run(context.Background(), selfplay.Request{
		APIKey:      "selfplay-test-key",
		OutputDir:   filepath.Join(t.TempDir(), "run"),
		MaxDuration: 10 * time.Second,
		MaxTurns:    1,
	})
	if err == nil || !strings.Contains(err.Error(), "primary customer failure") || strings.Contains(err.Error(), "secondary assistant failure") {
		t.Fatalf("Run error = %v, want only the first customer failure", err)
	}
	if result.StopReason != selfplay.StopFailure || !strings.Contains(result.Customer.TerminalError, "primary customer failure") {
		t.Fatalf("first failure result = %#v", result)
	}
}

func TestSelfPlayServiceMaxDurationStopsOpenSessionsCleanly(t *testing.T) {
	provider := newTestSessionService(t, "")
	provider.silent = true
	service := NewService(Dependencies{SessionService: provider, ModelCatalog: testModelCatalog{}, Clock: clock.Real{}})
	started := time.Now()
	result, err := service.Run(context.Background(), selfplay.Request{APIKey: "selfplay-test-key", OutputDir: filepath.Join(t.TempDir(), "run"), MaxDuration: 100 * time.Millisecond, MaxTurns: 2})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.StopReason != selfplay.StopMaxDuration || result.Customer.CompletedTurns != 0 || result.Assistant.CompletedTurns != 0 {
		t.Fatalf("duration result = %#v", result)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("duration stop took %s, want under 2s", elapsed)
	}
	if !result.Manifest.Complete {
		t.Fatalf("duration result has no complete manifest: %#v", result.Manifest)
	}
}

func TestSelfPlayServiceCallerCancellationStopsBothSides(t *testing.T) {
	provider := newTestSessionService(t, "")
	provider.silent = true
	service := NewService(Dependencies{SessionService: provider, ModelCatalog: testModelCatalog{}, Clock: clock.Real{}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		result selfplay.Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := service.Run(ctx, selfplay.Request{APIKey: "selfplay-test-key", OutputDir: filepath.Join(t.TempDir(), "run"), MaxDuration: 10 * time.Second, MaxTurns: 2})
		done <- outcome{result: result, err: err}
	}()
	for range 2 {
		select {
		case <-provider.connected:
		case <-time.After(2 * time.Second):
			t.Fatal("both provider sessions did not open")
		}
	}
	cancel()
	select {
	case finished := <-done:
		if !errors.Is(finished.err, context.Canceled) || finished.result.StopReason != selfplay.StopFailure {
			t.Fatalf("cancellation result = %#v, error = %v", finished.result, finished.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop promptly after caller cancellation")
	}
	_, sessions := provider.snapshot()
	for index, session := range sessions {
		select {
		case <-session.Done():
		case <-time.After(time.Second):
			t.Errorf("provider session %d remained open after cancellation (Close called: %v)", index, session.wasClosed())
		}
	}
}

func TestSelfPlayServiceReportsBoundedShutdownTimeout(t *testing.T) {
	releaseConnect := make(chan struct{})
	var releaseOnce sync.Once
	unblockConnect := func() { releaseOnce.Do(func() { close(releaseConnect) }) }
	defer unblockConnect()
	provider := &testSessionService{
		silent:          true,
		connected:       make(chan struct{}, 2),
		connectEntered:  make(chan struct{}, 2),
		connectRelease:  releaseConnect,
		connectReturned: make(chan struct{}, 2),
	}
	service := NewService(Dependencies{SessionService: provider, ModelCatalog: testModelCatalog{}, Clock: fastShutdownClock{}})
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		result selfplay.Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := service.Run(ctx, selfplay.Request{APIKey: "selfplay-test-key", OutputDir: filepath.Join(t.TempDir(), "run"), MaxDuration: 30 * time.Second, MaxTurns: 2})
		done <- outcome{result: result, err: err}
	}()
	for range 2 {
		select {
		case <-provider.connectEntered:
		case <-time.After(time.Second):
			t.Fatal("both provider connects did not enter")
		}
	}
	cancel()
	select {
	case finished := <-done:
		unblockConnect()
		if !errors.Is(finished.err, selfplay.ErrShutdownTimeout) {
			t.Fatalf("Run error = %v, want bounded shutdown timeout", finished.err)
		}
		if finished.result.StopReason != selfplay.StopFailure {
			t.Fatalf("shutdown-timeout result = %#v", finished.result)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return within the bounded shutdown interval")
	}
	for range 2 {
		select {
		case <-provider.connectReturned:
		case <-time.After(time.Second):
			t.Error("provider connect did not finish after the test gate was released")
		}
	}
}

func TestSelfPlayServiceRejectsInvalidRequestsBeforeSideEffects(t *testing.T) {
	provider := newTestSessionService(t, "")
	service := NewService(Dependencies{SessionService: provider, ModelCatalog: testModelCatalog{}, Clock: clock.Real{}})
	missingCredentialDir := filepath.Join(t.TempDir(), "missing-credential")
	for _, testCase := range []struct {
		name    string
		request selfplay.Request
		want    error
	}{
		{name: "provider", request: selfplay.Request{Provider: "other", OutputDir: filepath.Join(t.TempDir(), "provider")}, want: selfplay.ErrUnsupportedProvider},
		{name: "duration", request: selfplay.Request{OutputDir: filepath.Join(t.TempDir(), "duration"), MaxDuration: -time.Second}, want: selfplay.ErrInvalidRequest},
		{name: "turns", request: selfplay.Request{OutputDir: filepath.Join(t.TempDir(), "turns"), MaxTurns: -1}, want: selfplay.ErrInvalidRequest},
		{name: "output", request: selfplay.Request{MaxTurns: 1}, want: selfplay.ErrInvalidRequest},
		{name: "credential", request: selfplay.Request{Provider: "openai", Model: "gpt-realtime", OutputDir: missingCredentialDir}, want: selfplay.ErrCredentialRequired},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := service.Run(context.Background(), testCase.request)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("Run error = %v, want %v", err, testCase.want)
			}
			if testCase.name == "credential" {
				if _, err := os.Stat(missingCredentialDir); !os.IsNotExist(err) {
					t.Fatalf("missing-credential request created its output directory: stat error = %v", err)
				}
			}
		})
	}
	unsafeDir := filepath.Join(t.TempDir(), "unsafe")
	if err := os.Mkdir(unsafeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(unsafeDir, "keep.txt")
	if err := os.WriteFile(markerPath, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := service.Run(context.Background(), selfplay.Request{APIKey: "selfplay-test-key", OutputDir: unsafeDir, MaxTurns: 1})
	if !errors.Is(err, selfplay.ErrOutputTargetUnsafe) {
		t.Fatalf("non-empty output error = %v, want unsafe output", err)
	}
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("unsafe output validation removed existing data: %v", err)
	}
	configs, _ := provider.snapshot()
	if len(configs) != 0 {
		t.Fatalf("built %d provider sessions for invalid requests", len(configs))
	}
}

type testModelCatalog struct{ allow bool }

type fastShutdownClock struct{}

func (fastShutdownClock) Now() time.Time { return time.Now() }

func (fastShutdownClock) NewTimer(duration time.Duration) clock.Timer {
	if duration == 5*time.Second {
		duration = 25 * time.Millisecond
	}
	return clock.Real{}.NewTimer(duration)
}

func (c testModelCatalog) RealtimeModels(string) []providers.RealtimeModel { return nil }
func (c testModelCatalog) SupportedRealtimeModelIDs(string) []string       { return nil }
func (c testModelCatalog) LookupRealtimeModel(provider, model string) (providers.RealtimeModel, bool) {
	if !c.allow && (provider != "openai" || model != "gpt-realtime") {
		return providers.RealtimeModel{}, false
	}
	return providers.RealtimeModel{ID: model, SupportsAudio: true}, true
}
