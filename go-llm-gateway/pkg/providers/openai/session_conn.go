package openai

// This file owns OpenAI Realtime provider connection setup, the realtime
// loop hooks, outbound flush and the classification of response events.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/internal/realtime"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
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

	session := newConfiguredRealtimeSession(conn, p.logger, realtimeSessionSettings{
		writeBackpressure:    p.sessionWriteBackpressure,
		clientTurnBoundaries: p.clientOwnsAudioTurnBoundaries,
		outputSampleRate:     int(config.OutputAudioSampleRate),
		inputSampleRate:      int(config.InputAudioSampleRate),
	})
	// Queue any immediate server audio before the read loop starts. A caller
	// that only consumes the normalized stream releases this speculative queue
	// on its first Receive call; an RTC caller claims it through RTCMedia.
	session.base.PrepareRTCMedia()
	sessionUpdate, err := p.buildRealtimeSessionUpdate(config, model)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("openai realtime: build session update for %s: %w", safeEndpointForError(endpoint), err), conn.Close())
	}
	if err := session.base.WriteEvent(sessionUpdate); err != nil {
		return nil, errors.Join(fmt.Errorf("openai realtime: send session update to %s: %w", safeEndpointForError(endpoint), err), conn.Close())
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
	s.base.Start(ctx, s)
	go s.responseIntentLoop(ctx)
}

func (s *realtimeSession) readLoop(ctx context.Context) { s.base.ReadLoop(ctx, s) }

func (s *realtimeSession) writeLoop(ctx context.Context) { s.base.WriteLoop(ctx, s) }

// HandleEvent tracks the provider response lifecycle, forwards audio to the
// RTC media path and translates the event for the normalized stream.
func (s *realtimeSession) HandleEvent(ctx context.Context, event models.SessionEvent) []messages.StreamMessage {
	if event.Type == models.SessionEventSessionClosed {
		s.providerClosed.Store(true)
	}
	s.observeResponseLifecycle(event)
	if err := s.publishRTCMedia(ctx, event); err != nil {
		s.base.Logger().Error("openai realtime: RTC media event failed", logging.Field{Key: "error", Value: err})
	}
	return realtimeInboundMessages(event)
}

// ExpectedReadClose treats a connection close after the provider announced
// session.closed as orderly.
func (s *realtimeSession) ExpectedReadClose(err error) bool {
	return s.providerClosed.Load() && isProviderCloseTransportError(err)
}

// ExpectedWriteClose treats a connection close after session.closed or Close
// as orderly; the read loop still drains the provider's final frames.
func (s *realtimeSession) ExpectedWriteClose(_ context.Context, err error) bool {
	return isProviderCloseTransportError(err) && (s.providerClosed.Load() || s.base.Closed())
}

// EventWritten arms the single retry of a written response.create.
func (s *realtimeSession) EventWritten(event models.SessionEvent) {
	s.markResponseRequestSent(event)
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
		if err := s.Surface.FlushOutbound(ctx); err != nil {
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
		case <-s.Done():
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
	st := &s.response
	settlements := make([]<-chan messages.SessionSendOutcome, 0, len(st.pending)+1)
	for _, intent := range st.pending {
		if intent.settled != nil {
			settlements = append(settlements, intent.settled)
		}
	}
	if st.inflight != nil && st.inflight.settled != nil {
		settlements = append(settlements, st.inflight.settled)
	}
	return settlements
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

func isProviderCloseTransportError(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) ||
		websocket.IsCloseError(err, websocket.CloseNormalClosure)
}

// NewDefaultWebSocketDialer returns the live OpenAI realtime WebSocket dialer.
func NewDefaultWebSocketDialer() transport.Dialer {
	return realtime.NewDialer()
}

func standaloneDefaultResponseIntent(intent responseIntent) bool {
	hasResponseCreate := false
	for _, event := range intent.events {
		switch event.Type { //nolint:exhaustive // Only response.create and conversation items classify an intent; other events are neutral.
		case models.SessionEventResponseCreate:
			if realtimeResponseCreateIsOutOfBand(event) {
				return false
			}
			hasResponseCreate = true
		case conversationItemCreateEvent:
			// A user message or tool result plus response.create is a fresh
			// turn or a tool continuation. It may legitimately be queued while
			// a function-call response is active, so it must never be
			// classified as a stale standalone request.
			return false
		default:
		}
	}
	return hasResponseCreate
}

func responseIntentIsToolWork(intent responseIntent) bool {
	for _, event := range intent.events {
		if responseEventIsFunctionCallOutput(event) || responseEventIsToolContinuation(event) {
			return true
		}
	}
	return false
}

func responseEventIsToolContinuation(event models.SessionEvent) bool {
	return firstStringField(event.Data, "response.metadata."+realtimeResponsePurposeKey) == string(messages.ResponsePurposeToolContinuation)
}
