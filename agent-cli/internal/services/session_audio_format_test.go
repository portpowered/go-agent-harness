package services

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func TestPlanOpenAIRecordPromptAudioOutputWithoutInputUsesRealtimeDuplexRate(t *testing.T) {
	inferencer := &sessionAudioContractInferencer{}
	recordPath := filepath.Join(t.TempDir(), "cube-session.json")
	loaded := &config.Config{Model: config.ModelConfig{
		Provider: config.ProviderOpenAI,
		OpenAI: &config.OpenAIConfig{
			APIKey: "test-key",
			Model:  DefaultOpenAIRealtimeModel,
		},
	}}
	plan, err := planSessionRuntimeWithFactory(SessionRunOptions{
		Prompt:               "What is the current state of the cube? Then turn the top face once.",
		PromptProvided:       true,
		RecordPath:           recordPath,
		AudioOutputRequested: true,
		Provider:             config.ProviderOpenAI,
		ProviderProvided:     true,
		Model:                DefaultOpenAIRealtimeModel,
		ModelProvided:        true,
		APIKey:               "test-key",
		LoadedConfig:         loaded,
	}, sessionRuntimeFactory{
		newDefaultLiveDialer: defaultSessionRuntimeFactory.newDefaultLiveDialer,
		newRecordingDialer:   defaultSessionRuntimeFactory.newRecordingDialer,
		newOpenAISessionWithTools: func(
			config.OpenAIConfig,
			string,
			transport.Dialer,
			[]messages.ToolDefinition,
			models.InputAudioTranscriptionConfig,
		) (messages.SessionInferencer, error) {
			return inferencer, nil
		},
	})
	if err != nil {
		t.Fatalf("plan operator-shaped record session: %v", err)
	}
	defer func() { _ = plan.captureClaim.release() }()

	if plan.mode != sessionRuntimeModeRecordOpenAI || plan.capturePath != recordPath {
		t.Fatalf("record plan = mode:%q capture:%q, want OpenAI record at %q", plan.mode, plan.capturePath, recordPath)
	}
	assertSessionAudioContract(t, plan, inferencer, sessionRealtimeAudioSampleRate)
}

func TestConfigureSessionAudioContractPromptRecordWithoutInputUsesRealtimeRate(t *testing.T) {
	inferencer := &sessionAudioContractInferencer{}
	opts := SessionRunOptions{
		Prompt:               "inspect the cube",
		RecordPath:           "cube-session.json",
		AudioOutputRequested: true,
	}
	plan := sessionRuntimePlan{provider: sessionProviderOpenAI, inferencer: inferencer}

	if err := configureSessionAudioContract(opts, &plan); err != nil {
		t.Fatalf("configure session audio: %v", err)
	}
	assertSessionAudioContract(t, plan, inferencer, sessionRealtimeAudioSampleRate)
}

func TestConfigureSessionAudioContractResolution(t *testing.T) {
	tests := []struct {
		name       string
		opts       SessionRunOptions
		provider   string
		request    models.SessionConfig
		inputRate  int
		outputRate int
		wantRate   int
		wantErr    bool
	}{
		{name: "openai no flags", provider: sessionProviderOpenAI, wantRate: sessionRealtimeAudioSampleRate},
		{name: "grok no flags", provider: sessionProviderGrok, wantRate: sessionRealtimeAudioSampleRate},
		{name: "output file", provider: sessionProviderOpenAI, opts: SessionRunOptions{AudioOutputRequested: true}, wantRate: sessionRealtimeAudioSampleRate},
		{name: "input device", provider: sessionProviderOpenAI, opts: SessionRunOptions{RTCDeviceBinding: RTCDeviceBindingRequest{InputPresent: true}}, wantRate: sessionRealtimeAudioSampleRate},
		{name: "both devices", provider: sessionProviderGrok, opts: SessionRunOptions{RTCDeviceBinding: RTCDeviceBindingRequest{InputPresent: true, OutputPresent: true}}, wantRate: sessionRealtimeAudioSampleRate},
		{name: "caller openai inferencer defaults to realtime rate", provider: sessionProviderOpenAI, opts: SessionRunOptions{SessionInferencer: &sessionAudioContractInferencer{}}, wantRate: sessionRealtimeAudioSampleRate},
		{name: "caller grok inferencer defaults to realtime rate", provider: sessionProviderGrok, opts: SessionRunOptions{SessionInferencer: &sessionAudioContractInferencer{}}, wantRate: sessionRealtimeAudioSampleRate},
		{name: "caller seam explicitly declares native rate", provider: sessionProviderOpenAI, opts: SessionRunOptions{SessionInferencer: &sessionAudioContractInferencer{request: inference.SessionRequest{Config: models.SessionConfig{InputAudioSampleRate: models.SampleRate16000}}}}, wantRate: 16000},
		{name: "explicit request input", request: models.SessionConfig{InputAudioSampleRate: models.SampleRate16000}, wantRate: 16000},
		{name: "explicit request output", request: models.SessionConfig{OutputAudioSampleRate: models.SampleRate24000}, wantRate: 24000},
		{name: "captured output", outputRate: 16000, wantRate: 16000},
		{name: "matching captured rates", inputRate: 24000, outputRate: 24000, wantRate: 24000},
		{name: "conflicting request rates", request: models.SessionConfig{InputAudioSampleRate: models.SampleRate16000, OutputAudioSampleRate: models.SampleRate24000}, wantErr: true},
		{name: "conflicting captured rates", inputRate: 16000, outputRate: 24000, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inferencer := &sessionAudioContractInferencer{request: inference.SessionRequest{Config: tt.request}}
			opts := tt.opts
			if opts.SessionInferencer != nil {
				inferencer = opts.SessionInferencer.(*sessionAudioContractInferencer)
			}
			plan := sessionRuntimePlan{
				provider:              tt.provider,
				inferencer:            inferencer,
				inputAudioSampleRate:  tt.inputRate,
				outputAudioSampleRate: tt.outputRate,
			}

			err := configureSessionAudioContract(opts, &plan)
			if tt.wantErr {
				if !errors.Is(err, ErrSessionAudioSampleRateConflict) {
					t.Fatalf("configure error = %v, want ErrSessionAudioSampleRateConflict", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("configure session audio: %v", err)
			}
			assertSessionAudioContract(t, plan, inferencer, tt.wantRate)
		})
	}
}

func assertSessionAudioContract(t *testing.T, plan sessionRuntimePlan, inferencer *sessionAudioContractInferencer, wantRate int) {
	t.Helper()
	if plan.inputAudioSampleRate != wantRate || plan.outputAudioSampleRate != wantRate {
		t.Fatalf("planned audio rates = %d/%d, want %d/%d", plan.inputAudioSampleRate, plan.outputAudioSampleRate, wantRate, wantRate)
	}
	if inferencer.inputRate != models.SampleRate(wantRate) || inferencer.outputRate != models.SampleRate(wantRate) {
		t.Fatalf("configured audio rates = %d/%d, want %d/%d", inferencer.inputRate, inferencer.outputRate, wantRate, wantRate)
	}
}

type sessionAudioContractInferencer struct {
	request    inference.SessionRequest
	inputRate  models.SampleRate
	outputRate models.SampleRate
}

func (i *sessionAudioContractInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return nil, errors.New("not used")
}

func (i *sessionAudioContractInferencer) Request() inference.SessionRequest {
	return i.request
}

func (i *sessionAudioContractInferencer) SetSessionAudioInput(_ models.AudioFormat, rate models.SampleRate) {
	i.inputRate = rate
}

func (i *sessionAudioContractInferencer) SetSessionAudioOutput(_ models.AudioFormat, rate models.SampleRate) {
	i.outputRate = rate
}
