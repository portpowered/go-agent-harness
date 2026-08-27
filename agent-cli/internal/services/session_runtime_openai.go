// This file owns OpenAI-specific session-runtime recording, websocket replay planning, and realtime session inferencer construction.
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func planOpenAIRecordRuntime(opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	sessionCfg, err := resolveOpenAIRealtimeSessionConfig(opts)
	if err != nil {
		return sessionRuntimePlan{}, err
	}

	liveDialer := opts.WebSocketDialer
	if liveDialer == nil {
		liveDialer = factory.newDefaultLiveDialer()
	}
	if liveDialer == nil {
		return sessionRuntimePlan{}, missingOwnedSessionDialerError(sessionProviderOpenAI)
	}
	recordingDialer := factory.newRecordingDialer(liveDialer, sessionProviderOpenAI, sessionCfg.Model)
	sessionInferencer, err := factory.newOpenAISessionInferencerForTools(sessionCfg, opts.Voice, recordingDialer, opts.ToolDefinitions)
	if err != nil {
		return sessionRuntimePlan{}, err
	}

	return sessionRuntimePlan{
		mode:        sessionRuntimeModeRecordOpenAI,
		provider:    sessionProviderOpenAI,
		model:       sessionCfg.Model,
		capturePath: opts.RecordPath,
		announce:    fmt.Sprintf("Starting OpenAI realtime session recording to %s", opts.RecordPath),
		inferencer:  sessionInferencer,
		loop: sessionLoopOptions{
			Prompt:                   opts.Prompt,
			CloseAfterOpen:           !opts.WaitForClose && len(opts.AudioInputs) == 0,
			WaitForClose:             opts.WaitForClose || len(opts.AudioInputs) > 0,
			CloseAfterScheduledAudio: len(opts.AudioInputs) > 0,
		},
		flushCapture: func() error {
			return recordingDialer.FlushToFile(opts.RecordPath)
		},
		finalize: func(_ context.Context, out io.Writer) error {
			_, err := fmt.Fprintf(out, "Wrote session capture to %s\n", opts.RecordPath)
			return err
		},
	}, nil
}

func planOpenAIReplayRuntime(opts SessionRunOptions, factory sessionRuntimeFactory) (sessionRuntimePlan, error) {
	replayDialer, err := factory.newReplayDialer(opts.ReplayPath)
	if err != nil {
		return sessionRuntimePlan{}, fmt.Errorf("replay session capture %s: %w", opts.ReplayPath, err)
	}
	model := replayDialer.Model()
	if strings.TrimSpace(model) == "" {
		model = openAIRealtimeModel
	}
	replayToolDefinitions, err := openAIReplayToolDefinitions(opts.ReplayPath, opts.ToolDefinitions)
	if err != nil {
		return sessionRuntimePlan{}, err
	}
	// A websocket replay owns its historical initial session.update payload.
	// Historical captures without a tools field must keep their original
	// outbound sequence, while a strict tool-bearing capture opts into the
	// selected definitions so the production provider config is validated.
	sessionInferencer, err := factory.newOpenAISessionInferencerForTools(config.OpenAIConfig{
		APIKey: "replay",
		Model:  model,
	}, opts.Voice, replayDialer, replayToolDefinitions)
	if err != nil {
		return sessionRuntimePlan{}, fmt.Errorf("replay session capture %s: %w", opts.ReplayPath, err)
	}
	return sessionRuntimePlan{
		mode:       sessionRuntimeModeReplayOpenAI,
		provider:   sessionProviderOpenAI,
		model:      model,
		inferencer: sessionInferencer,
		loop: sessionLoopOptions{
			Prompt:       opts.Prompt,
			WaitForClose: opts.WaitForClose || captureHasEvent(opts.ReplayPath, sessionClosedEventType),
			MaxDuration:  3 * time.Second,
		},
		finalize: func(_ context.Context, _ io.Writer) error {
			if err := replayDialer.Err(); err != nil {
				return fmt.Errorf("replay session capture %s: %w", opts.ReplayPath, err)
			}
			return nil
		},
	}, nil
}

// openAIReplayToolDefinitions selects definitions only for captures whose
// initial session.update explicitly contains a tools field. This keeps older
// no-tools fixtures replayable while making a tool-bearing fixture a strict
// assertion of the provider-facing session configuration.
func openAIReplayToolDefinitions(path string, definitions []messages.ToolDefinition) ([]messages.ToolDefinition, error) {
	if len(definitions) == 0 {
		return nil, nil
	}

	capture, err := gwtesting.LoadSessionCapture(path)
	if err != nil {
		return nil, fmt.Errorf("inspect replay session capture %s: %w", path, err)
	}
	for _, record := range capture.Records {
		if record.Direction != gwtesting.DirectionClientToServer || record.Type != "session.update" {
			continue
		}
		payload := record.Payload
		if len(payload) == 0 {
			payload = record.Data
		}
		var envelope struct {
			Session map[string]json.RawMessage `json:"session"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return nil, fmt.Errorf("decode replay session.update: %w", err)
		}
		if _, hasTools := envelope.Session["tools"]; !hasTools {
			return nil, nil
		}

		selected := make([]messages.ToolDefinition, len(definitions))
		copy(selected, definitions)
		for i := range selected {
			selected[i].Parameters = append([]messages.ToolParameter(nil), definitions[i].Parameters...)
		}
		return selected, nil
	}
	return nil, nil
}

func buildOpenAIRealtimeSessionInferencer(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer) (messages.SessionInferencer, error) {
	return buildOpenAIRealtimeSessionInferencerWithTools(sessionCfg, voice, dialer, nil)
}

func buildOpenAIRealtimeSessionInferencerWithTools(sessionCfg config.OpenAIConfig, voice string, dialer transport.Dialer, toolDefinitions []messages.ToolDefinition) (messages.SessionInferencer, error) {
	if dialer == nil {
		return nil, missingOwnedSessionDialerError(sessionProviderOpenAI)
	}
	opts := make([]oaiprovider.Option, 0, 1)
	opts = append(opts, oaiprovider.WithWebSocketDialer(dialer))
	return newOpenAIRealtimeSessionInferencerWithVoiceAndToolsAndOptions(sessionCfg, voice, toolDefinitions, opts...)
}
