package agentruntime

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	audioiowire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio/wire"
	runtimedevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
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
	plan, err := planSessionRuntimeWithFactory(SessionRunOptions{ModelCatalog: testModelCatalog(), AudioService: newTestAudioIOService(),
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
	assertSessionAudioContract(t, plan, inferencer, audioio.RealtimeSampleRate)
}

func newTestAudioIOService() audioio.Service { return audioiowire.NewService() }

type audioioRateResolutionCase struct {
	name       string
	opts       SessionRunOptions
	provider   string
	request    models.SessionConfig
	inputRate  int
	outputRate int
	wantRate   int
	wantErr    bool
}

func TestAudioioRateResolution(t *testing.T) {
	tests := []audioioRateResolutionCase{
		{name: "openai no flags", provider: sessionProviderOpenAI, wantRate: audioio.RealtimeSampleRate},
		{name: "grok no flags", provider: sessionProviderGrok, wantRate: audioio.RealtimeSampleRate},
		{name: "output file", provider: sessionProviderOpenAI, opts: SessionRunOptions{ModelCatalog: testModelCatalog(), AudioOutputRequested: true}, wantRate: audioio.RealtimeSampleRate},
		{name: "input device", provider: sessionProviderOpenAI, opts: SessionRunOptions{ModelCatalog: testModelCatalog(), RTCBinding: runtimedevices.RTCBindingRequest{InputPresent: true}}, wantRate: audioio.RealtimeSampleRate},
		{name: "both devices", provider: sessionProviderGrok, opts: SessionRunOptions{ModelCatalog: testModelCatalog(), RTCBinding: runtimedevices.RTCBindingRequest{InputPresent: true, OutputPresent: true}}, wantRate: audioio.RealtimeSampleRate},
		{name: "caller openai inferencer defaults to realtime rate", provider: sessionProviderOpenAI, opts: SessionRunOptions{ModelCatalog: testModelCatalog(), SessionInferencer: &sessionAudioContractInferencer{}}, wantRate: audioio.RealtimeSampleRate},
		{name: "caller grok inferencer defaults to realtime rate", provider: sessionProviderGrok, opts: SessionRunOptions{ModelCatalog: testModelCatalog(), SessionInferencer: &sessionAudioContractInferencer{}}, wantRate: audioio.RealtimeSampleRate},
		{name: "caller seam explicitly declares native rate", provider: sessionProviderOpenAI, opts: SessionRunOptions{ModelCatalog: testModelCatalog(), SessionInferencer: &sessionAudioContractInferencer{request: inference.SessionRequest{Config: models.SessionConfig{InputAudioSampleRate: models.SampleRate16000}}}}, wantRate: 16000},
		{name: "explicit request input", request: models.SessionConfig{InputAudioSampleRate: models.SampleRate16000}, wantRate: 16000},
		{name: "explicit request output", request: models.SessionConfig{OutputAudioSampleRate: models.SampleRate24000}, wantRate: 24000},
		{name: "captured output", outputRate: 16000, wantRate: 16000},
		{name: "matching captured rates", inputRate: 24000, outputRate: 24000, wantRate: 24000},
		{name: "conflicting request rates", request: models.SessionConfig{InputAudioSampleRate: models.SampleRate16000, OutputAudioSampleRate: models.SampleRate24000}, wantErr: true},
		{name: "conflicting captured rates", inputRate: 16000, outputRate: 24000, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertAudioioRateResolution(t, tt)
		})
	}
}

func assertAudioioRateResolution(t *testing.T, testCase audioioRateResolutionCase) {
	t.Helper()
	inferencer := &sessionAudioContractInferencer{request: inference.SessionRequest{Config: testCase.request}}
	if testCase.opts.SessionInferencer != nil {
		var ok bool
		inferencer, ok = testCase.opts.SessionInferencer.(*sessionAudioContractInferencer)
		if !ok {
			t.Fatalf("session inferencer = %T, want *sessionAudioContractInferencer", testCase.opts.SessionInferencer)
		}
	}
	inputRate, outputRate := testCase.inputRate, testCase.outputRate
	request := inferencer.Request().Config
	if inputRate <= 0 {
		inputRate = int(request.InputAudioSampleRate)
	}
	if outputRate <= 0 {
		outputRate = int(request.OutputAudioSampleRate)
	}
	rates, err := audioiowire.NewService().ResolveRates(context.Background(), audioio.RateRequest{
		Provider: testCase.provider, CapturedInputRate: inputRate, CapturedOutputRate: outputRate,
	})
	if testCase.wantErr {
		if !errors.Is(err, audioio.ErrSampleRateConflict) {
			t.Fatalf("resolve error = %v, want audioio.ErrSampleRateConflict", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("resolve audio rate: %v", err)
	}
	if rates.InputRate != testCase.wantRate || rates.OutputRate != testCase.wantRate {
		t.Fatalf("resolved audio rates = %d/%d, want %d/%d", rates.InputRate, rates.OutputRate, testCase.wantRate, testCase.wantRate)
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
