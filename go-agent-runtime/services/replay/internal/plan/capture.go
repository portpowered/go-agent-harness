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
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	replayAudioChunkLimit   = 96 * 1024
	replayAppend            = "input_audio_buffer.append"
	replaySessionUpdate     = "session.update"
	replayCreateItem        = "conversation.item.create"
	replayTruncateItem      = "conversation.item.truncate"
	maxReplayResponseTarget = 128
)

// errSelfDrivingPlanUnavailable distinguishes a valid realtime capture whose
// client actions require the caller to drive them from a malformed capture.
// Inspection can still classify the former, while the explicit self-driving
// loader keeps its documented empty-plan behavior for that shape.
var errSelfDrivingPlanUnavailable = errors.New("self-driving replay plan unavailable")

type Service struct{}

type captureReplay struct {
	inner *gatewaytesting.SessionReplayer
}

// New constructs an inert replay planner.
func New() *Service { return &Service{} }

func (s *Service) Replay(ctx context.Context, path string) (replay.CaptureReplay, error) {
	if err := replayContextError(ctx); err != nil {
		return nil, err
	}
	capturePath, err := s.ResolveCapturePath(ctx, path)
	if err != nil {
		return nil, err
	}
	replayer, err := gatewaytesting.NewSessionReplayer(capturePath,
		gatewaytesting.WithReplayOutboundValidation(false),
		gatewaytesting.WithReplayContext(ctx),
	)
	if err != nil {
		return nil, fmt.Errorf("replay session capture %s: %w", path, err)
	}
	return &captureReplay{inner: replayer}, nil
}

func (r *captureReplay) Receive() <-chan messages.StreamMessage {
	if r == nil || r.inner == nil {
		return nil
	}
	return r.inner.Receive().Chan()
}

func (r *captureReplay) Done() <-chan struct{} {
	if r == nil || r.inner == nil {
		return nil
	}
	return r.inner.Done()
}

func (r *captureReplay) Err() error {
	if r == nil || r.inner == nil {
		return nil
	}
	return r.inner.Err()
}

func (r *captureReplay) Close() error {
	if r == nil || r.inner == nil {
		return nil
	}
	return r.inner.Close()
}

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
	if err != nil {
		if !errors.Is(err, errSelfDrivingPlanUnavailable) {
			return inspection, err
		}
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
		WaitForSessionUpdated:           replayHasSessionUpdated(records),
		StopAfterResponse:               !providerCloseExpected,
		ProviderCloseExpected:           providerCloseExpected,
		InterruptionReplacementExpected: replayHasInterruptionReplacement(records),
		InputAudioSampleRate:            inputRate,
		OutputAudioSampleRate:           outputRate,
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
	lifecycle := session.LiveReplayPlan{
		WaitForSessionUpdated:           waitForSessionUpdated,
		StopAfterResponse:               !providerCloseExpected,
		ProviderCloseExpected:           providerCloseExpected,
		InterruptionReplacementExpected: replayHasInterruptionReplacement(capture.Records),
		InputAudioSampleRate:            inputRate,
		OutputAudioSampleRate:           outputRate,
	}
	if len(actions) == 0 {
		return lifecycle, nil
	}
	if plan, ok, err := replayTextPlan(path, actions); ok || err != nil {
		if err != nil {
			if errors.Is(err, errSelfDrivingPlanUnavailable) {
				return session.LiveReplayPlan{}, err
			}
			return session.LiveReplayPlan{}, err
		}
		responseTarget, interruptionReplacementExpected, err := replayCaptureResponseTarget(path, capture.Records)
		if err != nil {
			return session.LiveReplayPlan{}, err
		}
		plan.WaitForSessionUpdated = waitForSessionUpdated
		plan.StopAfterResponse = !providerCloseExpected
		plan.ProviderCloseExpected = providerCloseExpected
		plan.ExpectedResponses = responseTarget
		plan.InterruptionReplacementExpected = interruptionReplacementExpected
		plan.InputAudioSampleRate = inputRate
		plan.OutputAudioSampleRate = outputRate
		return plan, err
	}
	if actions[0].Type != replayAppend {
		return lifecycle, nil
	}
	plan, err := replayAudioPlan(path, actions)
	if err != nil {
		// An incomplete audio action sequence is caller-driven: let the strict
		// transport validate the caller's actual outbound sequence instead of
		// rejecting the capture while deriving an optional self-driving plan.
		return session.LiveReplayPlan{}, fmt.Errorf("%w: %w", errSelfDrivingPlanUnavailable, err)
	}
	responseTarget, interruptionReplacementExpected, err := replayCaptureResponseTarget(path, capture.Records)
	if err != nil {
		return session.LiveReplayPlan{}, err
	}
	plan.WaitForSessionUpdated = waitForSessionUpdated
	plan.StopAfterResponse = !providerCloseExpected
	plan.ProviderCloseExpected = providerCloseExpected
	plan.ExpectedResponses = responseTarget
	plan.InterruptionReplacementExpected = interruptionReplacementExpected
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

// replayCaptureResponseTarget derives finite completion from correlated
// provider response identities. A cancelled interruption does not satisfy the
// target when the capture contains a distinct replacement response.
func replayCaptureResponseTarget(path string, records []gatewaytesting.CapturedSessionEvent) (int, bool, error) {
	createdResponses := make(map[string]struct{})
	toolResponses := make(map[string]struct{})
	terminalResponses := make(map[string]struct{})
	currentResponseID := ""
	cancelledResponseID := ""
	replacementExpected := false
	sawTerminal := false
	target := 0

	for _, record := range records {
		if record.Direction != gatewaytesting.DirectionServerToClient {
			continue
		}
		switch record.Type {
		case "response.created":
			response, err := replayResponseRecord(path, record, false)
			if err != nil {
				return 0, false, err
			}
			createdResponses[response.ID] = struct{}{}
			currentResponseID = response.ID
			if cancelledResponseID != "" && response.ID != cancelledResponseID {
				replacementExpected = true
				cancelledResponseID = ""
			}
		case "response.output_item.added", "response.output_item.done":
			if err := replayObserveToolResponse(path, record, currentResponseID, toolResponses); err != nil {
				return 0, false, err
			}
		case "response.done":
			response, err := replayResponseRecord(path, record, true)
			if err != nil {
				return 0, false, err
			}
			if _, duplicate := terminalResponses[response.ID]; duplicate {
				return 0, false, fmt.Errorf("live replay plan %s: duplicate response.done for response %q at sequence %d", path, response.ID, record.Sequence)
			}
			if _, created := createdResponses[response.ID]; !created {
				return 0, false, fmt.Errorf("live replay plan %s: response.done at sequence %d has no preceding response.created for response %q", path, record.Sequence, response.ID)
			}
			terminalResponses[response.ID] = struct{}{}
			sawTerminal = true
			if replayResponseWasCancelled(response.Status, response.StatusDetails) {
				if cancelledResponseID != "" {
					return 0, false, fmt.Errorf("live replay plan %s: multiple cancelled response boundaries before a replacement at sequence %d", path, record.Sequence)
				}
				cancelledResponseID = response.ID
				currentResponseID = ""
				continue
			}
			if cancelledResponseID != "" && !replacementExpected {
				return 0, false, fmt.Errorf("live replay plan %s: response %q completed after cancelled response %q without a distinct response.created boundary at sequence %d", path, response.ID, cancelledResponseID, record.Sequence)
			}
			if _, tool := toolResponses[response.ID]; !tool {
				target++
				if target > maxReplayResponseTarget {
					return 0, false, fmt.Errorf("live replay plan %s: response terminal target exceeds bounded limit %d at sequence %d", path, maxReplayResponseTarget, record.Sequence)
				}
			}
			currentResponseID = ""
		}
	}
	if target == 0 && (sawTerminal || replacementExpected) {
		target = 1
	}
	return target, replacementExpected, nil
}

type replayResponseBoundary struct {
	ID            string
	Status        string
	StatusDetails string
}

func replayResponseRecord(path string, record gatewaytesting.CapturedSessionEvent, requireStatus bool) (replayResponseBoundary, error) {
	var event struct {
		Type     string `json:"type"`
		Response *struct {
			ID            string `json:"id"`
			Status        string `json:"status"`
			StatusDetails *struct {
				Type string `json:"type"`
			} `json:"status_details"`
		} `json:"response"`
	}
	if err := json.Unmarshal(replayRecordPayload(record), &event); err != nil {
		return replayResponseBoundary{}, fmt.Errorf("live replay plan %s: decode %s at sequence %d: %w", path, record.Type, record.Sequence, err)
	}
	if event.Type != record.Type {
		return replayResponseBoundary{}, fmt.Errorf("live replay plan %s: %s at sequence %d has payload type %q", path, record.Type, record.Sequence, event.Type)
	}
	if event.Response == nil {
		return replayResponseBoundary{}, fmt.Errorf("live replay plan %s: %s at sequence %d is missing its response object", path, record.Type, record.Sequence)
	}
	responseID := strings.TrimSpace(event.Response.ID)
	if responseID == "" {
		return replayResponseBoundary{}, fmt.Errorf("live replay plan %s: %s at sequence %d is missing a response id", path, record.Type, record.Sequence)
	}
	statusDetails := ""
	if event.Response.StatusDetails != nil {
		statusDetails = strings.TrimSpace(event.Response.StatusDetails.Type)
	}
	if requireStatus && strings.TrimSpace(event.Response.Status) == "" && statusDetails == "" {
		return replayResponseBoundary{}, fmt.Errorf("live replay plan %s: response.done at sequence %d is missing status and status_details.type", path, record.Sequence)
	}
	return replayResponseBoundary{ID: responseID, Status: event.Response.Status, StatusDetails: statusDetails}, nil
}

func replayObserveToolResponse(path string, record gatewaytesting.CapturedSessionEvent, currentResponseID string, toolResponses map[string]struct{}) error {
	var event struct {
		Type       string `json:"type"`
		ResponseID string `json:"response_id"`
		Item       *struct {
			Type string `json:"type"`
		} `json:"item"`
	}
	if err := json.Unmarshal(replayRecordPayload(record), &event); err != nil {
		return fmt.Errorf("live replay plan %s: decode %s at sequence %d: %w", path, record.Type, record.Sequence, err)
	}
	if event.Type != record.Type {
		return fmt.Errorf("live replay plan %s: %s at sequence %d has payload type %q", path, record.Type, record.Sequence, event.Type)
	}
	if event.Item == nil {
		return fmt.Errorf("live replay plan %s: %s at sequence %d is missing its item object", path, record.Type, record.Sequence)
	}
	if event.Item.Type != "function_call" {
		return nil
	}
	responseID := strings.TrimSpace(event.ResponseID)
	if responseID == "" {
		responseID = currentResponseID
	}
	if responseID == "" {
		return fmt.Errorf("live replay plan %s: function_call at sequence %d has no response id", path, record.Sequence)
	}
	toolResponses[responseID] = struct{}{}
	return nil
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
