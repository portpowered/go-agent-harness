package agentruntime

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestLoadReplaySessionConfigurationExtractsDuplexAudioRates(t *testing.T) {
	path := writeReplayAudioRateCapture(t, sessionProviderOpenAI, 24000, 24000)

	configuration, err := loadReplaySessionConfiguration(path)
	if err != nil {
		t.Fatalf("load replay session configuration: %v", err)
	}
	if configuration.inputAudioSampleRate != 24000 || configuration.outputAudioSampleRate != 24000 {
		t.Fatalf(
			"captured audio rates = %d/%d, want 24000/24000",
			configuration.inputAudioSampleRate,
			configuration.outputAudioSampleRate,
		)
	}
}

func TestReplayPlannersRejectCapturedAsymmetricAudioRatesBeforeDialerConstruction(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		plan     func(SessionRunOptions, sessionRuntimeFactory) (sessionRuntimePlan, error)
	}{
		{name: "openai", provider: sessionProviderOpenAI, plan: planOpenAIReplayRuntime},
		{name: "grok", provider: sessionProviderGrok, plan: planGrokReplayRuntime},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeReplayAudioRateCapture(t, test.provider, 16000, 24000)
			dialerConstructed := false
			_, err := test.plan(SessionRunOptions{ModelCatalog: testModelCatalog(), ReplayPath: path}, sessionRuntimeFactory{
				newReplayDialer: func(string) (sessionReplayDialer, error) {
					dialerConstructed = true
					return nil, errors.New("must not construct replay dialer")
				},
			})
			if !errors.Is(err, ErrSessionAudioSampleRateConflict) {
				t.Fatalf("plan replay error = %v, want ErrSessionAudioSampleRateConflict", err)
			}
			if dialerConstructed {
				t.Fatal("replay dialer constructed before captured duplex rates were validated")
			}
		})
	}
}

func writeReplayAudioRateCapture(t *testing.T, provider string, inputRate int, outputRate int) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"type": sessionUpdateEventType,
		"session": map[string]any{
			"model": "realtime-replay",
			"audio": map[string]any{
				"input":  map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": inputRate}},
				"output": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": outputRate}},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal replay session update: %v", err)
	}

	path := filepath.Join(t.TempDir(), provider+"-replay.session.json")
	writeSessionCapture(t, path, gwtesting.SessionCapture{
		Version:  gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{Name: provider, Model: "realtime-replay"},
		Records: []gwtesting.CapturedSessionEvent{{
			Sequence:    1,
			Direction:   gwtesting.DirectionClientToServer,
			Type:        sessionUpdateEventType,
			PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
			Payload:     payload,
		}},
	})
	return path
}
