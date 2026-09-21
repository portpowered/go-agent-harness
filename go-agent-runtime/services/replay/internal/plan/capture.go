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
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/internal/engine"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
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
	inner *engine.MessageSession
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
	loaded, err := loadReplayCapture(ctx, capturePath)
	if err != nil {
		return nil, fmt.Errorf("replay session capture %s: %w", path, err)
	}
	return &captureReplay{inner: engine.NewMessageSession(loaded.Capture.Records, ctx, false)}, nil
}

func (s *Service) NewSessionInferencer(ctx context.Context, path string) (messages.SessionInferencer, error) {
	if err := replayContextError(ctx); err != nil {
		return nil, err
	}
	capturePath, err := s.ResolveCapturePath(ctx, path)
	if err != nil {
		return nil, err
	}
	return sessionCaptureInferencer{path: capturePath}, nil
}

func (s *Service) AnalyzeTiming(ctx context.Context, path string) (replay.CaptureTimingReport, error) {
	if err := replayContextError(ctx); err != nil {
		return replay.CaptureTimingReport{}, err
	}
	capturePath, err := s.ResolveCapturePath(ctx, path)
	if err != nil {
		return replay.CaptureTimingReport{}, err
	}
	loaded, err := loadReplayCapture(ctx, capturePath)
	if err != nil {
		return replay.CaptureTimingReport{}, fmt.Errorf("load capture timing source %s: %w", path, err)
	}
	return analyzeCaptureTiming(loaded.Capture)
}

type sessionCaptureInferencer struct{ path string }

func (i sessionCaptureInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if err := replayContextError(ctx); err != nil {
		return nil, err
	}
	loaded, err := loadReplayCapture(ctx, i.path)
	if err != nil {
		return nil, err
	}
	return engine.NewMessageSession(loaded.Capture.Records, ctx, true), nil
}

func (r *captureReplay) Receive() <-chan messages.StreamMessage {
	if r == nil || r.inner == nil {
		return nil
	}
	return r.inner.Receive().Chan()
}

func (r *captureReplay) Drain(ctx context.Context, consume func(messages.StreamMessage) error) error {
	if err := replayContextError(ctx); err != nil {
		return err
	}
	if r == nil || r.inner == nil {
		return errors.New("replay capture is unavailable")
	}
	if consume == nil {
		return errors.New("replay message consumer is required")
	}
	input := r.Receive()
	done := r.Done()
	if input == nil || done == nil {
		return errors.New("replay capture stream is unavailable")
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return r.drainReady(ctx, input, consume)
		case message, ok := <-input:
			if !ok {
				return r.Err()
			}
			if err := consume(message); err != nil {
				return err
			}
		}
	}
}

func (r *captureReplay) drainReady(ctx context.Context, input <-chan messages.StreamMessage, consume func(messages.StreamMessage) error) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case message, ok := <-input:
			if !ok {
				return r.Err()
			}
			if err := consume(message); err != nil {
				return err
			}
		default:
			return r.Err()
		}
	}
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
		Facts:            captureFacts(loaded.Capture),
	}
	inspection.InitialTools, inspection.InitialToolsKnown = initialToolNames(loaded.Capture.Records)
	if !inspection.IsRealtime() {
		return inspection, nil
	}
	if err := engine.ValidateWebSocketCapture(loaded.Capture); err != nil {
		return replay.CaptureInspection{}, fmt.Errorf("validate realtime replay capture %s: %w", sourcePath, err)
	}
	inspection.Facts.RealtimeWebSocketReplayable = true
	plan, err := loadLivePlanFromCapture(ctx, capturePath, loaded.Capture)
	if err != nil {
		if !errors.Is(err, errSelfDrivingPlanUnavailable) {
			return inspection, err
		}
		// Preserve valid opening actions while leaving response-target correlation
		// unavailable; the strict loader still rejects incomplete self-driving plans.
		fallback, fallbackErr := replayCallerActionPlan(capturePath, loaded.Capture.Records)
		if fallbackErr != nil {
			return inspection, fallbackErr
		}
		inspection.LivePlan = &fallback
		return inspection, nil
	}
	if err != nil {
		return replay.CaptureInspection{}, err
	}
	inspection.LivePlan = &plan
	return inspection, nil
}

func captureFacts(capture gatewaytesting.SessionCapture) replay.CaptureFacts {
	facts := replay.CaptureFacts{Version: capture.Version, EventCount: len(capture.Records)}
	totals := make(map[metrics.SeriesKey]int64)
	toolDeltaSeen := make(map[string]bool)
	for _, record := range capture.Records {
		if record.Direction == gatewaytesting.DirectionClientToServer && strings.EqualFold(strings.TrimSpace(record.Type), replayAppend) {
			facts.ClientAudioAppendCount++
		}
		payloadBytes := record.Payload
		if len(payloadBytes) == 0 {
			payloadBytes = record.Data
		}
		var payload struct {
			Type      string `json:"type"`
			Delta     string `json:"delta"`
			Audio     string `json:"audio"`
			CallID    string `json:"call_id"`
			Arguments string `json:"arguments"`
			Synthetic string `json:"synthetic_audio"`
			Item      struct {
				Type    string `json:"type"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"item"`
		}
		if json.Unmarshal(payloadBytes, &payload) != nil {
			continue
		}
		switch record.Direction {
		case gatewaytesting.DirectionServerToClient:
			switch payload.Type {
			case "response.audio.delta", "response.output_audio.delta":
				addCaptureMetric(totals, metrics.DirectionOutput, metrics.ModalityAudio, decodedCaptureBase64Len(payload.Delta))
			case "response.text.delta", "response.output_text.delta", "response.audio_transcript.delta", "response.output_audio_transcript.delta":
				addCaptureMetric(totals, metrics.DirectionOutput, metrics.ModalityText, len(payload.Delta))
			case "response.function_call_arguments.delta":
				addCaptureMetric(totals, metrics.DirectionOutput, metrics.ModalityTool, len(payload.Delta))
				toolDeltaSeen[payload.CallID] = true
			case "response.function_call_arguments.done":
				if !toolDeltaSeen[payload.CallID] {
					addCaptureMetric(totals, metrics.DirectionOutput, metrics.ModalityTool, len(payload.Arguments))
					toolDeltaSeen[payload.CallID] = true
				}
			}
		case gatewaytesting.DirectionClientToServer:
			switch payload.Type {
			case "conversation.item.create":
				for _, part := range payload.Item.Content {
					if part.Type == "input_text" {
						addCaptureMetric(totals, metrics.DirectionInput, metrics.ModalityText, len(part.Text))
					}
				}
			case replayAppend:
				audio := payload.Audio
				if audio == "" {
					audio = payload.Synthetic
				}
				addCaptureMetric(totals, metrics.DirectionInput, metrics.ModalityAudio, decodedCaptureBase64Len(audio))
			}
		}
	}
	for _, direction := range metrics.SupportedDirections() {
		for _, modality := range metrics.SupportedModalities() {
			key := metrics.SeriesKey{Direction: direction, Modality: modality}
			if bytes := totals[key]; bytes > 0 {
				facts.MetricDeltas = append(facts.MetricDeltas, replay.CaptureMetricDelta{Direction: direction, Modality: modality, Bytes: bytes})
			}
		}
	}
	return facts
}

func addCaptureMetric(totals map[metrics.SeriesKey]int64, direction metrics.Direction, modality metrics.Modality, bytes int) {
	if bytes > 0 {
		totals[metrics.SeriesKey{Direction: direction, Modality: modality}] += int64(bytes)
	}
}

func decodedCaptureBase64Len(encoded string) int {
	if encoded == "" {
		return 0
	}
	decoded, err := codec.DecodeBase64(encoded)
	if err != nil {
		return len(encoded)
	}
	return len(decoded)
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
	responseTarget, interruptionReplacementExpected, err := replayCaptureResponseTarget(path, records)
	if err != nil {
		if !errors.Is(err, replayResponseIdentityUnavailableError{}) {
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
