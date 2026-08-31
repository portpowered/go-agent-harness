package openai

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

func TestBuildRealtimeSessionUpdateCanonicalizesToolOrder(t *testing.T) {
	provider := &OpenAIProvider{}
	first, err := provider.buildRealtimeSessionUpdate(models.SessionConfig{
		Tools: []models.ToolDefinition{
			{Name: "zeta", Parameters: []models.ToolParameter{{Name: "z"}, {Name: "a"}}},
			{Name: "alpha"},
		},
	}, "gpt-realtime")
	if err != nil {
		t.Fatalf("build first realtime session.update: %v", err)
	}
	second, err := provider.buildRealtimeSessionUpdate(models.SessionConfig{
		Tools: []models.ToolDefinition{
			{Name: "alpha"},
			{Name: "zeta", Parameters: []models.ToolParameter{{Name: "a"}, {Name: "z"}}},
		},
	}, "gpt-realtime")
	if err != nil {
		t.Fatalf("build second realtime session.update: %v", err)
	}
	if !bytes.Equal(first.Data, second.Data) {
		t.Fatalf("equivalent tool compositions produced different session.update bytes:\nfirst=%s\nsecond=%s", first.Data, second.Data)
	}

	var envelope struct {
		Session struct {
			Tools []struct {
				Name       string `json:"name"`
				Parameters struct {
					Required []string `json:"required"`
				} `json:"parameters"`
			} `json:"tools"`
		} `json:"session"`
	}
	if err := json.Unmarshal(first.Data, &envelope); err != nil {
		t.Fatalf("decode session.update: %v", err)
	}
	if len(envelope.Session.Tools) != 2 || envelope.Session.Tools[0].Name != "alpha" || envelope.Session.Tools[1].Name != "zeta" {
		t.Fatalf("serialized tool order = %#v, want alpha then zeta", envelope.Session.Tools)
	}
}

func TestBuildRealtimeSessionUpdatePreservesCompletePageToolSchema(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"moves":{"type":"array","items":{"type":"object","properties":{"face":{"type":"string","enum":["R","U"]},"turns":{"type":"integer","minimum":1}},"required":["face","turns"],"additionalProperties":false}}},"required":["moves"],"additionalProperties":false}`)
	event, err := (&OpenAIProvider{}).buildRealtimeSessionUpdate(models.SessionConfig{
		Tools: []models.ToolDefinition{{
			Name:            "queue_cube_moves",
			Description:     "Queue cube rotations.",
			ParameterSchema: schema,
			Parameters:      []models.ToolParameter{{Name: "moves", Type: "array", Required: true}},
		}},
	}, "gpt-realtime")
	if err != nil {
		t.Fatalf("build realtime session.update: %v", err)
	}

	var envelope struct {
		Session struct {
			Tools []struct {
				Parameters json.RawMessage `json:"parameters"`
			} `json:"tools"`
		} `json:"session"`
	}
	if err := json.Unmarshal(event.Data, &envelope); err != nil {
		t.Fatalf("decode session.update: %v", err)
	}
	if len(envelope.Session.Tools) != 1 {
		t.Fatalf("serialized tools = %#v, want one page tool", envelope.Session.Tools)
	}

	var want, got any
	if err := json.Unmarshal(schema, &want); err != nil {
		t.Fatalf("decode expected schema: %v", err)
	}
	if err := json.Unmarshal(envelope.Session.Tools[0].Parameters, &got); err != nil {
		t.Fatalf("decode serialized parameters: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("serialized page schema = %#v, want complete schema %#v", got, want)
	}
}

func TestBuildRealtimeSessionUpdateInputAudioTranscriptionUsesSelectedWireContract(t *testing.T) {
	policy := &models.InputAudioTranscriptionConfig{Enabled: true}
	cases := []struct {
		name           string
		provider       *OpenAIProvider
		config         models.SessionConfig
		legacy         bool
		wantTranscribe bool
		wantModel      string
	}{
		{
			name:           "GA audio input",
			provider:       &OpenAIProvider{},
			config:         models.SessionConfig{InputAudioFormat: models.AudioFormatPCM16, InputAudioTranscription: policy},
			wantTranscribe: true,
			wantModel:      models.DefaultInputAudioTranscriptionModel,
		},
		{
			name:           "legacy audio input",
			provider:       &OpenAIProvider{realtimeLegacySessionUpdate: true},
			config:         models.SessionConfig{InputAudioFormat: models.AudioFormatPCM16, InputAudioTranscription: policy},
			legacy:         true,
			wantTranscribe: true,
			wantModel:      models.DefaultInputAudioTranscriptionModel,
		},
		{
			name:           "GA client-owned audio without format",
			provider:       &OpenAIProvider{clientOwnsAudioTurnBoundaries: true},
			config:         models.SessionConfig{InputAudioTranscription: &models.InputAudioTranscriptionConfig{Enabled: true, Model: "custom-transcriber"}},
			wantTranscribe: true,
			wantModel:      "custom-transcriber",
		},
		{
			name:     "disabled",
			provider: &OpenAIProvider{},
			config:   models.SessionConfig{InputAudioFormat: models.AudioFormatPCM16, InputAudioTranscription: &models.InputAudioTranscriptionConfig{}},
		},
		{
			name:     "text-only",
			provider: &OpenAIProvider{},
			config:   models.SessionConfig{Modalities: []models.SessionModality{models.SessionModalityText}, InputAudioTranscription: policy},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			event, err := testCase.provider.buildRealtimeSessionUpdate(testCase.config, "gpt-realtime")
			if err != nil {
				t.Fatalf("build realtime session.update: %v", err)
			}
			var envelope struct {
				Session map[string]any `json:"session"`
			}
			if err := json.Unmarshal(event.Data, &envelope); err != nil {
				t.Fatalf("decode session.update: %v", err)
			}
			if !testCase.legacy {
				audio, ok := envelope.Session["audio"].(map[string]any)
				var transcription map[string]any
				if ok {
					input, inputOK := audio["input"].(map[string]any)
					if inputOK {
						transcription, _ = input["transcription"].(map[string]any)
					}
				}
				if testCase.wantTranscribe != (transcription != nil) {
					t.Fatalf("GA transcription present = %t, want %t", transcription != nil, testCase.wantTranscribe)
				}
				if transcription != nil && transcription["model"] != testCase.wantModel {
					t.Fatalf("GA transcription model = %#v, want %q", transcription["model"], testCase.wantModel)
				}
				if _, legacy := envelope.Session["input_audio_transcription"]; legacy {
					t.Fatal("GA session.update unexpectedly included legacy input_audio_transcription")
				}
				return
			}

			if _, ga := envelope.Session["audio"]; ga {
				t.Fatal("non-GA session.update unexpectedly included nested GA audio")
			}
			transcription, ok := envelope.Session["input_audio_transcription"].(map[string]any)
			if testCase.wantTranscribe != ok {
				t.Fatalf("legacy transcription present = %t, want %t", ok, testCase.wantTranscribe)
			}
			if ok && transcription["model"] != testCase.wantModel {
				t.Fatalf("legacy transcription model = %#v, want %q", transcription["model"], testCase.wantModel)
			}
		})
	}
}

func TestBuildRealtimeSessionUpdateInputAudioTranscriptionDoesNotChangeOtherFields(t *testing.T) {
	base := models.SessionConfig{
		Modalities:            []models.SessionModality{models.SessionModalityText, models.SessionModalityAudio},
		Voice:                 "marin",
		Instructions:          "Keep responses concise.",
		InputAudioFormat:      models.AudioFormatPCM16,
		OutputAudioFormat:     models.AudioFormatG711Ulaw,
		InputAudioSampleRate:  models.SampleRate24000,
		OutputAudioSampleRate: models.SampleRate24000,
		TurnDetection:         &models.TurnDetectionConfig{Type: "server_vad"},
	}
	without, err := (&OpenAIProvider{}).buildRealtimeSessionUpdate(base, "gpt-realtime")
	if err != nil {
		t.Fatalf("build base session.update: %v", err)
	}
	withConfig := base
	withConfig.InputAudioTranscription = &models.InputAudioTranscriptionConfig{Enabled: true}
	with, err := (&OpenAIProvider{}).buildRealtimeSessionUpdate(withConfig, "gpt-realtime")
	if err != nil {
		t.Fatalf("build configured session.update: %v", err)
	}

	var withoutEnvelope, withEnvelope struct {
		Session map[string]any `json:"session"`
	}
	if err := json.Unmarshal(without.Data, &withoutEnvelope); err != nil {
		t.Fatalf("decode base session.update: %v", err)
	}
	if err := json.Unmarshal(with.Data, &withEnvelope); err != nil {
		t.Fatalf("decode configured session.update: %v", err)
	}
	withAudio := withEnvelope.Session["audio"].(map[string]any)
	withInput := withAudio["input"].(map[string]any)
	delete(withInput, "transcription")
	if !reflect.DeepEqual(withoutEnvelope.Session, withEnvelope.Session) {
		t.Fatalf("configured session.update changed fields beyond transcription:\nwithout=%#v\nwith=%#v", withoutEnvelope.Session, withEnvelope.Session)
	}
}
