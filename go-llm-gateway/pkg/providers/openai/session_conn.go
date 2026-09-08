package openai

// This file owns OpenAI Realtime provider connection setup and WebSocket I/O, including endpoint validation, session startup, read/write loops, and wire writes.
import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

const (
	realtimeFunctionCallOutputType = "function_call_output"
	realtimeConversationNone       = "none"
)

// ConnectSession establishes an OpenAI Realtime WebSocket session through the
// provider-agnostic session gateway contract.
func (p *OpenAIProvider) ConnectSession(ctx context.Context, config models.SessionConfig) (messages.Session, error) {
	if strings.TrimSpace(p.apiKey) == "" {
		return nil, fmt.Errorf("openai realtime: api key is required")
	}

	model := strings.TrimSpace(config.Model)
	if model == "" {
		model = p.model
	}
	endpoint, err := p.realtimeURL(model)
	if err != nil {
		return nil, err
	}

	headers := map[string]string{
		"Authorization": "Bearer " + p.apiKey,
	}
	if p.realtimeDialer == nil {
		return nil, fmt.Errorf("openai realtime: websocket dialer is required")
	}

	conn, err := p.realtimeDialer.Dial(endpoint, headers)
	if err != nil {
		return nil, fmt.Errorf("openai realtime: dial websocket %s: %w", safeEndpointForError(endpoint), err)
	}

	p.logger.Info("openai realtime: websocket connected", logging.Field{Key: "endpoint", Value: safeEndpointForError(endpoint)})

	session := newRealtimeSession(conn, p.logger)
	session.writeBackpressure = p.sessionWriteBackpressure
	session.mediaSampleRate = int(config.OutputAudioSampleRate)
	// Queue any immediate server audio before the read loop starts. A caller
	// that only consumes the normalized stream releases this speculative queue
	// on its first Receive call; an RTC caller claims it through RTCMedia.
	session.prepareRTCMedia()
	sessionUpdate, err := p.buildRealtimeSessionUpdate(config, model)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("openai realtime: build session update for %s: %w", safeEndpointForError(endpoint), err)
	}
	if err := session.writeEvent(sessionUpdate); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("openai realtime: send session update to %s: %w", safeEndpointForError(endpoint), err)
	}

	session.start(ctx)
	return session, nil
}

func (p *OpenAIProvider) realtimeURL(model string) (string, error) {
	base := strings.TrimSpace(p.realtimeBaseURL)
	if base == "" {
		base = strings.TrimSpace(p.baseURL)
	}
	if base == "" || base == defaultBaseURL {
		base = defaultRealtimeBaseURL
	}

	parsed, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("openai realtime: invalid endpoint: %w", err)
	}
	if parsed.Scheme != "wss" && parsed.Scheme != "ws" {
		return "", fmt.Errorf("openai realtime: invalid endpoint scheme %q for %s", parsed.Scheme, safeEndpointForError(base))
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("openai realtime: invalid endpoint host for %s", safeEndpointForError(base))
	}

	query := parsed.Query()
	if query.Get("model") == "" && model != "" {
		query.Set("model", model)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func safeEndpointForError(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "<invalid>"
	}
	parsed.User = nil
	query := parsed.Query()
	query.Del("key")
	query.Del("api_key")
	query.Del("access_token")
	query.Del("token")
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func (s *realtimeSession) start(ctx context.Context) {
	go s.readLoop(ctx)
	go s.writeLoop(ctx)
	go s.responseIntentLoop()
}

func (s *realtimeSession) readLoop(ctx context.Context) {
	for {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			if s.readStopped(ctx, err) {
				_ = s.Close()
				return
			}
			s.setTerminalError(err)
			s.logger.Error("openai realtime: websocket read error", logging.Field{Key: "error", Value: err})
			// Preserve unexpected read failures so callers can distinguish abrupt
			// provider closure from intentional shutdown.
			s.recvBuf.WriteTerminal(messages.StreamMessage{
				Type:  messages.StreamTypeError,
				Value: providers.NewStreamTransportErrorValue(err),
			})
			_ = s.Close()
			return
		}

		event, err := parseRealtimeServerEvent(data)
		if err != nil {
			s.setTerminalError(err)
			s.logger.Warn("openai realtime: failed to parse server event", logging.Field{Key: "error", Value: err})
			// An unparseable provider frame is a protocol violation, not a
			// skippable event: surface a classified terminal ERROR so consumers
			// can diagnose the failure instead of silently losing the stream.
			s.recvBuf.WriteTerminal(messages.StreamMessage{
				Type: messages.StreamTypeError,
				Value: messages.NewErrorValueWithTerminal(
					fmt.Sprintf("malformed provider event: %v", err),
					providers.ErrorClassInvalidRequest,
					messages.TerminalReasonTerminalFailure,
					messages.TerminalProvenanceGateway,
					messages.TerminalOutputNone,
				),
			})
			_ = s.Close()
			return
		}
		if event.Type == models.SessionEventSessionClosed {
			s.providerClosed.Store(true)
		}
		s.observeResponseLifecycle(event)
		if err := s.publishRTCMedia(ctx, event); err != nil {
			s.logger.Error("openai realtime: RTC media event failed", logging.Field{Key: "error", Value: err})
		}
		for _, msg := range realtimeInboundMessages(event) {
			// Provider frames are lossless. Apply backpressure when their bounded
			// normalized buffer fills, retaining both shutdown paths.
			if outcome := s.recvBuf.WriteWaitContextOrDone(ctx, s.done, msg); !outcome.OK() {
				if ctx.Err() != nil {
					_ = s.Close()
				}
				return
			}
		}
	}
}

func (s *realtimeSession) readStopped(ctx context.Context, err error) bool {
	select {
	case <-s.done:
		return true
	default:
		return ctx.Err() != nil || s.providerClosed.Load() && isProviderCloseTransportError(err)
	}
}

func (s *realtimeSession) observeResponseLifecycle(event models.SessionEvent) {
	switch event.Type {
	case models.SessionEventResponseCreated:
		s.observeResponseCreated(event)
	case models.SessionEventResponseDone:
		s.observeResponseDone(event)
	case models.SessionEventResponseOutputItemAdded:
		// A function-call response supersedes any standalone response request
		// that was queued for the same audio turn. Keep combined tool-result
		// intents, which are the continuation needed to complete this call.
		if firstStringField(event.Data, "item.type") == "function_call" {
			s.responseMu.Lock()
			s.responseHasFunctionCall = true
			s.suppressStandaloneResponseCreate = true
			s.toolResultAdmitted = false
			s.responseRetry = nil
			s.responseSent = false
			s.responseRetryPending = false
			s.dropStandaloneResponseIntentsLocked()
			s.responseMu.Unlock()
		}
	case models.SessionEventError:
		s.observeResponseCancelRejection(event)
		s.observeResponseCreateActiveError(event)
	}
}

func (s *realtimeSession) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			_ = s.Close()
			return
		case <-s.done:
			return
		case event := <-s.sendQueue.Chan():
			if err := s.writeEvent(event); err != nil {
				if isProviderCloseTransportError(err) && (s.providerClosed.Load() || sessionDone(s.done)) {
					s.outbound.Complete()
					return
				}
				s.setTerminalError(err)
				s.outbound.Complete()
				s.logger.Error("openai realtime: websocket write error", logging.Field{Key: "error", Value: err})
				_ = s.Close()
				return
			}
			s.markResponseRequestSent(event)
			s.outbound.Complete()
		}
	}
}

// FlushOutbound waits until events already admitted to the provider queue have
// completed their websocket writes. Queue admission alone is insufficient for
// a finite audio boundary: the runtime may close the session immediately after
// the commit control is acknowledged.
func (s *realtimeSession) FlushOutbound(ctx context.Context) error {
	if s == nil {
		return nil
	}
	for {
		if err := s.outbound.Flush(ctx, s.done, s.TerminalError); err != nil {
			return err
		}
		settlements := s.pendingAudioIntentSettlements()
		if len(settlements) == 0 {
			return nil
		}
		if err := s.waitForAudioIntentSettlements(ctx, settlements); err != nil {
			return err
		}
	}
}

func (s *realtimeSession) waitForAudioIntentSettlements(ctx context.Context, settlements []<-chan messages.SessionSendOutcome) error {
	for _, settled := range settlements {
		select {
		case outcome := <-settled:
			if err := deferredAudioIntentError(outcome); err != nil {
				return err
			}
		case <-s.done:
			if err := s.TerminalError(); err != nil {
				return err
			}
			return fmt.Errorf("provider session closed before deferred audio intent settled")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func deferredAudioIntentError(outcome messages.SessionSendOutcome) error {
	if outcome.OK() {
		return nil
	}
	if outcome.Err != nil {
		return fmt.Errorf("deferred audio intent failed with status %q: %w", outcome.Status, outcome.Err)
	}
	return fmt.Errorf("deferred audio intent failed with status %q", outcome.Status)
}

func (s *realtimeSession) pendingAudioIntentSettlements() []<-chan messages.SessionSendOutcome {
	s.responseWireMu.Lock()
	defer s.responseWireMu.Unlock()
	s.responseMu.Lock()
	defer s.responseMu.Unlock()
	settlements := make([]<-chan messages.SessionSendOutcome, 0, len(s.pendingResponseIntents)+1)
	for _, intent := range s.pendingResponseIntents {
		if intent.settled != nil {
			settlements = append(settlements, intent.settled)
		}
	}
	if s.activeResponseIntent != nil && s.activeResponseIntent.settled != nil {
		settlements = append(settlements, s.activeResponseIntent.settled)
	}
	return settlements
}

func (s *realtimeSession) writeEvent(event models.SessionEvent) error {
	payload := map[string]json.RawMessage{}
	if len(event.Data) > 0 {
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return fmt.Errorf("unmarshal event payload: %w", err)
		}
	}
	typeBytes, _ := json.Marshal(event.Type)
	payload["type"] = typeBytes
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return s.conn.WriteMessage(1, data)
}

func realtimeEventNeedsResponseAdmission(event models.SessionEvent) bool {
	if event.Type == models.SessionEventResponseCreate && !realtimeResponseCreateIsOutOfBand(event) {
		return true
	}
	if event.Type != conversationItemCreateEvent {
		return false
	}
	var payload struct {
		Item struct {
			Type string `json:"type"`
		} `json:"item"`
	}
	return json.Unmarshal(event.Data, &payload) == nil && payload.Item.Type == realtimeFunctionCallOutputType
}

func realtimeResponseCreateIsOutOfBand(event models.SessionEvent) bool {
	if event.Type != models.SessionEventResponseCreate || len(event.Data) == 0 {
		return false
	}
	var payload struct {
		Response struct {
			Conversation string `json:"conversation"`
		} `json:"response"`
	}
	return json.Unmarshal(event.Data, &payload) == nil && payload.Response.Conversation == realtimeConversationNone
}

func responseIntentHasFunctionCallOutput(intent responseIntent) bool {
	for _, event := range intent.events {
		if responseEventIsFunctionCallOutput(event) {
			return true
		}
	}
	return false
}

func responseEventIsFunctionCallOutput(event models.SessionEvent) bool {
	if event.Type != conversationItemCreateEvent {
		return false
	}
	var payload struct {
		Item struct {
			Type string `json:"type"`
		} `json:"item"`
	}
	return json.Unmarshal(event.Data, &payload) == nil && payload.Item.Type == realtimeFunctionCallOutputType
}

func realtimeResponseCreatedIsOutOfBand(event models.SessionEvent) bool {
	if event.Type != models.SessionEventResponseCreated || len(event.Data) == 0 {
		return false
	}
	var payload struct {
		Response struct {
			Conversation string `json:"conversation"`
		} `json:"response"`
	}
	return json.Unmarshal(event.Data, &payload) == nil && payload.Response.Conversation == realtimeConversationNone
}

func realtimeResponseDoneIsOutOfBand(event models.SessionEvent) bool {
	if event.Type != models.SessionEventResponseDone || len(event.Data) == 0 {
		return false
	}
	var payload struct {
		Response struct {
			Conversation string `json:"conversation"`
		} `json:"response"`
	}
	return json.Unmarshal(event.Data, &payload) == nil && payload.Response.Conversation == realtimeConversationNone
}
