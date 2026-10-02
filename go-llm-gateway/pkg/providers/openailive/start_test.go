package openailive_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	live "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/fakelive"
)

func restaurantConfig() models.SessionConfig {
	return models.SessionConfig{
		Model:                 live.Model1,
		Instructions:          restaurantPrompt,
		Voice:                 "marin",
		InputAudioFormat:      models.AudioFormatPCM16,
		OutputAudioFormat:     models.AudioFormatPCM16,
		InputAudioSampleRate:  models.SampleRate24000,
		OutputAudioSampleRate: models.SampleRate24000,
	}
}

func TestBuildSessionStartReproducesTheSpecExample(t *testing.T) {
	start, err := live.BuildSessionStart("evt_start_001", restaurantConfig())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := live.EncodeEvent(start)
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile(filepath.Join(goldenDir, "client.session_start.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !sameJSON(t, encoded, golden) {
		t.Fatalf("built\n%s\nwant the spec example\n%s", encoded, golden)
	}
}

// TestBuildSessionStartSendsOnlyDocumentedKeys builds session.start from
// configs that set every harness field GPT-Live lacks, and checks the
// session object against the spec's key allowlist.
func TestBuildSessionStartSendsOnlyDocumentedKeys(t *testing.T) {
	everything := restaurantConfig()
	everything.Tools = []models.ToolDefinition{{Name: "lookup", Description: "Look up a booking."}}
	everything.TurnDetection = &models.TurnDetectionConfig{Type: "server_vad"}
	everything.InputAudioTranscription = &models.InputAudioTranscriptionConfig{Model: "gpt-live-transcribe"}
	everything.Modalities = []models.SessionModality{models.SessionModalityAudio, models.SessionModalityText}
	everything.ReasoningEffort = "high"
	responses := everything
	responses.Config = json.RawMessage(`{
		"store": true,
		"delegation": {"type": "responses", "responses": {
			"model": "gpt-6-astra", "instructions": "Be brief.", "max_output_tokens": 64,
			"parallel_tool_calls": true, "reasoning": {"effort": "low", "summary": "auto"},
			"service_tier": "priority", "text": {"verbosity": "low"}, "tool_choice": "auto",
			"tools": [{"type": "function", "name": "book", "description": "Book.", "parameters": {"type": "object"}, "strict": true}, {"type": "web_search"}]
		}},
		"input": [
			{"type": "message", "id": "m1", "role": "developer", "status": "completed", "content": [{"type": "input_text", "text": "Rules."}]},
			{"role": "user", "content": [{"type": "input_text", "text": "Hi."}]},
			{"role": "assistant", "content": [{"type": "output_text", "text": "Hello."}]},
			{"role": "assistant", "status": "incomplete", "content": [{"type": "text", "text": "One"}]}
		]
	}`)
	for name, cfg := range map[string]models.SessionConfig{"client": everything, "responses": responses} {
		t.Run(name, func(t *testing.T) {
			start, err := live.BuildSessionStart("evt_1", cfg)
			if err != nil {
				t.Fatal(err)
			}
			session, err := json.Marshal(start.Session)
			if err != nil {
				t.Fatal(err)
			}
			if unknown := fakelive.UnknownSessionKey(session); unknown != "" {
				t.Fatalf("session.start carries undocumented key %s: %s", unknown, session)
			}
		})
	}
}

func TestBuildSessionStartMapsAudioFormats(t *testing.T) {
	for name, tc := range map[string]struct {
		format models.AudioFormat
		rate   models.SampleRate
		want   live.AudioFormat
	}{
		"defaults":  {want: live.AudioFormat{Type: live.AudioTypePCM, Rate: live.RatePCM24k}},
		"pcm 16k":   {format: models.AudioFormatPCM16, rate: models.SampleRate16000, want: live.AudioFormat{Type: live.AudioTypePCM, Rate: live.RatePCM16k}},
		"mu-law":    {format: models.AudioFormatG711Ulaw, want: live.AudioFormat{Type: live.AudioTypePCMU, Rate: live.RateG711}},
		"A-law 8k":  {format: models.AudioFormatG711Alaw, rate: models.SampleRate8000, want: live.AudioFormat{Type: live.AudioTypePCMA, Rate: live.RateG711}},
		"mu-law 8k": {format: models.AudioFormatG711Ulaw, rate: models.SampleRate8000, want: live.AudioFormat{Type: live.AudioTypePCMU, Rate: live.RateG711}},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := models.SessionConfig{Model: live.Model1, InputAudioFormat: tc.format, OutputAudioFormat: tc.format,
				InputAudioSampleRate: tc.rate, OutputAudioSampleRate: tc.rate}
			start, err := live.BuildSessionStart("", cfg)
			if err != nil {
				t.Fatal(err)
			}
			if got := *start.Session.Audio.Format; got != tc.want {
				t.Fatalf("format = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestBuildSessionStartRejectsInvalidConfig(t *testing.T) {
	base := restaurantConfig()
	with := func(edit func(*models.SessionConfig)) models.SessionConfig {
		cfg := base
		edit(&cfg)
		return cfg
	}
	options := func(raw string) models.SessionConfig {
		return with(func(c *models.SessionConfig) { c.Config = json.RawMessage(raw) })
	}
	for name, cfg := range map[string]models.SessionConfig{
		"blank model":      with(func(c *models.SessionConfig) { c.Model = "  " }),
		"oversized prompt": with(func(c *models.SessionConfig) { c.Instructions = strings.Repeat("x", live.MaxInstructionsBytes+1) }),
		"formats differ":   with(func(c *models.SessionConfig) { c.OutputAudioFormat = models.AudioFormatG711Ulaw }),
		"rates differ":     with(func(c *models.SessionConfig) { c.InputAudioSampleRate = models.SampleRate16000 }),
		"pcm at 8k":        with(func(c *models.SessionConfig) { c.InputAudioSampleRate, c.OutputAudioSampleRate = 8000, 8000 }),
		"pcm at 44.1k":     with(func(c *models.SessionConfig) { c.InputAudioSampleRate, c.OutputAudioSampleRate = 44100, 44100 }),
		"bad output rate":  with(func(c *models.SessionConfig) { c.OutputAudioSampleRate = 44100 }),
		"mu-law at 16k": with(func(c *models.SessionConfig) {
			c.InputAudioFormat, c.InputAudioSampleRate = models.AudioFormatG711Ulaw, 16000
		}),
		"A-law at 24k":          with(func(c *models.SessionConfig) { c.InputAudioFormat = models.AudioFormatG711Alaw }),
		"unknown format":        with(func(c *models.SessionConfig) { c.InputAudioFormat = "opus" }),
		"options not json":      options(`{`),
		"unknown option":        options(`{"turn_detection": {}}`),
		"unknown delegation":    options(`{"delegation": {"type": "server"}}`),
		"client with settings":  options(`{"delegation": {"type": "client", "responses": {"model": "m"}}}`),
		"responses no model":    options(`{"delegation": {"type": "responses", "responses": {"instructions": "x"}}}`),
		"responses no settings": options(`{"delegation": {"type": "responses"}}`),
		"tiny max tokens":       options(`{"delegation": {"type": "responses", "responses": {"model": "m", "max_output_tokens": 15}}}`),
		"two parts":             options(`{"input": [{"role": "user", "content": [{"type": "input_text", "text": "a"}, {"type": "input_text", "text": "b"}]}]}`),
		"no parts":              options(`{"input": [{"role": "user", "content": []}]}`),
		"user output part":      options(`{"input": [{"role": "user", "content": [{"type": "output_text", "text": "a"}]}]}`),
		"assistant input part":  options(`{"input": [{"role": "assistant", "content": [{"type": "input_text", "text": "a"}]}]}`),
		"system role":           options(`{"input": [{"role": "system", "content": [{"type": "input_text", "text": "a"}]}]}`),
		"bad status":            options(`{"input": [{"role": "user", "status": "done", "content": [{"type": "input_text", "text": "a"}]}]}`),
		"bad item type":         options(`{"input": [{"type": "function_call", "role": "user", "content": [{"type": "input_text", "text": "a"}]}]}`),
		"too much history":      options(historyOf(live.MaxInitialItems + 1)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := live.BuildSessionStart("", cfg); !errors.Is(err, live.ErrInvalidSessionConfig) {
				t.Fatalf("error = %v, want ErrInvalidSessionConfig", err)
			}
		})
	}
}

func historyOf(n int) string {
	items := make([]string, n)
	for i := range items {
		items[i] = `{"role":"user","content":[{"type":"input_text","text":"a"}]}`
	}
	return `{"input":[` + strings.Join(items, ",") + `]}`
}

func TestBuildSessionStartOmitsBlankFieldsAndDefaultsToClientDelegation(t *testing.T) {
	cfg := models.SessionConfig{Model: " gpt-live-1 ", Instructions: "  \n", Voice: "  ", Config: json.RawMessage(" null ")}
	start, err := live.BuildSessionStart("evt_1", cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := live.SessionStart{EventID: "evt_1", Session: live.SessionConfig{
		Model:      live.Model1,
		Audio:      &live.SessionAudio{Format: pcm24()},
		Delegation: clientDelegation(),
	}}
	if !reflect.DeepEqual(start, want) {
		t.Fatalf("built %#v, want %#v", start, want)
	}
	history, err := live.BuildSessionStart("", restaurantConfigWith(`{"store": false, "input": [{"role":"user","content":[{"type":"input_text","text":"Hi."}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if history.Session.Store == nil || *history.Session.Store || len(history.Session.Input) != 1 {
		t.Fatalf("store and input not carried: %#v", history.Session)
	}
}

func restaurantConfigWith(options string) models.SessionConfig {
	cfg := restaurantConfig()
	cfg.Config = json.RawMessage(options)
	return cfg
}
