package plan

// This file contains the small capture-derived plan used by self-driving
// replay. Provider handshake bytes stay with the provider service; this plan
// only recovers the first text action or bounded audio append chunks needed to
// drive the reusable live session.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	replayAudioChunkLimit = 96 * 1024
	replayAppend          = "input_audio_buffer.append"
	replaySessionUpdate   = "session.update"
	replayCreateItem      = "conversation.item.create"
)

// errSelfDrivingPlanUnavailable distinguishes a valid realtime capture whose
// client actions require the caller to drive them from a malformed capture.
// Inspection can still classify the former, while the explicit self-driving
// loader keeps its documented empty-plan behavior for that shape.
var errSelfDrivingPlanUnavailable = errors.New("self-driving replay plan unavailable")

type Service struct{}

// New constructs an inert replay planner.
func New() *Service { return &Service{} }

// InspectCapture performs the complete replay admission once and returns the
// host-facing metadata needed to select a session route. This keeps capture
// format detection, provider metadata, integrity warnings, and self-driving
// planning behind the replay service boundary.
func (s *Service) InspectCapture(ctx context.Context, path string) (replay.CaptureInspection, error) {
	if err := replayContextError(ctx); err != nil {
		return replay.CaptureInspection{}, err
	}
	sourcePath := path
	capturePath, err := s.ResolveCapturePath(ctx, path)
	if err != nil {
		return replay.CaptureInspection{}, err
	}
	loaded, err := loadReplayCapture(ctx, capturePath)
	if err != nil {
		return replay.CaptureInspection{}, fmt.Errorf("inspect live replay %s: %w", sourcePath, err)
	}
	inspection := replay.CaptureInspection{
		SourcePath:       sourcePath,
		CapturePath:      capturePath,
		Kind:             captureKind(loaded.Capture.Records),
		Provider:         loaded.Capture.Provider.Name,
		Model:            loaded.Capture.Provider.Model,
		IntegrityWarning: loaded.IntegrityWarning(sourcePath),
	}
	inspection.InitialTools, inspection.InitialToolsKnown = initialToolNames(loaded.Capture.Records)
	if !inspection.IsRealtime() {
		return inspection, nil
	}
	plan, err := loadLivePlanFromCapture(ctx, capturePath, loaded.Capture)
	if errors.Is(err, errSelfDrivingPlanUnavailable) {
		// A caller-driven capture is still a valid realtime artifact. The host
		// may provide its own prompt/audio actions; only the optional
		// self-driving actions are unavailable. Retain lifecycle metadata so a
		// recorded provider close remains authoritative after those actions run.
		metadata, metadataErr := replayLifecyclePlan(capturePath, loaded.Capture.Records)
		if metadataErr != nil {
			return inspection, metadataErr
		}
		inspection.LivePlan = &metadata
		return inspection, nil
	}
	if err != nil {
		return replay.CaptureInspection{}, err
	}
	inspection.LivePlan = &plan
	return inspection, nil
}

func replayLifecyclePlan(path string, records []gatewaytesting.CapturedSessionEvent) (session.LiveReplayPlan, error) {
	inputRate, outputRate, err := replayAudioSampleRates(records)
	if err != nil {
		return session.LiveReplayPlan{}, fmt.Errorf("live replay plan %s: %w", path, err)
	}
	providerCloseExpected := replayProviderCloseExpected(records)
	return session.LiveReplayPlan{
		WaitForSessionUpdated: replayHasSessionUpdated(records),
		StopAfterResponse:     !providerCloseExpected,
		ProviderCloseExpected: providerCloseExpected,
		InputAudioSampleRate:  inputRate,
		OutputAudioSampleRate: outputRate,
	}, nil
}

// LoadLivePlan extracts a narrow self-driving action sequence from an
// explicit WebSocket capture. Captures with other action shapes return an
// empty plan so the caller can continue with strict, caller-supplied replay.
func (*Service) LoadLivePlan(ctx context.Context, path string) (session.LiveReplayPlan, error) {
	if err := replayContextError(ctx); err != nil {
		return session.LiveReplayPlan{}, err
	}
	loaded, err := loadReplayCapture(ctx, path)
	if err != nil {
		return session.LiveReplayPlan{}, fmt.Errorf("load live replay plan %s: %w", path, err)
	}
	return loadLivePlanFromCapture(ctx, path, loaded.Capture)
}

func loadLivePlanFromCapture(ctx context.Context, path string, capture gatewaytesting.SessionCapture) (session.LiveReplayPlan, error) {
	if err := replayContextError(ctx); err != nil {
		return session.LiveReplayPlan{}, err
	}
	actions, err := replayDriverActions(capture.Records)
	if err != nil {
		return session.LiveReplayPlan{}, fmt.Errorf("live replay plan %s: %w", path, err)
	}
	providerCloseExpected := replayProviderCloseExpected(capture.Records)
	waitForSessionUpdated := replayHasSessionUpdated(capture.Records)
	inputRate, outputRate, err := replayAudioSampleRates(capture.Records)
	if err != nil {
		return session.LiveReplayPlan{}, fmt.Errorf("live replay plan %s: %w", path, err)
	}
	if len(actions) == 0 {
		return session.LiveReplayPlan{
			WaitForSessionUpdated: waitForSessionUpdated,
			StopAfterResponse:     !providerCloseExpected,
			ProviderCloseExpected: providerCloseExpected,
			InputAudioSampleRate:  inputRate,
			OutputAudioSampleRate: outputRate,
		}, nil
	}
	if plan, ok, err := replayTextPlan(path, actions); ok || err != nil {
		plan.WaitForSessionUpdated = waitForSessionUpdated
		plan.StopAfterResponse = !providerCloseExpected
		plan.ProviderCloseExpected = providerCloseExpected
		plan.InputAudioSampleRate = inputRate
		plan.OutputAudioSampleRate = outputRate
		return plan, err
	}
	if actions[0].Type != replayAppend {
		return session.LiveReplayPlan{}, nil
	}
	plan, err := replayAudioPlan(path, actions)
	plan.WaitForSessionUpdated = waitForSessionUpdated
	plan.StopAfterResponse = !providerCloseExpected
	plan.ProviderCloseExpected = providerCloseExpected
	plan.InputAudioSampleRate = inputRate
	plan.OutputAudioSampleRate = outputRate
	return plan, err
}

func loadReplayCapture(ctx context.Context, path string) (gatewaytesting.SessionCaptureReplayLoad, error) {
	if err := replayContextError(ctx); err != nil {
		return gatewaytesting.SessionCaptureReplayLoad{}, err
	}
	loaded, err := gatewaytesting.LoadSessionCaptureForReplay(path)
	if err != nil {
		return gatewaytesting.SessionCaptureReplayLoad{}, err
	}
	if err := replayContextError(ctx); err != nil {
		return gatewaytesting.SessionCaptureReplayLoad{}, err
	}
	return loaded, nil
}

func replayContextError(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("replay planning requires a context")
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	return nil
}

func captureKind(records []gatewaytesting.CapturedSessionEvent) replay.CaptureKind {
	for _, record := range records {
		if record.PayloadType == gatewaytesting.SessionPayloadTypeWebSocketMessage {
			return replay.CaptureKindRealtime
		}
	}
	return replay.CaptureKindTurn
}

func replayAudioSampleRates(records []gatewaytesting.CapturedSessionEvent) (int, int, error) {
	for _, record := range records {
		if record.Direction != gatewaytesting.DirectionClientToServer || record.Type != replaySessionUpdate {
			continue
		}
		var envelope struct {
			Session map[string]json.RawMessage `json:"session"`
		}
		if err := json.Unmarshal(replayRecordPayload(record), &envelope); err != nil {
			return 0, 0, fmt.Errorf("decode session configuration at sequence %d: %w", record.Sequence, err)
		}
		return replaySessionAudioSampleRates(envelope.Session)
	}
	return 0, 0, nil
}

func replaySessionAudioSampleRates(session map[string]json.RawMessage) (int, int, error) {
	var audio struct {
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
	if raw, ok := session["audio"]; ok {
		if err := json.Unmarshal(raw, &audio); err != nil {
			return 0, 0, fmt.Errorf("decode audio configuration: %w", err)
		}
	}
	inputRate := audio.Input.Format.Rate
	outputRate := audio.Output.Format.Rate
	var err error
	if inputRate <= 0 {
		inputRate, err = replayLegacyFormatRate(session["input_audio_format"])
		if err != nil {
			return 0, 0, fmt.Errorf("decode input audio format: %w", err)
		}
	}
	if outputRate <= 0 {
		outputRate, err = replayLegacyFormatRate(session["output_audio_format"])
		if err != nil {
			return 0, 0, fmt.Errorf("decode output audio format: %w", err)
		}
	}
	return inputRate, outputRate, nil
}

func replayLegacyFormatRate(raw json.RawMessage) (int, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}
	// Older captures name a codec without declaring a sample rate.
	if raw[0] == '"' {
		var codec string
		return 0, json.Unmarshal(raw, &codec)
	}
	var format struct {
		Rate int `json:"rate"`
	}
	if err := json.Unmarshal(raw, &format); err != nil {
		return 0, err
	}
	return format.Rate, nil
}

func replayHasSessionUpdated(records []gatewaytesting.CapturedSessionEvent) bool {
	// A replay only needs to wait for the handshake update when the capture
	// shows that update before the first client action. An update observed
	// after a client action is part of the provider's ordinary event stream and
	// cannot be used as an admission barrier: waiting for it would make a
	// replay with no handshake response hang indefinitely.
	sawClientAction := false
	for _, record := range records {
		if record.Direction == gatewaytesting.DirectionClientToServer && record.Type != replaySessionUpdate {
			sawClientAction = true
			continue
		}
		if record.Direction == gatewaytesting.DirectionServerToClient && record.Type == "session.updated" {
			return !sawClientAction
		}
	}
	return false
}

func replayProviderCloseExpected(records []gatewaytesting.CapturedSessionEvent) bool {
	for _, record := range records {
		if record.Direction == gatewaytesting.DirectionServerToClient && record.Type == "session.closed" {
			return true
		}
	}
	return false
}

func replayTextPlan(path string, actions []gatewaytesting.CapturedSessionEvent) (session.LiveReplayPlan, bool, error) {
	if actions[0].Type != replayCreateItem {
		return session.LiveReplayPlan{}, false, nil
	}
	prompt, ok, err := replayTextPrompt(path, actions[0])
	if err != nil || !ok {
		return session.LiveReplayPlan{}, false, err
	}
	// Providers differ on whether a text item implicitly starts a response.
	// OpenAI captures commonly contain an explicit response.create, while Grok
	// captures may contain only the conversation item before provider output.
	// Both are valid self-driving text turns; reject only additional client
	// actions that this narrow plan cannot reproduce safely.
	if len(actions) > 2 || (len(actions) == 2 && actions[1].Type != "response.create") {
		return session.LiveReplayPlan{}, true, fmt.Errorf("%w: live replay plan %s: recorded text prompt at sequence %d has unsupported additional client actions", errSelfDrivingPlanUnavailable, path, actions[0].Sequence)
	}
	if len(actions) == 2 {
		if err := replayPayloadType(actions[1], "response.create"); err != nil {
			return session.LiveReplayPlan{}, true, fmt.Errorf("live replay plan %s: %w", path, err)
		}
	}
	return session.LiveReplayPlan{OpeningPrompt: prompt, OpeningPromptPresent: true}, true, nil
}

func replayTextPrompt(path string, record gatewaytesting.CapturedSessionEvent) (string, bool, error) {
	payload := replayRecordPayload(record)
	var envelope struct {
		Type string          `json:"type"`
		Item json.RawMessage `json:"item"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return "", false, fmt.Errorf("live replay plan %s: decode conversation.item.create at sequence %d: %w", path, record.Sequence, err)
	}
	if envelope.Type != replayCreateItem {
		return "", false, fmt.Errorf("live replay plan %s: conversation.item.create at sequence %d has payload type %q", path, record.Sequence, envelope.Type)
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
		return "", false, fmt.Errorf("live replay plan %s: decode user item at sequence %d: %w", path, record.Sequence, err)
	}
	if item.Type != "message" || item.Role != "user" {
		return "", false, nil
	}
	if len(item.Content) != 1 || item.Content[0].Type != "input_text" || item.Content[0].Text == nil {
		return "", false, fmt.Errorf("live replay plan %s: user item at sequence %d must contain exactly one input_text part", path, record.Sequence)
	}
	return *item.Content[0].Text, true, nil
}
