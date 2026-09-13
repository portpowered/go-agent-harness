package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providersession"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

const (
	sessionUpdateEvent    = "session.update"
	sessionClosedEvent    = "session.closed"
	conversationItemEvent = "conversation.item.create"
	responseCreateEvent   = "response.create"
	inputAudioAppendEvent = "input_audio_buffer.append"
	inputAudioCommitEvent = "input_audio_buffer.commit"
)

type replaySessionConfiguration struct {
	payload               []byte
	model                 string
	inputAudioSampleRate  int
	outputAudioSampleRate int
	initialToolNames      []string
	initialToolsKnown     bool
}

type replayPromptPlan struct {
	prompt         string
	promptProvided bool
	barePrompt     bool
	bareAudio      []providersession.AudioInput
}

func (s *Service) PlanReplay(ctx context.Context, req providersession.ReplayRequest) (providersession.Plan, error) {
	if err := checkContext(ctx); err != nil {
		return providersession.Plan{}, err
	}
	provider, err := providerName(req.Provider)
	if err != nil {
		return providersession.Plan{}, err
	}
	configuration, err := loadReplaySessionConfiguration(req.ReplayPath)
	if err != nil {
		return providersession.Plan{}, err
	}
	replayDialer, err := s.replayDialer(req.ReplayPath, req.ReplayTiming)
	if err != nil {
		return providersession.Plan{}, fmt.Errorf("replay session capture %s: %w", req.ReplayPath, err)
	}
	model := replayModel(provider, configuration, replayDialer)
	prompt, err := replayPrompt(req, provider)
	if err != nil {
		return providersession.Plan{}, err
	}
	plan := buildReplayPlan(req, provider, model, configuration, replayDialer, prompt)
	plan.Inferencer, err = s.newInferencer(ctx, plan.Build)
	if err != nil {
		return providersession.Plan{}, fmt.Errorf("replay session capture %s: %w", req.ReplayPath, err)
	}
	if s.deps.WrapReplayInferencer != nil {
		plan.Inferencer = s.deps.WrapReplayInferencer(plan.Inferencer)
	}
	return plan, nil
}

func replayModel(provider string, configuration replaySessionConfiguration, replayDialer providersession.ReplayDialer) string {
	model := strings.TrimSpace(configuration.model)
	if model == "" {
		model = strings.TrimSpace(replayDialer.Model())
	}
	if model == "" {
		if provider == providersession.ProviderOpenAI {
			return defaultOpenAIModel
		}
		return defaultGrokModel
	}
	return model
}

func replayPrompt(req providersession.ReplayRequest, provider string) (replayPromptPlan, error) {
	plan := replayPromptPlan{prompt: req.Prompt, promptProvided: req.PromptProvided || req.Prompt != ""}
	if plan.promptProvided || len(req.AudioInputs) > 0 || req.ClientOwnsAudioTurnBoundaries || req.RoomReplay || provider != providersession.ProviderOpenAI {
		return plan, nil
	}
	capturedPrompt, err := loadReplaySessionPrompt(req.ReplayPath)
	if err != nil {
		return replayPromptPlan{}, err
	}
	if capturedPrompt != nil {
		plan.prompt, plan.promptProvided, plan.barePrompt = capturedPrompt.text, true, true
		return plan, nil
	}
	plan.bareAudio, err = loadReplaySessionAudioTurns(req.ReplayPath)
	return plan, err
}

func buildReplayPlan(req providersession.ReplayRequest, provider, model string, configuration replaySessionConfiguration, replayDialer providersession.ReplayDialer, prompt replayPromptPlan) providersession.Plan {
	bareAudioReplay := len(prompt.bareAudio) > 0
	scheduledAudio := len(req.AudioInputs) > 0 || bareAudioReplay
	dialer := transport.Dialer(&initialSessionUpdateDialer{inner: replayDialer, payload: configuration.payload, pace: provider == providersession.ProviderOpenAI && (prompt.barePrompt || bareAudioReplay)})
	build := providersession.BuildRequest{Provider: provider, Model: model, APIKey: "replay", Voice: req.Voice, Dialer: dialer, ClientOwnsAudioTurnBoundaries: scheduledAudio, InitialSessionUpdate: append([]byte(nil), configuration.payload...)}
	plan := providersession.Plan{Mode: replayMode(provider), Provider: provider, Model: model, Build: build, Dialer: dialer, InitialSessionUpdate: append([]byte(nil), configuration.payload...), InputAudioSampleRate: configuration.inputAudioSampleRate, OutputAudioSampleRate: configuration.outputAudioSampleRate, Prompt: prompt.prompt, PromptProvided: prompt.promptProvided, WaitForClose: req.WaitForClose || replayHasEvent(req.ReplayPath, sessionClosedEvent, provider == providersession.ProviderGrok), MaxDuration: replayMaxDuration(req.ReplayPath, req.ReplayTiming), Done: replayDialer.Done(), DoneErr: replayDialer.Err, CloseAfterScheduledAudio: len(req.AudioInputs) > 0, AnnounceTools: replayAnnouncementTools(req.ToolDefinitions, configuration.initialToolNames, configuration.initialToolsKnown), Finalize: func(_ context.Context, _ io.Writer) error { return replayFinalize(req.ReplayPath, replayDialer.Err()) }, ReplayComplete: prompt.barePrompt || bareAudioReplay}
	if provider != providersession.ProviderOpenAI {
		plan.AnnounceTools = nil
	}
	if bareAudioReplay {
		plan.AudioInputs = prompt.bareAudio
	}
	return plan
}

func replayFinalize(path string, err error) error {
	if err != nil {
		return fmt.Errorf("replay session capture %s: %w", path, err)
	}
	return nil
}

func replayMode(provider string) string {
	if provider == providersession.ProviderOpenAI {
		return providersession.ModeReplayOpenAI
	}
	return providersession.ModeReplayGrok
}

func (s *Service) replayDialer(path, timing string) (providersession.ReplayDialer, error) {
	if s.deps.NewReplayDialer != nil {
		return s.deps.NewReplayDialer(path, timing)
	}
	options := []gwtesting.ReplayWebSocketDialerOption{}
	if strings.EqualFold(strings.TrimSpace(timing), "recorded") {
		options = append(options, gwtesting.WithRecordedSessionTiming())
	}
	return gwtesting.NewReplayWebSocketDialer(path, options...)
}

func loadReplaySessionConfiguration(path string) (replaySessionConfiguration, error) {
	loaded, err := gwtesting.LoadSessionCaptureForReplay(path)
	if err != nil {
		return replaySessionConfiguration{}, fmt.Errorf("load replay session capture %s: %w", path, err)
	}
	for _, record := range loaded.Capture.Records {
		if record.Direction != gwtesting.DirectionClientToServer || record.Type != sessionUpdateEvent {
			continue
		}
		return decodeReplaySessionConfiguration(path, record)
	}
	return replaySessionConfiguration{}, fmt.Errorf("replay session capture %s: missing initial outbound %s configuration", path, sessionUpdateEvent)
}

func decodeReplaySessionConfiguration(path string, record gwtesting.CapturedSessionEvent) (replaySessionConfiguration, error) {
	payload, session, err := decodeReplaySession(path, record)
	if err != nil {
		return replaySessionConfiguration{}, err
	}
	tools, known, toolErr := replayToolNames(path, record.Sequence, session)
	model, err := replaySessionModel(path, record.Sequence, session)
	if err != nil {
		return replaySessionConfiguration{}, err
	}
	inputRate, outputRate := replayAudioRates(session)
	if inputRate > 0 && outputRate > 0 && inputRate != outputRate {
		return replaySessionConfiguration{}, fmt.Errorf("replay session capture %s: %w: input=%d Hz output=%d Hz", path, providersession.ErrAudioSampleRateConflict, inputRate, outputRate)
	}
	return replaySessionConfiguration{payload: append([]byte(nil), payload...), model: model, inputAudioSampleRate: inputRate, outputAudioSampleRate: outputRate, initialToolNames: tools, initialToolsKnown: known}, toolErr
}

func decodeReplaySession(path string, record gwtesting.CapturedSessionEvent) ([]byte, map[string]json.RawMessage, error) {
	payload := replayRecordPayload(record)
	if len(payload) == 0 {
		return nil, nil, fmt.Errorf("replay session capture %s: initial outbound %s at sequence %d has no payload", path, sessionUpdateEvent, record.Sequence)
	}
	var envelope struct {
		Type    string          `json:"type"`
		Session json.RawMessage `json:"session"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, nil, fmt.Errorf("replay session capture %s: decode initial outbound %s at sequence %d: %w", path, sessionUpdateEvent, record.Sequence, err)
	}
	if envelope.Type != sessionUpdateEvent {
		return nil, nil, fmt.Errorf("replay session capture %s: initial outbound %s at sequence %d has payload type %q", path, sessionUpdateEvent, record.Sequence, envelope.Type)
	}
	if len(envelope.Session) == 0 || string(envelope.Session) == "null" {
		return nil, nil, fmt.Errorf("replay session capture %s: initial outbound %s at sequence %d is missing the session configuration", path, sessionUpdateEvent, record.Sequence)
	}
	var session map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Session, &session); err != nil {
		return nil, nil, fmt.Errorf("replay session capture %s: decode session configuration at sequence %d: %w", path, record.Sequence, err)
	}
	if len(session) == 0 {
		return nil, nil, fmt.Errorf("replay session capture %s: initial outbound %s at sequence %d has an empty session configuration", path, sessionUpdateEvent, record.Sequence)
	}
	return payload, session, nil
}

func replaySessionModel(path string, sequence int, session map[string]json.RawMessage) (string, error) {
	raw, ok := session["model"]
	if !ok {
		return "", nil
	}
	var model string
	if err := json.Unmarshal(raw, &model); err != nil {
		return "", fmt.Errorf("replay session capture %s: session.model at sequence %d must be a string: %w", path, sequence, err)
	}
	return strings.TrimSpace(model), nil
}

func replayAudioRates(session map[string]json.RawMessage) (int, int) {
	type audioConfiguration struct {
		Input struct {
			Format struct {
				Rate int `json:"rate"`
			} `json:"format"`
		} `json:"input"`
		Output struct {
			Format struct {
				Rate int `json:"rate"`
			} `json:"format"`
		} `json:"output"`
	}
	var audio audioConfiguration
	if raw, ok := session["audio"]; ok {
		if err := json.Unmarshal(raw, &audio); err != nil {
			audio = audioConfiguration{}
		}
	}
	in, out := audio.Input.Format.Rate, audio.Output.Format.Rate
	var format struct {
		Rate int `json:"rate"`
	}
	if in <= 0 {
		if raw, ok := session["input_audio_format"]; ok {
			if err := json.Unmarshal(raw, &format); err != nil {
				format.Rate = 0
			}
			in = format.Rate
		}
	}
	format.Rate = 0
	if out <= 0 {
		if raw, ok := session["output_audio_format"]; ok {
			if err := json.Unmarshal(raw, &format); err != nil {
				format.Rate = 0
			}
			out = format.Rate
		}
	}
	return in, out
}

func replayToolNames(path string, sequence int, session map[string]json.RawMessage) ([]string, bool, error) {
	raw, ok := session["tools"]
	if !ok {
		return []string{}, true, nil
	}
	var tools []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil, true, fmt.Errorf("replay session capture %s: session.tools at sequence %d is invalid: %w", path, sequence, err)
	}
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		if name := strings.TrimSpace(tool.Name); name != "" {
			names = append(names, name)
		}
	}
	return names, true, nil
}

func replayAnnouncementTools(definitions []messages.ToolDefinition, names []string, known bool) []messages.ToolDefinition {
	if !known {
		return nil
	}
	allowed := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			allowed[name] = struct{}{}
		}
	}
	selected := make([]messages.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		if _, ok := allowed[strings.TrimSpace(definition.Name)]; ok {
			selected = append(selected, definition)
		}
	}
	return selected
}

type capturedPrompt struct{ text string }

func loadReplaySessionPrompt(path string) (*capturedPrompt, error) {
	loaded, err := gwtesting.LoadSessionCaptureForReplay(path)
	if err != nil {
		return nil, fmt.Errorf("load replay session capture %s: %w", path, err)
	}
	actions := replayClientActions(loaded.Capture.Records)
	var prompt *capturedPrompt
	position := -1
	for i, record := range actions {
		if record.Type != conversationItemEvent {
			continue
		}
		text, ok, err := parseReplayTextPrompt(path, record)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if prompt != nil {
			return nil, fmt.Errorf("replay session capture %s has ambiguous recorded text prompts at sequences %d and %d", path, actions[position].Sequence, record.Sequence)
		}
		prompt, position = &capturedPrompt{text: text}, i
	}
	if prompt == nil {
		return nil, nil
	}
	if err := validateReplayPrompt(path, actions, position); err != nil {
		return nil, err
	}
	return prompt, nil
}

func validateReplayPrompt(path string, actions []gwtesting.CapturedSessionEvent, position int) error {
	if position != 0 {
		return fmt.Errorf("replay session capture %s has an ambiguous recorded prompt at sequence %d: it must be the first client action after session.update", path, actions[position].Sequence)
	}
	if position+1 >= len(actions) || actions[position+1].Type != responseCreateEvent {
		return fmt.Errorf("replay session capture %s has an incomplete recorded prompt at sequence %d: expected the next client action to be %s", path, actions[position].Sequence, responseCreateEvent)
	}
	if position+2 != len(actions) {
		return fmt.Errorf("replay session capture %s has an ambiguous recorded prompt at sequence %d: expected only %s after it", path, actions[position].Sequence, responseCreateEvent)
	}
	return validateReplayEvent(path, actions[position+1], responseCreateEvent)
}

func replayClientActions(records []gwtesting.CapturedSessionEvent) []gwtesting.CapturedSessionEvent {
	actions := make([]gwtesting.CapturedSessionEvent, 0, len(records))
	for _, record := range records {
		if record.Direction == gwtesting.DirectionClientToServer && record.Type != sessionUpdateEvent {
			actions = append(actions, record)
		}
	}
	return actions
}

func parseReplayTextPrompt(path string, record gwtesting.CapturedSessionEvent) (string, bool, error) {
	payload := replayRecordPayload(record)
	if len(payload) == 0 {
		return "", false, fmt.Errorf("replay session capture %s: recorded %s at sequence %d has no payload", path, conversationItemEvent, record.Sequence)
	}
	var envelope struct {
		Type string          `json:"type"`
		Item json.RawMessage `json:"item"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return "", false, fmt.Errorf("replay session capture %s: decode recorded %s at sequence %d: %w", path, conversationItemEvent, record.Sequence, err)
	}
	if envelope.Type != conversationItemEvent {
		return "", false, fmt.Errorf("replay session capture %s: recorded %s at sequence %d has payload type %q", path, conversationItemEvent, record.Sequence, envelope.Type)
	}
	if len(envelope.Item) == 0 || string(envelope.Item) == "null" {
		return "", false, fmt.Errorf("replay session capture %s: recorded %s at sequence %d is missing its item", path, conversationItemEvent, record.Sequence)
	}
	var item struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string  `json:"type"`
			Text *string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(envelope.Item, &item); err != nil {
		return "", false, fmt.Errorf("replay session capture %s: decode recorded conversation item at sequence %d: %w", path, record.Sequence, err)
	}
	if item.Type != "message" || item.Role != "user" {
		return "", false, nil
	}
	if len(item.Content) == 0 {
		return "", false, fmt.Errorf("replay session capture %s: recorded user message at sequence %d has no content", path, record.Sequence)
	}
	for _, part := range item.Content {
		if part.Type != "input_text" {
			return "", false, nil
		}
	}
	if len(item.Content) != 1 || item.Content[0].Text == nil {
		return "", false, fmt.Errorf("replay session capture %s: recorded user message at sequence %d must contain exactly one input_text part with text", path, record.Sequence)
	}
	return *item.Content[0].Text, true, nil
}
