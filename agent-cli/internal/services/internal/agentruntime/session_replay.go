// This file is a compatibility boundary for the session runtime. Capture
// admission, replay planning, pacing, and execution live in the reusable
// replay service; these adapters preserve the older provider call sites while
// that migration completes.
package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	runtimeReplayWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/wire"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// Deprecated: these event names are retained only for the compatibility
// adapters below; replay policy and event interpretation live in services/replay.
const (
	sessionClosedEventType          = "session.closed"
	sessionUpdateEventType          = "session.update"
	conversationItemCreateEventType = "conversation.item.create"
	responseCreateEventType         = "response.create"
	inputAudioBufferAppendEventType = "input_audio_buffer.append"
	inputAudioBufferCommitEventType = "input_audio_buffer.commit"
)

// replaySessionConfiguration is retained solely for source compatibility with
// provider planning code. Deprecated: use replay.Service admission results.
type replaySessionConfiguration struct {
	payload               []byte
	model                 string
	inputAudioSampleRate  int
	outputAudioSampleRate int
	initialToolNames      []string
	initialToolsKnown     bool
}

func (c replaySessionConfiguration) Payload() []byte {
	return append([]byte(nil), c.payload...)
}

func (c replaySessionConfiguration) Model() string { return c.model }

func (c replaySessionConfiguration) InputAudioSampleRate() int { return c.inputAudioSampleRate }

func (c replaySessionConfiguration) OutputAudioSampleRate() int { return c.outputAudioSampleRate }

func (c replaySessionConfiguration) InitialToolNames() []string {
	return append([]string(nil), c.initialToolNames...)
}

func (c replaySessionConfiguration) InitialToolsKnown() bool { return c.initialToolsKnown }

// loadReplaySessionConfiguration is a Deprecated compatibility adapter. The
// reusable replay service owns capture loading and all admission decisions.
func loadReplaySessionConfiguration(path string) (replaySessionConfiguration, error) {
	// replaySessionToolNames remains in the read-only provider file for source
	// compatibility with older snapshots. Deprecated: provider replay now gets
	// tool metadata from the runtime replay service.
	_ = replaySessionToolNames
	configuration, err := runtimeReplayWire.NewService().LoadSessionConfiguration(context.Background(), path)
	if err != nil {
		if errors.Is(err, runtimeReplay.ErrSessionAudioSampleRateConflict) {
			return replaySessionConfiguration{}, fmt.Errorf("%w: %s", ErrSessionAudioSampleRateConflict, err.Error())
		}
		return replaySessionConfiguration{}, err
	}
	return replaySessionConfiguration{
		payload:               configuration.Payload(),
		model:                 configuration.Model(),
		inputAudioSampleRate:  configuration.InputAudioSampleRate(),
		outputAudioSampleRate: configuration.OutputAudioSampleRate(),
		initialToolNames:      configuration.InitialToolNames(),
		initialToolsKnown:     configuration.InitialToolsKnown(),
	}, nil
}

// replayCapturedPrompt is retained solely for source compatibility with the
// existing provider planner. Deprecated: use replay.CapturedTextPrompt.
type replayCapturedPrompt struct {
	text string
}

// loadReplaySessionPrompt is a Deprecated compatibility adapter. Prompt shape
// validation is owned by the replay service.
func loadReplaySessionPrompt(path string) (*replayCapturedPrompt, error) {
	prompt, err := runtimeReplayWire.NewService().LoadCapturedTextPrompt(context.Background(), path)
	if err != nil || prompt == nil {
		return nil, err
	}
	return &replayCapturedPrompt{text: prompt.Text}, nil
}

// loadReplaySessionAudioTurns is a Deprecated compatibility adapter. PCM
// decoding, bounds, and action ordering are owned by the replay service.
func loadReplaySessionAudioTurns(path string) ([]ScheduledAudioInput, error) {
	service := runtimeReplayWire.NewService()
	rawAudioService, ok := service.(interface {
		LoadCapturedAudioTurnsRaw(context.Context, string) ([]runtimeReplay.CapturedAudioTurn, error)
	})
	if !ok {
		return nil, fmt.Errorf("replay service does not provide its Deprecated raw-audio compatibility port")
	}
	turns, err := rawAudioService.LoadCapturedAudioTurnsRaw(context.Background(), path)
	if err != nil {
		return nil, err
	}
	if turns == nil {
		return nil, nil
	}
	inputs := make([]ScheduledAudioInput, len(turns))
	for index, turn := range turns {
		inputs[index] = ScheduledAudioInput{
			AfterCompletedTurns: turn.AfterCompletedTurns,
			PCM:                 append([]byte(nil), turn.PCM...),
			EndOfTurn:           turn.EndOfTurn,
		}
	}
	return inputs, nil
}

// replayLoopMaxDuration is a Deprecated compatibility adapter. The replay
// service owns timestamp admission and the completion grace policy.
func replayLoopMaxDuration(path, timing string) time.Duration {
	duration, err := runtimeReplayWire.NewService().ReplayDuration(context.Background(), path, timing)
	if err != nil {
		return duration
	}
	return duration
}

// newReplayInitialSessionUpdateDialer is a Deprecated compatibility adapter.
// The replay service owns handshake replacement and optional cursor pacing.
func newReplayInitialSessionUpdateDialer(inner transport.Dialer, configuration replaySessionConfiguration, paceOutbound ...bool) transport.Dialer {
	return &replayInitialSessionUpdateDialer{
		inner: runtimeReplayWire.NewService().WrapInitialSessionUpdateDialer(inner, configuration, paceOutbound...),
	}
}

// replayInitialSessionUpdateDialer is a Deprecated port adapter retained for
// package-local callers that identify the historical wrapper type.
type replayInitialSessionUpdateDialer struct {
	inner transport.Dialer
}

var _ transport.Dialer = (*replayInitialSessionUpdateDialer)(nil)

func (d *replayInitialSessionUpdateDialer) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	return d.inner.Dial(endpoint, headers)
}

// replaySessionCapture is a Deprecated compatibility adapter. The replay
// service owns cursor lifecycle and bounded draining; the CLI only renders.
func replaySessionCapture(ctx context.Context, out io.Writer, path string) error {
	renderer := newSessionReplayRenderer(out, sessionTerminalReporterFromContext(ctx))
	return runtimeReplayWire.NewService().ReplayCapture(ctx, path, func(message messages.StreamMessage) error {
		return writeSessionReplayMessage(renderer, message)
	})
}

// grokReplayCaptureHasSessionClose is a Deprecated compatibility adapter.
func grokReplayCaptureHasSessionClose(path string) bool {
	return captureHasEvent(path, sessionClosedEventType)
}

// usesWebSocketCapture is a Deprecated compatibility adapter.
func usesWebSocketCapture(path string) bool {
	inspection, err := runtimeReplayWire.NewService().InspectCapture(context.Background(), path)
	if err != nil && inspection.Kind == "" {
		return false
	}
	return inspection.IsRealtime()
}

// usesOpenAIWebSocketCapture is a Deprecated compatibility adapter.
func usesOpenAIWebSocketCapture(path string) bool {
	inspection, err := runtimeReplayWire.NewService().InspectCapture(context.Background(), path)
	if err != nil && inspection.Provider == "" {
		return false
	}
	return strings.EqualFold(inspection.Provider, sessionProviderOpenAI)
}

// captureHasEvent is a Deprecated compatibility adapter.
func captureHasEvent(path string, eventType string) bool {
	hasEvent, err := runtimeReplayWire.NewService().HasEvent(context.Background(), path, eventType)
	return err == nil && hasEvent
}
