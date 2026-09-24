package plan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/internal/plan/probe"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/internal/plan/response"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/internal/plan/timing"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

type Report = replay.CaptureTimingReport
type ResponseTiming = replay.CaptureResponseTiming
type ToolTiming = replay.CaptureToolTiming
type Summary = replay.CaptureTimingSummary
type DurationSummary = replay.CaptureDurationSummary

const ReportSchemaVersion = replay.CaptureTimingReportSchemaVersion

func analyzeCaptureTiming(capture gatewaytesting.SessionCapture) (Report, error) {
	return timing.AnalyzeCapture(capture)
}

func (s *Service) AnalyzeProbe(ctx context.Context, request replay.CaptureProbeRequest) (replay.CaptureProbeObservation, error) {
	return probe.AnalyzeProbe(ctx, s.ResolveCapturePath, request)
}

func (s *Service) AnalyzeProbeDocument(ctx context.Context, name string, document []byte) (replay.CaptureProbeObservation, error) {
	return probe.AnalyzeProbeDocument(ctx, name, document)
}

func (s *Service) InspectProbeDocument(ctx context.Context, name string, document []byte) (replay.CaptureProbeObservation, error) {
	return probe.InspectProbeDocument(ctx, name, document)
}

func (s *Service) WriteCaptureDocument(ctx context.Context, request replay.CaptureDocumentRequest, output io.Writer) error {
	return probe.WriteCaptureDocument(ctx, s.ResolveCapturePath, request, output)
}

func replayHasInterruptionReplacement(records []gatewaytesting.CapturedSessionEvent) bool {
	return response.HasInterruptionReplacement(records)
}

func replayCallerActionPlan(path string, records []gatewaytesting.CapturedSessionEvent) (session.LiveReplayPlan, error) {
	lifecycle, err := replayLifecyclePlan(path, records)
	if err != nil {
		return session.LiveReplayPlan{}, fmt.Errorf("live replay plan %s: %w", path, err)
	}
	actions, err := replayDriverActions(records)
	if err != nil {
		if errors.Is(err, errSelfDrivingPlanUnavailable) {
			return lifecycle, nil
		}
		return session.LiveReplayPlan{}, fmt.Errorf("live replay plan %s: %w", path, err)
	}
	if len(actions) == 0 {
		return lifecycle, nil
	}
	if actions[0].Type == replayCreateItem {
		plan, ok, planErr := replayTextPlan(path, actions)
		if planErr == nil && ok {
			applyReplayLifecycle(&plan, lifecycle)
			return plan, nil
		}
	}
	if actions[0].Type == replayAppend {
		plan, planErr := replayAudioPlan(path, actions)
		if planErr == nil {
			applyReplayLifecycle(&plan, lifecycle)
			return plan, nil
		}
	}
	return lifecycle, nil
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
	lifecycle, err := replayLifecyclePlan(path, capture.Records)
	if err != nil {
		return session.LiveReplayPlan{}, fmt.Errorf("live replay plan %s: %w", path, err)
	}
	if len(actions) == 0 {
		return lifecycle, nil
	}
	return replayActionsPlan(path, actions, capture.Records, lifecycle)
}

func replayActionsPlan(path string, actions []gatewaytesting.CapturedSessionEvent, records []gatewaytesting.CapturedSessionEvent, lifecycle session.LiveReplayPlan) (session.LiveReplayPlan, error) {
	if plan, ok, err := replayTextPlan(path, actions); ok || err != nil {
		if err != nil {
			return session.LiveReplayPlan{}, err
		}
		return attachReplayResponseTarget(path, records, lifecycle, plan)
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
	return attachReplayResponseTarget(path, records, lifecycle, plan)
}

func attachReplayResponseTarget(path string, records []gatewaytesting.CapturedSessionEvent, lifecycle, plan session.LiveReplayPlan) (session.LiveReplayPlan, error) {
	responseTarget, interruptionReplacementExpected, err := response.CaptureResponseTarget(path, records)
	if err != nil {
		if !response.IsIdentityUnavailable(err) {
			return session.LiveReplayPlan{}, err
		}
		// A capture can remain a valid caller-driven realtime artifact even when
		// its provider response boundaries cannot supply a self-driving target.
		// Keep the strict error for LoadLivePlan, while InspectCapture preserves
		// lifecycle metadata for callers that provide the turn actions.
		return session.LiveReplayPlan{}, fmt.Errorf("%w: %w", errSelfDrivingPlanUnavailable, err)
	}
	applyReplayLifecycle(&plan, lifecycle)
	plan.ExpectedResponses = responseTarget
	plan.InterruptionReplacementExpected = interruptionReplacementExpected
	return plan, nil
}

func applyReplayLifecycle(plan *session.LiveReplayPlan, lifecycle session.LiveReplayPlan) {
	if plan == nil {
		return
	}
	plan.WaitForSessionUpdated = lifecycle.WaitForSessionUpdated
	plan.StopAfterResponse = lifecycle.StopAfterResponse
	plan.ProviderCloseExpected = lifecycle.ProviderCloseExpected
	plan.InputAudioSampleRate = lifecycle.InputAudioSampleRate
	plan.OutputAudioSampleRate = lifecycle.OutputAudioSampleRate
}

func captureKind(records []gatewaytesting.CapturedSessionEvent) replay.CaptureKind {
	for _, record := range records {
		if record.PayloadType == gatewaytesting.SessionPayloadTypeWebSocketMessage {
			return replay.CaptureKindRealtime
		}
	}
	return replay.CaptureKindTurn
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
	var prompt *string
	for _, part := range item.Content {
		if part.Type != "input_text" || part.Text == nil {
			continue
		}
		if prompt != nil {
			return "", false, fmt.Errorf("live replay plan %s: user item at sequence %d contains multiple input_text parts", path, record.Sequence)
		}
		prompt = part.Text
	}
	if prompt == nil {
		return "", false, fmt.Errorf("live replay plan %s: user item at sequence %d must contain exactly one input_text part", path, record.Sequence)
	}
	return *prompt, true, nil
}
