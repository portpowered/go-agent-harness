package cli

import (
	"context"
	"errors"
	"fmt"
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"strings"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audioiowire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio/wire"
	runtimedeviceswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices/wire"
	runtimeProviders "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	providerswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/wire"
	runtimeRecording "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	runtimeRecordingWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording/wire"
	runtimeReplayWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/wire"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	runtimeSessionWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	runtimeSessionTraceWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// testSessionDeps are the per-test seams of a composed session command. A
// nil Inferencer builds provider sessions through the provider service, as
// production composition does; a non-nil value replaces provider
// construction exactly like the application's session-inferencer port.
type testSessionDeps struct {
	Inferencer     messages.SessionInferencer
	Registry       devicegw.DeviceRegistry
	Capabilities   SessionToolCapabilitiesFactory
	ModelAdmission runtimeProviders.ModelAdmission
	// Dialer optionally replaces the provider transport for provider-built
	// sessions, like the application's transport-dialer port.
	Dialer transport.Dialer
}

// newTestSessionCommand composes the session command with the same live,
// replay, recording, provider, and device services as the application graph.
func newTestSessionCommand(askFlags *flags.AskFlags, globalFlags *flags.GlobalFlags, deps testSessionDeps) *SessionCommand {
	if askFlags == nil {
		askFlags = flags.NewAskFlags()
	}
	if globalFlags == nil {
		globalFlags = flags.NewGlobalFlags()
	}
	clockSource := clock.Real{}
	audioService := audioiowire.NewService()
	replayService := runtimeReplayWire.NewService()
	recordingService := runtimeRecordingWire.NewService(clockSource)
	providerService := providerswire.NewService(providerswire.Dependencies{
		Recording: recordingService, ProviderCapture: runtimeRecordingWire.NewProviderCaptureService(clockSource),
		Replay: replayService, Clock: clockSource,
	})
	credentials := &testCredentialVault{values: make(map[string]string)}
	liveService := runtimeSessionWire.NewLiveService(runtimeSessionWire.LiveDependencies{
		InferencerFactory: testLiveInferencerFactory(deps, providerService, recordingService, credentials),
		Clock:             clockSource.Now,
		Scheduler:         clockSource,
	})
	return NewSessionCommandWithLive(
		askFlags, globalFlags, nil,
		liveService, replayService, runtimedeviceswire.NewService(deps.Registry, audioService),
		FileDeviceService{Service: runtimedeviceswire.NewFileService(audioService), Scheduler: clockSource, TraceService: runtimeSessionTraceWire.NewService()},
		deps.Capabilities, credentials.put, nil, recordingService, deps.ModelAdmission,
	)
}

// ambiguousCubeStateTool names the cube-state page tool the ambiguous page-tool
// conversations select.
const ambiguousCubeStateTool = "get_cube_state"

func newTestReplaySessionCommand(globalFlags *flags.GlobalFlags, registry devicegw.DeviceRegistry) *SessionCommand {
	return newTestSessionCommand(nil, globalFlags, testSessionDeps{Registry: registry})
}

func newTestLiveSessionCommand(askFlags *flags.AskFlags, globalFlags *flags.GlobalFlags, inferencer messages.SessionInferencer, registry devicegw.DeviceRegistry, capabilities ...SessionToolCapabilitiesFactory) *SessionCommand {
	deps := testSessionDeps{Inferencer: inferencer, Registry: registry}
	if len(capabilities) > 0 {
		deps.Capabilities = capabilities[0]
	}
	return newTestSessionCommand(askFlags, globalFlags, deps)
}

func testLiveInferencerFactory(deps testSessionDeps, providers runtimeProviders.SessionService, recording runtimeRecording.Service, credentials *testCredentialVault) runtimeSession.LiveInferencerFactory {
	injected := deps.Inferencer
	if injected == nil {
		return runtimeSessionWire.NewProviderInferencerFactory(runtimeSessionWire.ProviderInferenceDependencies{
			Providers: providers, Credentials: credentials.take, Dialer: deps.Dialer,
		})
	}
	return func(_ context.Context, request runtimeSession.LiveRequest) (messages.SessionInferencer, error) {
		path := strings.TrimSpace(request.Replay.OutputCapturePath)
		if path == "" || !request.Replay.InjectedCaptureAllowed {
			return injected, nil
		}
		return recording.TrackInjectedSession(injected, path)
	}
}

// testCredentialVault mirrors the application's one-time credential vault so
// raw keys never enter a live request.
type testCredentialVault struct {
	mu     sync.Mutex
	values map[string]string
	next   int
}

func (v *testCredentialVault) put(value string) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.next++
	reference := fmt.Sprintf("test-credential:%d", v.next)
	v.values[reference] = value
	return reference
}

func (v *testCredentialVault) take(_ context.Context, reference string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	value, ok := v.values[reference]
	if !ok {
		return "", errors.New("test credential reference is unavailable")
	}
	return value, nil
}

// testLiveConversation is one live handle driven directly by a test that
// must inject customer turns while the provider conversation is in flight.
type testLiveConversation struct {
	handle      runtimeSession.LiveHandle
	output      *strings.Builder
	runErr      chan error
	runComplete chan struct{}
}

// startTestLiveConversation builds the command's live request, opens the
// live handle, and starts it. Assistant text and customer transcripts are
// rendered into output as the CLI transcript does.
func startTestLiveConversation(t *testing.T, ctx context.Context, deps testSessionDeps, request serviceSession.Request) testLiveConversation {
	t.Helper()
	command := newTestSessionCommand(nil, nil, deps)
	liveRequest, err := command.runtimeLiveRequest(ctx, request, nil)
	if err != nil {
		t.Fatalf("build live conversation request: %v", err)
	}
	handle, err := command.liveService.OpenLive(ctx, liveRequest)
	if err != nil {
		t.Fatalf("open live conversation: %v", err)
	}
	t.Cleanup(func() {
		if err := handle.Close(); err != nil {
			t.Logf("close live conversation: %v", err)
		}
	})
	conversation := testLiveConversation{handle: handle, output: &strings.Builder{}, runErr: make(chan error, 1), runComplete: make(chan struct{})}
	transcript := collectLiveTranscript(handle, conversation.output)
	go func() {
		err := handle.Start(ctx)
		if err == nil {
			err = handle.Wait()
		}
		<-transcript
		conversation.runErr <- err
		close(conversation.runComplete)
	}()
	return conversation
}

// commitCustomerTurn commits the customer's spoken turn through the live
// handle's ordered control ingress.
func (c testLiveConversation) commitCustomerTurn(t *testing.T, ctx context.Context) {
	t.Helper()
	if err := c.handle.Send(ctx, runtimeSession.LiveControl{Kind: runtimeSession.LiveControlAudioCommit}); err != nil {
		t.Fatalf("commit the customer's spoken turn: %v", err)
	}
}

// collectLiveTranscript renders assistant text and customer transcripts from
// a live handle and closes the returned channel once the event stream ends.
func collectLiveTranscript(handle runtimeSession.LiveHandle, output *strings.Builder) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for event := range handle.Events() {
			if event.Message == nil {
				continue
			}
			switch value := event.Message.Value.(type) {
			case *messages.TextDeltaValue:
				if value != nil {
					output.WriteString(value.Content)
				}
			case *messages.TranscriptEndValue:
				if value != nil {
					output.WriteString(value.FullText)
				}
			}
		}
	}()
	return done
}

type chatFlagMatrixCase struct {
	name            string
	args            []string
	input           string
	wantExit        int
	wantOutput      string
	wantOutputParts []string
	wantErrorPart   string
	wantFlagParse   bool
	wantInferCalls  int
	checkFlags      func(*testing.T, *flags.ChatFlags, *flags.LoopFlags)
}

const chatTextCancelOutput = "Port OS Agent Chat (type 'exit' or 'quit' to end)\n---\n\x1b[?25l\x1b[?2004h\r \r\x1b[2K\r\x1b[?2004l\x1b[?25h\x1b[?1002l\x1b[?1003l\x1b[?1006l"
const chatAudioEOFOutput = "Port OS Agent Chat - Audio Mode (Ctrl+C to exit)\n---\n\nListening...\nGoodbye!\n"

func TestChatCommand_FlagMatrix(t *testing.T) {
	for _, makeCases := range []func() []chatFlagMatrixCase{
		chatFlagMatrixAudioCases,
		chatFlagMatrixValidationCases,
		chatFlagMatrixParseCases,
	} {
		for _, tt := range makeCases() {
			tt := tt
			t.Run(tt.name, func(t *testing.T) {
				runChatFlagMatrixCase(t, tt)
			})
		}
	}
}

func runChatFlagMatrixCase(t *testing.T, tt chatFlagMatrixCase) {
	t.Helper()
	inf := &chatTestInferencer{response: "matrix response"}
	agentCLI, chatFlags, loopFlags := newTestAgentCLIAtWithFlags(t, inf, t.TempDir())
	got := executeInteractiveRoot(t, agentCLI, tt.args, tt.input)
	if got.exitCode != tt.wantExit {
		t.Fatalf("exit code = %d, want %d (err=%v)", got.exitCode, tt.wantExit, got.err)
	}
	if tt.wantErrorPart == "" {
		assertChatFlagMatrixSuccess(t, tt, got, inf, chatFlags, loopFlags)
		return
	}
	if got.err == nil || !strings.Contains(got.err.Error(), tt.wantErrorPart) {
		t.Fatalf("error = %v, want substring %q", got.err, tt.wantErrorPart)
	}
	if !tt.wantFlagParse {
		if got.stdout != "" || got.stderr != "" {
			t.Fatalf("local validation output = stdout %q stderr %q, want both empty", got.stdout, got.stderr)
		}
		if tt.checkFlags != nil {
			tt.checkFlags(t, chatFlags, loopFlags)
		}
		return
	}
	var typed *chatFlagParseError
	if !errors.As(got.err, &typed) {
		t.Fatalf("error type = %T, want *chatFlagParseError", got.err)
	}
	if !strings.Contains(got.stdout, "Usage:\n  yui chat [flags]") {
		t.Fatalf("stdout = %q, want Cobra usage on flag failure", got.stdout)
	}
	if got.stderr != "Error: "+got.err.Error()+"\n" {
		t.Fatalf("stderr = %q, want exact Cobra error channel", got.stderr)
	}
}

func assertChatFlagMatrixSuccess(t *testing.T, tt chatFlagMatrixCase, got chatRun, inf *chatTestInferencer, chatFlags *flags.ChatFlags, loopFlags *flags.LoopFlags) {
	t.Helper()
	if got.err != nil {
		t.Fatalf("ExecuteContext() error = %v", got.err)
	}
	if got.stderr != "" {
		t.Fatalf("stderr = %q, want empty", got.stderr)
	}
	if tt.wantOutput != "" && got.stdout != tt.wantOutput {
		t.Fatalf("stdout = %q, want %q", got.stdout, tt.wantOutput)
	}
	for _, want := range tt.wantOutputParts {
		if !strings.Contains(got.stdout, want) {
			t.Fatalf("stdout = %q, want substring %q", got.stdout, want)
		}
	}
	if tt.wantInferCalls >= 0 && inf.calls != tt.wantInferCalls {
		t.Fatalf("inferencer calls = %d, want %d", inf.calls, tt.wantInferCalls)
	}
	if tt.checkFlags != nil {
		tt.checkFlags(t, chatFlags, loopFlags)
	}
}

func chatFlagMatrixAudioCases() []chatFlagMatrixCase {
	return []chatFlagMatrixCase{
		{
			name:       "activate audio input alone",
			args:       []string{"chat", "--activate-audio-in"},
			wantExit:   0,
			wantOutput: chatAudioEOFOutput,
			checkFlags: func(t *testing.T, chatFlags *flags.ChatFlags, _ *flags.LoopFlags) {
				if !chatFlags.ActivateAudioIn {
					t.Fatal("ActivateAudioIn = false, want true")
				}
			},
		},
		{
			name:       "activate audio output alone",
			args:       []string{"chat", "--activate-audio-out"},
			input:      "\x03",
			wantExit:   0,
			wantOutput: chatTextCancelOutput,
			checkFlags: func(t *testing.T, chatFlags *flags.ChatFlags, _ *flags.LoopFlags) {
				if !chatFlags.ActivateAudioOut {
					t.Fatal("ActivateAudioOut = false, want true")
				}
			},
		},
		{
			name:            "loop alone",
			args:            []string{"chat", "--loop"},
			input:           "task\n",
			wantExit:        0,
			wantOutputParts: []string{"Port OS Agent Loop Chat (up to 5 iterations)", "Loop complete: 5 iteration(s)"},
			wantInferCalls:  5,
			checkFlags: func(t *testing.T, _ *flags.ChatFlags, loopFlags *flags.LoopFlags) {
				if !loopFlags.Loop || loopFlags.MaxIterations != 5 {
					t.Fatalf("loop flags = %+v, want Loop=true MaxIterations=5", *loopFlags)
				}
			},
		},
		{
			name:          "max iterations alone",
			args:          []string{"chat", "--max-iterations", "2"},
			input:         "\x03",
			wantExit:      1,
			wantErrorPart: "--max-iterations requires --loop",
			checkFlags: func(t *testing.T, _ *flags.ChatFlags, loopFlags *flags.LoopFlags) {
				if loopFlags.MaxIterations != 2 {
					t.Fatalf("MaxIterations = %d, want 2", loopFlags.MaxIterations)
				}
			},
		},
		{
			name:          "stop word alone",
			args:          []string{"chat", "--stop-word", "DONE"},
			input:         "\x03",
			wantExit:      1,
			wantErrorPart: "--stop-word requires --loop",
			checkFlags: func(t *testing.T, _ *flags.ChatFlags, loopFlags *flags.LoopFlags) {
				if loopFlags.StopWord != "DONE" {
					t.Fatalf("StopWord = %q, want %q", loopFlags.StopWord, "DONE")
				}
			},
		},
		{
			name:          "context pressure threshold alone",
			args:          []string{"chat", "--context-pressure-threshold", "0.4"},
			input:         "\x03",
			wantExit:      1,
			wantErrorPart: "--context-pressure-threshold requires --loop",
			checkFlags: func(t *testing.T, _ *flags.ChatFlags, loopFlags *flags.LoopFlags) {
				if loopFlags.ContextPressureThreshold != 0.4 {
					t.Fatalf("ContextPressureThreshold = %v, want 0.4", loopFlags.ContextPressureThreshold)
				}
			},
		},
	}
}

func chatFlagMatrixValidationCases() []chatFlagMatrixCase {
	return []chatFlagMatrixCase{
		{
			name:          "negative max iterations with loop is rejected",
			args:          []string{"chat", "--loop", "--max-iterations", "-5"},
			wantExit:      1,
			wantErrorPart: "--max-iterations must be a positive integer, got -5",
		},
		{
			name:          "negative context pressure threshold with loop is rejected",
			args:          []string{"chat", "--loop", "--context-pressure-threshold", "-3"},
			wantExit:      1,
			wantErrorPart: "--context-pressure-threshold must be greater than 0 and at most 1",
		},
		{
			name:          "context pressure threshold above 1 with loop is rejected",
			args:          []string{"chat", "--loop", "--context-pressure-threshold", "5000"},
			wantExit:      1,
			wantErrorPart: "--context-pressure-threshold must be greater than 0 and at most 1",
		},
		{
			name:            "in-range loop flags with loop are accepted",
			args:            []string{"chat", "--loop", "--max-iterations", "2", "--context-pressure-threshold", "0.5"},
			input:           "task\nsteer\n",
			wantExit:        0,
			wantOutputParts: []string{"Port OS Agent Loop Chat (up to 2 iterations)", "Loop complete: 2 iteration(s)"},
			wantInferCalls:  2,
		},
		{
			name:          "context pressure message alone",
			args:          []string{"chat", "--context-pressure-message", "warning"},
			input:         "\x03",
			wantExit:      1,
			wantErrorPart: "--context-pressure-message requires --loop",
			checkFlags: func(t *testing.T, _ *flags.ChatFlags, loopFlags *flags.LoopFlags) {
				if loopFlags.ContextPressureMessage != "warning" {
					t.Fatalf("ContextPressureMessage = %q, want %q", loopFlags.ContextPressureMessage, "warning")
				}
			},
		},
		{
			name:          "trace id alone",
			args:          []string{"chat", "--trace-id", "ignored-trace"},
			input:         "\x03",
			wantExit:      1,
			wantErrorPart: "--trace-id requires --loop",
			checkFlags: func(t *testing.T, _ *flags.ChatFlags, loopFlags *flags.LoopFlags) {
				if loopFlags.TraceID != "ignored-trace" {
					t.Fatalf("TraceID = %q, want %q", loopFlags.TraceID, "ignored-trace")
				}
			},
		},
		{
			name:          "loop rejects audio input",
			args:          []string{"chat", "--loop", "--max-iterations", "1", "--activate-audio-in"},
			input:         "task\n",
			wantExit:      1,
			wantErrorPart: "--activate-audio-in cannot be combined with --loop",
			checkFlags: func(t *testing.T, chatFlags *flags.ChatFlags, loopFlags *flags.LoopFlags) {
				if !chatFlags.ActivateAudioIn || !loopFlags.Loop || loopFlags.MaxIterations != 1 {
					t.Fatalf("flags = chat=%+v loop=%+v, want audio input, loop, max iterations 1", *chatFlags, *loopFlags)
				}
			},
		},
	}
}

func chatFlagMatrixParseCases() []chatFlagMatrixCase {
	return []chatFlagMatrixCase{
		{
			name:          "boolean audio input rejects wrong type",
			args:          []string{"chat", "--activate-audio-in=maybe"},
			wantExit:      1,
			wantFlagParse: true,
			wantErrorPart: `invalid argument "maybe" for "--activate-audio-in" flag`,
		},
		{
			name:          "boolean audio output rejects wrong type",
			args:          []string{"chat", "--activate-audio-out=maybe"},
			wantExit:      1,
			wantFlagParse: true,
			wantErrorPart: `invalid argument "maybe" for "--activate-audio-out" flag`,
		},
		{
			name:          "boolean loop rejects wrong type",
			args:          []string{"chat", "--loop=maybe"},
			wantExit:      1,
			wantFlagParse: true,
			wantErrorPart: `invalid argument "maybe" for "--loop" flag`,
		},
		{
			name:          "integer rejects wrong type",
			args:          []string{"chat", "--max-iterations", "many"},
			wantExit:      1,
			wantFlagParse: true,
			wantErrorPart: `invalid argument "many" for "--max-iterations" flag`,
		},
		{
			name:          "float rejects wrong type",
			args:          []string{"chat", "--context-pressure-threshold", "many"},
			wantExit:      1,
			wantFlagParse: true,
			wantErrorPart: `invalid argument "many" for "--context-pressure-threshold" flag`,
		},
		{
			name:          "stop word requires a value",
			args:          []string{"chat", "--stop-word"},
			wantExit:      1,
			wantFlagParse: true,
			wantErrorPart: "flag needs an argument: --stop-word",
		},
		{
			name:          "context message requires a value",
			args:          []string{"chat", "--context-pressure-message"},
			wantExit:      1,
			wantFlagParse: true,
			wantErrorPart: "flag needs an argument: --context-pressure-message",
		},
		{
			name:          "trace id requires a value",
			args:          []string{"chat", "--trace-id"},
			wantExit:      1,
			wantFlagParse: true,
			wantErrorPart: "flag needs an argument: --trace-id",
		},
	}
}
