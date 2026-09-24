package agentruntime

import (
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/grok"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

type sessionRecordingDialer interface {
	transport.Dialer
	FlushToFile(path string) error
}

type sessionReplayDialer interface {
	transport.Dialer
	Done() <-chan struct{}
	Err() error
	Model() string
}

type runtimeAudioOutputConfigurer interface {
	SetSessionAudioOutput(models.AudioFormat, models.SampleRate)
}

type runtimeAudioInputConfigurer interface {
	SetSessionAudioInput(models.AudioFormat, models.SampleRate)
}

type sessionAudioRequestProvider interface {
	Request() inference.SessionRequest
}

type sessionRuntimeFactory struct {
	durationService                    sessionduration.Service
	durationRunner                     session.DurationRunner
	newDefaultLiveDialer               func() transport.Dialer
	newRecordingDialer                 func(transport.Dialer, string, string) sessionRecordingDialer
	newReplayDialer                    func(string) (sessionReplayDialer, error)
	newRecordedTimingReplayDialer      func(string) (sessionReplayDialer, error)
	newReplayInferencer                func(string) messages.SessionInferencer
	newGrokSessionInferencer           func(config.GrokConfig, transport.Dialer) (messages.SessionInferencer, error)
	newOpenAISessionInf                func(config.OpenAIConfig, string, transport.Dialer, models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error)
	newBareLiveSessionInferencer       func(SessionRunOptions) (messages.SessionInferencer, string, error)
	newGrokSessionWithTools            func(config.GrokConfig, transport.Dialer, []messages.ToolDefinition) (messages.SessionInferencer, error)
	newOpenAISessionWithTools          func(config.OpenAIConfig, string, transport.Dialer, []messages.ToolDefinition, models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error)
	newOpenAIScheduledSessionWithTools func(config.OpenAIConfig, string, transport.Dialer, []messages.ToolDefinition, models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error)
	newRTCRuntime                      SessionRTCRuntimeFactory
}

// SessionRuntimeFactory is the process-scoped provider construction owner.
// Wire creates one instance and every session/room planner receives that
// instance, keeping provider construction out of request dispatch.
type SessionRuntimeFactory = sessionRuntimeFactory

// WithRuntimeFactory returns options backed by the application-composed
// session runtime factory. Callers that already compose a runtime graph can
// pass the same factory to the session's convenience entry points.
func (opts SessionRunOptions) WithRuntimeFactory(factory SessionRuntimeFactory) SessionRunOptions {
	opts.runtimeFactory = factory
	return opts
}

func NewSessionRuntimeFactory(service sessionduration.Service, runner session.DurationRunner) SessionRuntimeFactory {
	factory := newDefaultSessionRuntimeFactory()
	factory.durationService = service
	factory.durationRunner = runner
	return factory
}

func (f sessionRuntimeFactory) configured() bool {
	return f.durationService != nil || f.durationRunner != nil || f.newDefaultLiveDialer != nil || f.newReplayDialer != nil || f.newBareLiveSessionInferencer != nil || f.newRTCRuntime != nil
}

func newDefaultSessionRuntimeFactory() sessionRuntimeFactory {
	return sessionRuntimeFactory{
		newDefaultLiveDialer: func() transport.Dialer {
			return grok.NewDefaultWebSocketDialer()
		},
		newRecordingDialer: func(inner transport.Dialer, providerName string, model string) sessionRecordingDialer {
			return gwtesting.NewRecordingWebSocketDialer(inner, providerName, model)
		},
		newReplayDialer: func(path string) (sessionReplayDialer, error) {
			return gwtesting.NewReplayWebSocketDialer(path)
		},
		newRecordedTimingReplayDialer: func(path string) (sessionReplayDialer, error) {
			return gwtesting.NewReplayWebSocketDialer(path, gwtesting.WithRecordedSessionTiming())
		},
		newReplayInferencer: func(path string) messages.SessionInferencer {
			return gwtesting.NewReplaySessionInferencer(path)
		},
		newGrokSessionInferencer: func(sessionCfg config.GrokConfig, dialer transport.Dialer) (messages.SessionInferencer, error) {
			return buildGrokSessionInferencer(sessionCfg, dialer)
		},
		newOpenAISessionInf: func(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, inputAudioTranscription models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
			return buildOpenAIRealtimeSessionInferencerWithInputAudioTranscription(sessionCfg, voice, dialer, inputAudioTranscription)
		},
		newBareLiveSessionInferencer: func(opts SessionRunOptions) (messages.SessionInferencer, string, error) {
			return NewLiveSessionInferencer(opts, "")
		},
		newGrokSessionWithTools: func(sessionCfg config.GrokConfig, dialer transport.Dialer, toolDefinitions []messages.ToolDefinition) (messages.SessionInferencer, error) {
			return buildGrokSessionInferencerWithTools(sessionCfg, dialer, toolDefinitions)
		},
		newOpenAISessionWithTools: func(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, toolDefinitions []messages.ToolDefinition, inputAudioTranscription models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
			return buildOpenAIRealtimeSessionInferencerWithToolsAndInputAudioTranscription(sessionCfg, voice, dialer, toolDefinitions, inputAudioTranscription)
		},
		newOpenAIScheduledSessionWithTools: func(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, toolDefinitions []messages.ToolDefinition, inputAudioTranscription models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
			return buildOpenAIRealtimeSessionInferencerWithScheduledAudioAndInputAudioTranscription(sessionCfg, voice, dialer, toolDefinitions, inputAudioTranscription)
		},
	}
}

func (f sessionRuntimeFactory) replayDialer(path, timing string) (sessionReplayDialer, error) {
	if normalizedSessionReplayTiming(timing) == sessionReplayTimingRecorded && f.newRecordedTimingReplayDialer != nil {
		return f.newRecordedTimingReplayDialer(path)
	}
	return f.newReplayDialer(path)
}

func (f sessionRuntimeFactory) newGrokSessionInferencerForTools(sessionCfg config.GrokConfig, dialer transport.Dialer, toolDefinitions []messages.ToolDefinition) (messages.SessionInferencer, error) {
	if f.newGrokSessionWithTools != nil {
		return f.newGrokSessionWithTools(sessionCfg, dialer, toolDefinitions)
	}
	return f.newGrokSessionInferencer(sessionCfg, dialer)
}

func (f sessionRuntimeFactory) newOpenAISessionInferencerForTools(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, toolDefinitions []messages.ToolDefinition, scheduledAudio bool, inputAudioTranscription models.InputAudioTranscriptionConfig) (messages.SessionInferencer, error) {
	if scheduledAudio && f.newOpenAIScheduledSessionWithTools != nil {
		return f.newOpenAIScheduledSessionWithTools(sessionCfg, voice, dialer, toolDefinitions, inputAudioTranscription)
	}
	if f.newOpenAISessionWithTools != nil {
		return f.newOpenAISessionWithTools(sessionCfg, voice, dialer, toolDefinitions, inputAudioTranscription)
	}
	return f.newOpenAISessionInf(sessionCfg, voice, dialer, inputAudioTranscription)
}
