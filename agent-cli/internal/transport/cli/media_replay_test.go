package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
	"github.com/spf13/cobra"
)

func replaySessionFixturePath(t *testing.T) string {
	t.Helper()
	return gatewaytesting.SharedSessionFixturePath("session_healthy_multiturn_audio.session.json")
}

func TestMediaProbeCommandReplayOptionCompletesObservationCycle(t *testing.T) {
	fixture := replaySessionFixturePath(t)
	command := NewMediaProbeCommandWithOptions(WithReplayFixture(fixture))
	var out bytes.Buffer
	if err := command.Run(context.Background(), &out, "go2rtc://unused-when-replaying"); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"Mode: replay",
		"Source: " + fixture,
		"Provider: grok",
		"Model: grok-4-healthy-multiturn",
		"Provenance: synthetic",
		"Inbound frames: 10",
		"Outbound ticks: 1",
		"Observation: 1 client_to_server session.update",
		"Observation: 2 server_to_client session.created",
		"Observation: 4 server_to_client response.audio.delta",
		"Observation: 11 server_to_client session.closed",
	}, "\n")
	for _, line := range strings.Split(want, "\n") {
		if !strings.Contains(out.String(), line+"\n") {
			t.Fatalf("replay report missing %q; report:\n%s", line, out.String())
		}
	}
}

func TestMediaProbeCommandReplayReportIsDeterministicAcrossRuns(t *testing.T) {
	fixture := replaySessionFixturePath(t)
	var first, second bytes.Buffer
	for _, out := range []*bytes.Buffer{&first, &second} {
		command := NewMediaProbeCommandWithOptions(WithReplayFixture(fixture))
		command.Timeout = time.Second
		if err := command.Run(context.Background(), out, "go2rtc://unused-when-replaying"); err != nil {
			t.Fatal(err)
		}
	}
	if first.String() != second.String() || first.Len() == 0 {
		t.Fatalf("replay reports diverged or empty:\n%s\n---\n%s", first.String(), second.String())
	}
}

func TestMediaProbeCommandReplayRejectsInvalidFixtureWithClearError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.session.json")
	invalid := `{"version":1,"provider":{"name":"grok","model":"m"},"records":[]}`
	if err := os.WriteFile(path, []byte(invalid), 0o644); err != nil {
		t.Fatal(err)
	}
	command := NewMediaProbeCommandWithOptions(WithReplayFixture(path))
	err := command.Run(context.Background(), &bytes.Buffer{}, "go2rtc://unused-when-replaying")
	if err == nil || !strings.Contains(err.Error(), "session fixture validation failed before any probe observation") {
		t.Fatalf("error = %v, want clear fixture validation failure", err)
	}
}

func TestMediaProbeCommandWithoutReplayOptionUsesLiveProbe(t *testing.T) {
	liveCalls := 0
	command := NewMediaProbeCommand(func(context.Context, string) (rtc.MediaCapabilities, error) {
		liveCalls++
		return rtc.MediaCapabilities{Source: "rtsp://camera:<redacted>@host/main", AudioCodec: "PCMU", SampleRate: 8000, Channels: 1}, nil
	})
	if command.ReplayFixture != "" {
		t.Fatalf("default ReplayFixture = %q, want empty (live default)", command.ReplayFixture)
	}
	var out bytes.Buffer
	if err := command.Run(context.Background(), &out, "rtsp://camera@host/main"); err != nil {
		t.Fatal(err)
	}
	if liveCalls != 1 || strings.Contains(out.String(), "Mode: replay") {
		t.Fatalf("live calls = %d, output = %q", liveCalls, out.String())
	}
}

func TestMediaProbeCLIReplayFlagProducesDeterministicReport(t *testing.T) {
	fixture := replaySessionFixturePath(t)
	var first, second bytes.Buffer
	for _, out := range []*bytes.Buffer{&first, &second} {
		command := NewMediaProbeCommand().Generate()
		command.SetOut(out)
		command.SetArgs([]string{"--replay-fixture", fixture, "go2rtc://unused-when-replaying"})
		if err := command.ExecuteContext(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if first.String() != second.String() || !strings.Contains(first.String(), "Mode: replay\n") || !strings.Contains(first.String(), "Inbound frames: 10\n") {
		t.Fatalf("CLI replay report not deterministic/complete:\n%s\n---\n%s", first.String(), second.String())
	}
}

func TestMediaProbeCLIDefaultInvocationUsesLivePath(t *testing.T) {
	command := NewMediaProbeCommand(func(context.Context, string) (rtc.MediaCapabilities, error) {
		return rtc.MediaCapabilities{Source: "stub-src", AudioCodec: "PCMU", SampleRate: 8000, Channels: 1}, nil
	}).Generate()
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetArgs([]string{"stub"})
	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Mode: replay") {
		t.Fatalf("default invocation used replay path: %q", out.String())
	}
}

type failAfterWriter struct {
	failAt int
	writes int
	err    error
}

func (w *failAfterWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes >= w.failAt {
		return 0, w.err
	}
	return len(p), nil
}

func executeChatWithWriters(t *testing.T, agentCLI *AgentCLI, args []string, input string, out, errOut io.Writer) error {
	t.Helper()
	original := chatInputIsInteractive
	chatInputIsInteractive = func(*cobra.Command) bool { return true }
	t.Cleanup(func() { chatInputIsInteractive = original })
	root := agentCLI.Generate()
	root.SetIn(strings.NewReader(input))
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetArgs(args)
	return root.ExecuteContext(context.Background())
}

func TestChatCommand_LoopWriterErrors(t *testing.T) {
	tests := []struct {
		name    string
		failAt  int
		args    []string
		input   string
		inf     *chatTestInferencer
		wantErr string
	}{
		{name: "header", failAt: 1, args: []string{"chat", "--loop"}, wantErr: "write chat header"},
		{name: "header separator", failAt: 2, args: []string{"chat", "--loop"}, wantErr: "write chat header separator"},
		{name: "task prompt", failAt: 3, args: []string{"chat", "--loop"}, wantErr: "write task prompt"},
		{name: "trace id", failAt: 4, args: []string{"chat", "--loop"}, input: "task\n", wantErr: "write trace ID"},
		{name: "iteration header", failAt: 5, args: []string{"chat", "--loop"}, input: "task\n", wantErr: "write iteration header"},
		{name: "iteration error", failAt: 6, args: []string{"chat", "--loop"}, input: "task\n", inf: &chatTestInferencer{callErr: errors.New("inference failed")}, wantErr: "write iteration error"},
		{name: "completion banner", failAt: 7, args: []string{"chat", "--loop", "--max-iterations", "2", "--stop-word", "DONE"}, input: "task\n", inf: &chatTestInferencer{response: "DONE"}, wantErr: "write completion banner"},
		{name: "steering prompt", failAt: 7, args: []string{"chat", "--loop", "--max-iterations", "2"}, input: "task\n", inf: &chatTestInferencer{response: "continue"}, wantErr: "write steering prompt"},
		{name: "loop completion banner", failAt: 7, args: []string{"chat", "--loop", "--max-iterations", "1"}, input: "task\n", inf: &chatTestInferencer{response: "continue"}, wantErr: "write loop completion banner"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inf := tt.inf
			if inf == nil {
				inf = &chatTestInferencer{response: "unused"}
			}
			out := &failAfterWriter{failAt: tt.failAt, err: errors.New("output failed")}
			var errOut bytes.Buffer
			err := executeChatWithWriters(t, newTestAgentCLI(t, inf), tt.args, tt.input, out, &errOut)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestChatCommand_AudioWriterErrors(t *testing.T) {
	tests := []struct {
		name       string
		failAt     int
		wantErr    string
		withSpeech bool
	}{
		{name: "audio header", failAt: 1, wantErr: "write audio chat header"},
		{name: "audio header separator", failAt: 2, wantErr: "write audio chat header separator"},
		{name: "listening status", failAt: 3, wantErr: "write listening status"},
		{name: "speech status", failAt: 4, wantErr: "write speech detected status", withSpeech: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			textService := newChatTestSessionService(&chatTestInferencer{response: "audio response"}, nil)
			global := flags.NewGlobalFlags()
			global.ConfigDirPath = t.TempDir()
			ask := flags.NewAskFlags()
			ask.NoSystemInformation = true
			samples := []int16(nil)
			if tt.withSpeech {
				samples = make([]int16, audio.FrameSize*(3+audio.DefaultVADConfig.MaxSilenceFrames))
				for i := 0; i < audio.FrameSize*3; i++ {
					samples[i] = 1000
				}
			}
			out := &failAfterWriter{failAt: tt.failAt, err: errors.New("audio output failed")}
			var errOut bytes.Buffer
			err := RunChatWithAudio(context.Background(), out, &errOut, textService, global, ask, audio.NewSliceSource(samples))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

type scriptedAudioSource struct {
	steps []error
	index int
}

func (s *scriptedAudioSource) ReadFrame(_ context.Context, _ []int16) error {
	if s.index >= len(s.steps) {
		return io.EOF
	}
	err := s.steps[s.index]
	s.index++
	return err
}

func (*scriptedAudioSource) Close() error { return nil }

func TestChatCommand_AudioHelperProcessesSpeechAndReportsPipelineErrors(t *testing.T) {
	t.Run("speech dispatches once", func(t *testing.T) {
		testAudioSpeechDispatch(t)
	})

	t.Run("pipeline error is reported and loop continues", func(t *testing.T) {
		testAudioPipelineError(t)
	})

	t.Run("context cancellation says goodbye", func(t *testing.T) {
		testAudioContextCancellation(t)
	})
}

func testAudioSpeechDispatch(t *testing.T) {
	t.Helper()
	inf := &chatTestInferencer{response: "audio response"}
	textService := newChatTestSessionService(inf, nil)
	global := flags.NewGlobalFlags()
	global.ConfigDirPath = t.TempDir()
	ask := flags.NewAskFlags()
	ask.NoSystemInformation = true
	samples := make([]int16, audio.FrameSize*(3+audio.DefaultVADConfig.MaxSilenceFrames))
	for i := 0; i < audio.FrameSize*3; i++ {
		samples[i] = 1000
	}
	var out, errOut bytes.Buffer
	if err := RunChatWithAudio(context.Background(), &out, &errOut, textService, global, ask, audio.NewSliceSource(samples)); err != nil {
		t.Fatalf("RunChatWithAudio() error = %v", err)
	}
	if inf.calls != 1 {
		t.Fatalf("inferencer calls = %d, want 1; stdout=%q stderr=%q", inf.calls, out.String(), errOut.String())
	}
	for _, want := range []string{"Audio Mode", "(speech detected, processing...)", "Goodbye!"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("stdout = %q, want substring %q", out.String(), want)
		}
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", errOut.String())
	}
}

func testAudioPipelineError(t *testing.T) {
	t.Helper()
	textService := newChatTestSessionService(&chatTestInferencer{}, nil)
	global := flags.NewGlobalFlags()
	global.ConfigDirPath = t.TempDir()
	ask := flags.NewAskFlags()
	ask.NoSystemInformation = true
	var out, errOut bytes.Buffer
	sourceErr := errors.New("frame failed")
	if err := RunChatWithAudio(context.Background(), &out, &errOut, textService, global, ask, &scriptedAudioSource{steps: []error{sourceErr, io.EOF}}); err != nil {
		t.Fatalf("RunChatWithAudio() error = %v", err)
	}
	if got, want := errOut.String(), "Audio pipeline error: read audio frame: frame failed\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
	if !strings.Contains(out.String(), "Goodbye!") {
		t.Fatalf("stdout = %q, want Goodbye", out.String())
	}
}

func testAudioContextCancellation(t *testing.T) {
	t.Helper()
	textService := newChatTestSessionService(&chatTestInferencer{}, nil)
	global := flags.NewGlobalFlags()
	global.ConfigDirPath = t.TempDir()
	ask := flags.NewAskFlags()
	ask.NoSystemInformation = true
	var out, errOut bytes.Buffer
	if err := RunChatWithAudio(context.Background(), &out, &errOut, textService, global, ask, &scriptedAudioSource{steps: []error{context.Canceled}}); err != nil {
		t.Fatalf("RunChatWithAudio() error = %v", err)
	}
	if !strings.Contains(out.String(), "Goodbye!") || errOut.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q, want goodbye and empty stderr", out.String(), errOut.String())
	}
}

type unadmittedFileRunner struct {
	runtimeSession.LiveService
	cause error
}

func (r unadmittedFileRunner) RunLive(context.Context, runtimeSession.LiveRunOptions) error {
	return r.cause
}

func TestLiveHostReportsUnadmittedWAVFinalizationFailure(t *testing.T) {
	cause := errors.New("provider admission failed")
	for _, runErr := range []error{nil, cause} {
		root := t.TempDir()
		command := &SessionCommand{liveService: unadmittedFileRunner{cause: runErr}}
		request := serviceSession.Request{
			LoadedConfig: &config.Config{}, Provider: config.ProviderOpenAI, APIKey: "inert-fixture-key", WorkDir: root,
			AudioOutputPath: filepath.Join(root, "output.wav"),
		}
		err := command.runRuntimeLiveSession(t.Context(), io.Discard, request)
		if !errors.Is(err, wavio.ErrEmptySamples) {
			t.Fatalf("WAV finalization failure lost: %v", err)
		}
		if runErr != nil && !errors.Is(err, runErr) {
			t.Fatalf("invocation failure lost during file cleanup: %v", err)
		}
	}
}
