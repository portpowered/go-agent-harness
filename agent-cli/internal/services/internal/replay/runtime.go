package replay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	publicreplay "github.com/portpowered/go-agent-harness/agent-cli/internal/services/replay"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// NewOpenAIRuntimeFactory returns the production headless core runtime
// factory. It composes the same AgentLoop and OpenAI realtime adapter used by
// live sessions, but receives only Prepared's strict in-memory dialer and
// recorded tool executor.
func NewOpenAIRuntimeFactory() publicreplay.RuntimeFactory { return openAIRuntimeFactory{} }

type openAIRuntimeFactory struct{}

func (openAIRuntimeFactory) New(prepared publicreplay.Prepared) (publicreplay.Runtime, error) {
	providerName := strings.TrimSpace(prepared.Capture.Provider.Name)
	if providerName != "" && !strings.EqualFold(providerName, "openai") {
		return nil, fmt.Errorf("%w: production offline replay factory supports provider %q, got %q", publicreplay.ErrBundleMismatch, "openai", providerName)
	}
	model := strings.TrimSpace(prepared.Capture.Provider.Model)
	if model == "" {
		return nil, fmt.Errorf("%w: replay handshake has no model", publicreplay.ErrBundleMismatch)
	}
	initialUpdate, err := initialSessionUpdate(prepared.Capture)
	if err != nil {
		return nil, err
	}
	dialer := &initialUpdateDialer{inner: prepared.Dialer, payload: initialUpdate}
	provider := oaiprovider.New(
		oaiprovider.WithAPIKey("offline-replay"),
		oaiprovider.WithModel(model),
		oaiprovider.WithRealtimeBaseURL("ws://offline.invalid/realtime"),
		oaiprovider.WithWebSocketDialer(dialer),
	)
	sessionGateway, err := gateway.NewSessionGateway(gateway.WithSessionProvider(provider))
	if err != nil {
		return nil, fmt.Errorf("construct offline OpenAI gateway: %w", err)
	}
	inferencer := inference.NewSessionGatewayInferencer(sessionGateway, inference.WithSessionModel(model))
	loop, err := agentloop.New(
		agentloop.WithMode(engine.DuplexSession),
		agentloop.WithSessionInferencer(inferencer),
		agentloop.WithToolExecutor(prepared.ToolExecutor),
		agentloop.WithTools(replayToolDefinitions(prepared.Capture)),
		agentloop.WithBufferCapacity(128),
	)
	if err != nil {
		return nil, fmt.Errorf("construct offline agent loop: %w", err)
	}
	actions, err := deriveInputActions(prepared.Capture)
	if err != nil {
		return nil, err
	}
	return &coreRuntime{loop: loop, actions: actions}, nil
}

func replayToolDefinitions(capture gwtesting.SessionCapture) []messages.ToolDefinition {
	// The provider handshake is authoritative and may contain schema details
	// that are intentionally opaque to the replay service. The loop still needs
	// the names to enable its tool participant; the recorded executor validates
	// the complete call contract (ID, name, and arguments).
	seen := make(map[string]struct{})
	var definitions []messages.ToolDefinition
	for _, record := range capture.Records {
		if record.Direction != gwtesting.DirectionServerToClient || record.Type != "response.function_call_arguments.done" {
			continue
		}
		var event struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(record.Payload, &event) != nil || strings.TrimSpace(event.Name) == "" {
			continue
		}
		if _, ok := seen[event.Name]; ok {
			continue
		}
		seen[event.Name] = struct{}{}
		definitions = append(definitions, messages.ToolDefinition{Name: event.Name, ParametersClosed: true})
	}
	return definitions
}

type coreRuntime struct {
	loop    *agentloop.AgentLoop
	actions []replayInputAction
}

func (r *coreRuntime) Run(ctx context.Context, out io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type runState struct {
		done chan struct{}
		err  error
	}
	state := &runState{done: make(chan struct{})}
	readCtx, stopRead := context.WithCancel(runCtx)
	defer stopRead()
	go func() {
		state.err = r.loop.Run(runCtx)
		close(state.done)
		stopRead()
	}()
	if len(r.actions) == 0 {
		cancel()
		<-state.done
		return fmt.Errorf("%w: recorded provider session has no replayable user input", publicreplay.ErrBundleIncomplete)
	}
	// The provider's SESSION.OPEN boundary must be observed before injecting
	// the first user action. AgentLoop.Send only queues user input; without
	// this barrier the hot loop can write conversation.item.create while the
	// realtime read worker is still consuming session.created, which makes
	// otherwise valid replay depend on goroutine scheduling.
	for {
		message, err := r.loop.Deltas().ReadContext(readCtx)
		if err != nil {
			cancel()
			<-state.done
			if state.err != nil && !errors.Is(state.err, context.Canceled) && !errors.Is(state.err, io.EOF) {
				return fmt.Errorf("offline core loop: %w", state.err)
			}
			return fmt.Errorf("%w: provider session did not open: %v", publicreplay.ErrBundleIncomplete, err)
		}
		if message.Type == messages.StreamTypeSessionOpen {
			break
		}
	}
	for _, action := range r.actions {
		if err := r.runInput(runCtx, action); err != nil {
			cancel()
			<-state.done
			return err
		}
		for ended := 0; ended < action.responseEnds; {
			message, err := r.loop.Deltas().ReadContext(readCtx)
			if err != nil {
				// Run cancels readCtx as soon as the provider/session worker
				// exits. A final provider MESSAGE.END can already be buffered
				// alongside the worker completion, so drain buffered deltas
				// before treating cancellation as a failed replay. Ignore the
				// internal tool batch boundary while looking for the recorded
				// provider response boundary.
				for {
					buffered, ok := r.loop.Deltas().Read()
					if !ok {
						break
					}
					if isProviderMessageEnd(buffered) {
						ended++
						break
					}
				}
				if ended >= action.responseEnds {
					break
				}
				select {
				case <-state.done:
					if state.err != nil && !errors.Is(state.err, context.Canceled) && !errors.Is(state.err, io.EOF) {
						return fmt.Errorf("offline core loop: %w", state.err)
					}
					if state.err != nil {
						return fmt.Errorf("offline core loop stopped before response boundary: %w", state.err)
					}
					return fmt.Errorf("offline core loop stopped before response boundary")
				case <-runCtx.Done():
					return runCtx.Err()
				}
			}
			// AgentLoop.Deltas carries both provider model boundaries and the
			// internal tool runner's MESSAGE.END. Only provider response
			// boundaries discharge the response count derived from the wire;
			// counting the tool batch would cancel the loop before the
			// ToolResultForwarder can enqueue its function_call_output.
			if isProviderMessageEnd(message) {
				ended++
			}
			// AgentLoop.Run publishes completed text through an unbuffered
			// user-output channel. That channel is intentionally no longer
			// selected once Run is cancelled, so using SetOutputs here can lose a
			// final response when the provider boundary and shutdown race. The
			// public delta stream is the ordered output surface for this headless
			// runtime; write model text as it is consumed instead.
			if out != nil && message.ActorID == messages.Model {
				if text, ok := message.Value.(*messages.TextDeltaValue); ok {
					if _, err := io.WriteString(out, text.Content); err != nil {
						cancel()
						<-state.done
						return fmt.Errorf("write offline core output: %w", err)
					}
				}
			}
		}
	}
	cancel()
	<-state.done
	if state.err != nil && !errors.Is(state.err, context.Canceled) && !errors.Is(state.err, io.EOF) {
		return state.err
	}
	return nil
}

func isProviderMessageEnd(message messages.StreamMessage) bool {
	if message.Type != messages.StreamTypeMessageEnd {
		return false
	}
	return message.ActorID == messages.Model || message.ResponseID != ""
}

func (r *coreRuntime) runInput(ctx context.Context, action replayInputAction) error {
	if action.text != "" {
		return r.loop.Send(ctx, []messages.Message{messages.NewTextMessage(messages.RoleUser, action.text)})
	}
	for _, pcm := range action.audio {
		if err := r.loop.SendAudioInput(ctx, pcm); err != nil {
			return err
		}
	}
	if len(action.audio) > 0 {
		return r.loop.SendSessionEvent(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	}
	return nil
}

type replayInputAction struct {
	startIndex   int
	text         string
	audio        [][]byte
	responseEnds int
}

func deriveInputActions(capture gwtesting.SessionCapture) ([]replayInputAction, error) {
	var actions []replayInputAction
	audioAction := -1
	for index, record := range capture.Records {
		if record.Direction != gwtesting.DirectionClientToServer {
			continue
		}
		payload := record.Payload
		var envelope struct {
			Type string `json:"type"`
			Item struct {
				Type    string `json:"type"`
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"item"`
			Audio string `json:"audio"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return nil, fmt.Errorf("%w: decode recorded input at sequence %d: %v", publicreplay.ErrBundleIncomplete, record.Sequence, err)
		}
		switch envelope.Type {
		case "conversation.item.create":
			audioAction = -1
			if envelope.Item.Type != "message" || envelope.Item.Role != "user" {
				continue
			}
			var text strings.Builder
			for _, part := range envelope.Item.Content {
				if part.Type == "input_text" {
					text.WriteString(part.Text)
				}
			}
			if text.Len() > 0 {
				actions = append(actions, replayInputAction{startIndex: index, text: text.String()})
			}
		case "input_audio_buffer.append":
			if envelope.Audio == "" {
				return nil, fmt.Errorf("%w: empty recorded audio input at sequence %d", publicreplay.ErrBundleIncomplete, record.Sequence)
			}
			pcm, err := codec.DecodeBase64(envelope.Audio)
			if err != nil {
				return nil, fmt.Errorf("%w: decode recorded audio input at sequence %d: %v", publicreplay.ErrBundleIncomplete, record.Sequence, err)
			}
			if audioAction >= 0 {
				actions[audioAction].audio = append(actions[audioAction].audio, pcm)
			} else {
				actions = append(actions, replayInputAction{startIndex: index, audio: [][]byte{pcm}})
				audioAction = len(actions) - 1
			}
		case "input_audio_buffer.commit", "response.create", "response.cancel":
			if envelope.Type == "response.cancel" {
				return nil, fmt.Errorf("%w: response cancellation and overlapping input are not supported by the headless replay factory", publicreplay.ErrBundleMismatch)
			}
			audioAction = -1
		}
	}
	for index := range actions {
		end := len(capture.Records)
		if index+1 < len(actions) {
			end = actions[index+1].startIndex
		}
		for _, record := range capture.Records[actions[index].startIndex:end] {
			if record.Direction == gwtesting.DirectionServerToClient && record.Type == "response.done" {
				actions[index].responseEnds++
			}
		}
		if index+1 < len(actions) && actions[index].responseEnds == 0 {
			return nil, fmt.Errorf("%w: recorded input overlaps an unfinished provider response", publicreplay.ErrBundleMismatch)
		}
	}
	return actions, nil
}

func initialSessionUpdate(capture gwtesting.SessionCapture) ([]byte, error) {
	for _, record := range capture.Records {
		if record.Direction == gwtesting.DirectionClientToServer && record.Type == "session.update" {
			return append([]byte(nil), record.Payload...), nil
		}
	}
	return nil, fmt.Errorf("%w: initial session.update is missing", publicreplay.ErrBundleIncomplete)
}

type initialUpdateDialer struct {
	inner   transport.Dialer
	payload []byte
}

func (d *initialUpdateDialer) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	conn, err := d.inner.Dial(endpoint, headers)
	if err != nil {
		return nil, err
	}
	return &initialUpdateConn{inner: conn, payload: append([]byte(nil), d.payload...)}, nil
}

type initialUpdateConn struct {
	inner   transport.Conn
	payload []byte
	written bool
}

func (c *initialUpdateConn) ReadMessage() (int, []byte, error) { return c.inner.ReadMessage() }

func (c *initialUpdateConn) WriteMessage(messageType int, payload []byte) error {
	if !c.written {
		var envelope struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(payload, &envelope) == nil && envelope.Type == "session.update" {
			payload = c.payload
		}
		c.written = true
	}
	return c.inner.WriteMessage(messageType, payload)
}

func (c *initialUpdateConn) Close() error { return c.inner.Close() }
