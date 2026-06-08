package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-llm-gateway/pkg/providers"
)

// Interact runs a provider-backed model interaction and emits the normalized
// PNIG event contract.
func (g *DefaultGateway) Interact(ctx context.Context, req InteractionRequest) (<-chan InteractionEvent, error) {
	interactionID := req.InteractionID
	if interactionID == "" {
		interactionID = newInteractionID()
	}

	out := make(chan InteractionEvent)
	go func() {
		defer close(out)

		emitter := newInteractionEventEmitter(out, interactionID, g.provider.Name(), req.Model, req.ContinueFromSequence)
		if err := ctx.Err(); err != nil {
			emitter.emitTerminalForErr(err)
			return
		}
		if err := emitter.emit(ctx, InteractionEvent{Type: InteractionEventStart}); err != nil {
			emitter.emitTerminalForErr(err)
			return
		}

		if err := validateInteractionToolResults(req); err != nil {
			_ = emitter.emitTerminal(ctx, InteractionEvent{
				Type: InteractionEventError,
				Error: &InteractionError{
					Code:           "tool_result_validation_error",
					Message:        err.Error(),
					Classification: providers.ErrorClassInvalidRequest,
				},
			})
			return
		}

		for _, result := range req.ToolResults {
			result := result
			if err := emitter.emit(ctx, InteractionEvent{
				Type:        InteractionEventToolResultAccepted,
				Correlation: InteractionCorrelation{ToolCallID: result.ToolCallID},
				ToolResult:  &result,
			}); err != nil {
				emitter.emitTerminalForErr(err)
				return
			}
		}

		resp, err := g.provider.Infer(ctx, interactionProviderRequest(req))
		if err != nil {
			emitter.emitTerminalForErr(err)
			return
		}

		text := resp.Message.TextContent()
		if text != "" {
			if err := emitter.emit(ctx, InteractionEvent{
				Type:      InteractionEventTextDelta,
				TextDelta: &TextDeltaEvent{Content: text},
			}); err != nil {
				emitter.emitTerminalForErr(err)
				return
			}
			emitter.markOutputEmitted()
			if err := ctx.Err(); err != nil {
				emitter.emitTerminalForErr(err)
				return
			}
		}

		toolCalls := normalizedInteractionToolCallsFromModel(resp.Message.ToolCalls)
		if len(toolCalls) > 0 {
			for _, call := range toolCalls {
				call := call
				if err := emitter.emit(ctx, InteractionEvent{
					Type:        InteractionEventToolCallRequest,
					Correlation: InteractionCorrelation{ToolCallID: call.ID},
					ToolCall:    &call,
				}); err != nil {
					emitter.emitTerminalForErr(err)
					return
				}
				if err := ctx.Err(); err != nil {
					emitter.emitTerminalForErr(err)
					return
				}
			}
			if usage, ok := interactionUsageFromModel(resp.Usage); ok {
				if err := emitter.emit(ctx, InteractionEvent{
					Type:  InteractionEventUsage,
					Usage: &usage,
				}); err != nil {
					emitter.emitTerminalForErr(err)
					return
				}
				if err := ctx.Err(); err != nil {
					emitter.emitTerminalForErr(err)
					return
				}
			}
			_ = emitter.emitTerminal(ctx, InteractionEvent{Type: InteractionEventEnd})
			return
		}

		if err := emitter.emit(ctx, InteractionEvent{
			Type:         InteractionEventFinalMessage,
			FinalMessage: interactionMessageFromModel(resp.Message),
		}); err != nil {
			emitter.emitTerminalForErr(err)
			return
		}
		if err := ctx.Err(); err != nil {
			emitter.emitTerminalForErr(err)
			return
		}

		if usage, ok := interactionUsageFromModel(resp.Usage); ok {
			if err := emitter.emit(ctx, InteractionEvent{
				Type:  InteractionEventUsage,
				Usage: &usage,
			}); err != nil {
				emitter.emitTerminalForErr(err)
				return
			}
			if err := ctx.Err(); err != nil {
				emitter.emitTerminalForErr(err)
				return
			}
		}

		_ = emitter.emitTerminal(ctx, InteractionEvent{Type: InteractionEventEnd})
	}()

	return out, nil
}

type interactionEventEmitter struct {
	out           chan<- InteractionEvent
	interactionID string
	provider      string
	model         string
	sequence      int64
	outputEmitted bool
}

func newInteractionEventEmitter(out chan<- InteractionEvent, interactionID, provider, model string, sequence int64) *interactionEventEmitter {
	return &interactionEventEmitter{
		out:           out,
		interactionID: interactionID,
		provider:      provider,
		model:         model,
		sequence:      sequence,
	}
}

func (e *interactionEventEmitter) emit(ctx context.Context, event InteractionEvent) error {
	e.sequence++
	now := time.Now().UTC()
	event.InteractionID = e.interactionID
	event.Sequence = e.sequence
	event.Provider = e.provider
	event.Model = e.model
	event.CreatedAt = &now

	select {
	case <-ctx.Done():
		e.sequence--
		return ctx.Err()
	case e.out <- event:
		return nil
	}
}

func (e *interactionEventEmitter) emitTerminal(ctx context.Context, event InteractionEvent) error {
	if err := e.emit(ctx, event); err != nil {
		return err
	}
	if event.Type == InteractionEventEnd {
		return nil
	}
	return e.emitRaw(InteractionEvent{Type: InteractionEventEnd})
}

func (e *interactionEventEmitter) emitTerminalForErr(err error) {
	if err == nil {
		return
	}
	if errors.Is(err, context.Canceled) {
		_ = e.emitTerminalRaw(InteractionEvent{
			Type: InteractionEventCancellation,
			Cancellation: &InteractionCancellation{
				Reason:         "caller_cancelled",
				Message:        err.Error(),
				Classification: providers.ErrorClassification(err),
				OutputState:    e.outputStateForTerminal(),
			},
		})
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		_ = e.emitTerminalRaw(InteractionEvent{
			Type: InteractionEventError,
			Error: &InteractionError{
				Code:           "provider_timeout",
				Message:        err.Error(),
				Classification: providers.ErrorClassTransport,
				Retryable:      true,
			},
		})
		return
	}
	_ = e.emitTerminalRaw(InteractionEvent{
		Type: InteractionEventError,
		Error: &InteractionError{
			Code:           "provider_error",
			Message:        err.Error(),
			Classification: interactionErrorClassification(err),
		},
	})
}

func interactionErrorClassification(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrCancellation):
		return providers.ErrorClassCancellation
	case errors.Is(err, ErrReplayMismatch):
		return providers.ErrorClassReplayMismatch
	case errors.Is(err, ErrAuthentication), errors.Is(err, ErrAuthorization):
		return providers.ErrorClassAuthentication
	case errors.Is(err, ErrRateLimit):
		return providers.ErrorClassRateLimited
	case errors.Is(err, ErrInvalidRequest):
		return providers.ErrorClassInvalidRequest
	case errors.Is(err, ErrUnsupportedModel):
		return providers.ErrorClassUnsupportedRequest
	case errors.Is(err, ErrTransport):
		return providers.ErrorClassTransport
	case errors.Is(err, ErrProviderHTTPStatus):
		return providers.ErrorClassProviderRejected
	default:
		return providers.ErrorClassification(err)
	}
}

func (e *interactionEventEmitter) markOutputEmitted() {
	e.outputEmitted = true
}

func (e *interactionEventEmitter) outputStateForTerminal() string {
	if e.outputEmitted {
		return providers.ErrorClassPartialOutput
	}
	return ""
}

func (e *interactionEventEmitter) emitTerminalRaw(event InteractionEvent) error {
	if err := e.emitRaw(event); err != nil {
		return err
	}
	if event.Type == InteractionEventEnd {
		return nil
	}
	return e.emitRaw(InteractionEvent{Type: InteractionEventEnd})
}

func (e *interactionEventEmitter) emitRaw(event InteractionEvent) error {
	e.sequence++
	now := time.Now().UTC()
	event.InteractionID = e.interactionID
	event.Sequence = e.sequence
	event.Provider = e.provider
	event.Model = e.model
	event.CreatedAt = &now
	e.out <- event
	return nil
}

func interactionProviderRequest(req InteractionRequest) providers.InferenceRequest {
	return providers.InferenceRequest{
		Messages: interactionMessagesToModel(req),
		Tools:    interactionToolsToModel(req.Tools),
		Model:    req.Model,
		Config:   req.Config,
	}
}

func interactionMessagesToModel(req InteractionRequest) []models.Message {
	msgs := make([]models.Message, 0, len(req.SystemInstructions)+len(req.Messages)+len(req.ToolResults))
	for _, instruction := range req.SystemInstructions {
		msgs = append(msgs, models.NewTextMessage(models.RoleSystem, instruction))
	}
	for _, msg := range req.Messages {
		msgs = append(msgs, interactionMessageToModel(msg))
	}
	for _, result := range req.ToolResults {
		msgs = append(msgs, interactionToolResultToModel(result))
	}
	return msgs
}

func interactionMessageToModel(msg InteractionMessage) models.Message {
	return models.Message{
		Role:         modelRoleFromInteraction(msg.Role),
		ContentParts: interactionContentToModel(msg.ContentParts),
		ToolCalls:    interactionToolCallsToModel(msg.ToolCalls),
		ToolCallID:   msg.ToolCallID,
		Name:         msg.Name,
	}
}

func interactionMessageFromModel(msg models.Message) *InteractionMessage {
	return &InteractionMessage{
		Role:         interactionRoleFromModel(msg.Role),
		ContentParts: interactionContentFromModel(msg.ContentParts),
		ToolCalls:    normalizedInteractionToolCallsFromModel(msg.ToolCalls),
		ToolCallID:   msg.ToolCallID,
		Name:         msg.Name,
	}
}

func interactionToolResultToModel(result InteractionToolResult) models.Message {
	return models.Message{
		Role:         models.RoleTool,
		ToolCallID:   result.ToolCallID,
		Name:         result.Name,
		ContentParts: []models.ContentPart{models.TextPart{Text: interactionToolResultContent(result)}},
	}
}

func interactionToolResultContent(result InteractionToolResult) string {
	if len(result.Payload) > 0 {
		return string(result.Payload)
	}
	if result.Error != "" {
		return result.Error
	}
	return result.Content
}

func interactionContentToModel(parts []InteractionContent) []models.ContentPart {
	out := make([]models.ContentPart, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case InteractionContentText:
			out = append(out, models.TextPart{Text: part.Text})
		case InteractionContentImage:
			out = append(out, models.ImagePart{URL: part.URL, Bytes: part.Bytes, MediaType: part.MediaType})
		case InteractionContentAudio:
			out = append(out, models.AudioPart{URL: part.URL, Bytes: part.Bytes, MediaType: part.MediaType})
		case InteractionContentVideo:
			out = append(out, models.VideoPart{URL: part.URL, Bytes: part.Bytes, MediaType: part.MediaType})
		case InteractionContentFile:
			out = append(out, messages.FilePart{URL: part.URL, Bytes: part.Bytes, Name: part.Name, MediaType: part.MediaType})
		}
	}
	return out
}

func interactionContentFromModel(parts []models.ContentPart) []InteractionContent {
	out := make([]InteractionContent, 0, len(parts))
	for _, part := range parts {
		switch p := part.(type) {
		case models.TextPart:
			out = append(out, InteractionContent{Type: InteractionContentText, Text: p.Text})
		case models.ImagePart:
			out = append(out, InteractionContent{Type: InteractionContentImage, URL: p.URL, Bytes: p.Bytes, MediaType: p.MediaType})
		case models.AudioPart:
			out = append(out, InteractionContent{Type: InteractionContentAudio, URL: p.URL, Bytes: p.Bytes, MediaType: p.MediaType})
		case models.VideoPart:
			out = append(out, InteractionContent{Type: InteractionContentVideo, URL: p.URL, Bytes: p.Bytes, MediaType: p.MediaType})
		case messages.FilePart:
			out = append(out, InteractionContent{Type: InteractionContentFile, URL: p.URL, Bytes: p.Bytes, Name: p.Name, MediaType: p.MediaType})
		}
	}
	return out
}

func interactionToolsToModel(tools []InteractionTool) []models.ToolDefinition {
	out := make([]models.ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		params := make([]models.ToolParameter, 0, len(tool.Parameters))
		for _, param := range tool.Parameters {
			params = append(params, models.ToolParameter{
				Name:        param.Name,
				Type:        param.Type,
				Description: param.Description,
				Required:    param.Required,
			})
		}
		out = append(out, models.ToolDefinition{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  params,
		})
	}
	return out
}

func interactionToolCallsToModel(calls []InteractionToolCall) []models.ToolCall {
	out := make([]models.ToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, models.ToolCall{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: string(call.Arguments),
		})
	}
	return out
}

func normalizedInteractionToolCallsFromModel(calls []models.ToolCall) []InteractionToolCall {
	out := make([]InteractionToolCall, 0, len(calls))
	for i, call := range calls {
		id := call.ID
		if id == "" {
			id = "tool-call-" + strconv.Itoa(i+1)
		}
		out = append(out, InteractionToolCall{
			ID:        id,
			Name:      call.Name,
			Arguments: json.RawMessage(call.Arguments),
		})
	}
	return out
}

func validateInteractionToolResults(req InteractionRequest) error {
	if len(req.ToolResults) == 0 {
		return nil
	}

	pending := latestInteractionToolCalls(req.Messages)
	if len(pending) == 0 {
		return fmt.Errorf("unknown tool result %q: no pending tool calls", req.ToolResults[0].ToolCallID)
	}

	pendingByID := make(map[string]InteractionToolCall, len(pending))
	for _, call := range pending {
		pendingByID[call.ID] = call
	}

	seen := make(map[string]struct{}, len(req.ToolResults))
	for _, result := range req.ToolResults {
		if result.ToolCallID == "" {
			return fmt.Errorf("tool result is missing toolCallId")
		}
		if _, ok := seen[result.ToolCallID]; ok {
			return fmt.Errorf("duplicate tool result for tool call %q", result.ToolCallID)
		}
		seen[result.ToolCallID] = struct{}{}
		if _, ok := pendingByID[result.ToolCallID]; !ok {
			return fmt.Errorf("unknown tool result %q", result.ToolCallID)
		}
	}

	for _, call := range pending {
		if _, ok := seen[call.ID]; !ok {
			return fmt.Errorf("missing tool result for tool call %q", call.ID)
		}
	}
	return nil
}

func latestInteractionToolCalls(messages []InteractionMessage) []InteractionToolCall {
	for i := len(messages) - 1; i >= 0; i-- {
		if len(messages[i].ToolCalls) > 0 {
			return messages[i].ToolCalls
		}
	}
	return nil
}

func interactionUsageFromModel(usage models.TokenUsage) (InteractionUsage, bool) {
	if usage.PromptTokens == 0 && usage.CompletionTokens == 0 && usage.TotalTokens == 0 && usage.ReasoningTokens == 0 {
		return InteractionUsage{}, false
	}
	return InteractionUsage{
		InputTokens:  int64(usage.PromptTokens),
		OutputTokens: int64(usage.CompletionTokens),
		TotalTokens:  int64(usage.TotalTokens),
	}, true
}

func modelRoleFromInteraction(role InteractionRole) models.Role {
	switch role {
	case InteractionRoleAssistant:
		return models.RoleAssistant
	case InteractionRoleTool:
		return models.RoleTool
	case InteractionRoleSystem:
		return models.RoleSystem
	default:
		return models.RoleUser
	}
}

func interactionRoleFromModel(role models.Role) InteractionRole {
	switch role {
	case models.RoleAssistant:
		return InteractionRoleAssistant
	case models.RoleTool:
		return InteractionRoleTool
	case models.RoleSystem:
		return InteractionRoleSystem
	default:
		return InteractionRoleUser
	}
}

func newInteractionID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return "interaction-" + hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("interaction-%d", time.Now().UTC().UnixNano())
}
