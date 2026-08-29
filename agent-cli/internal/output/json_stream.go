package output

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// streamEventJSON is the NDJSON wire shape for a single delta event.
type streamEventJSON struct {
	Type       string          `json:"type"`
	Index      int             `json:"index,omitempty"`
	Role       string          `json:"role,omitempty"`
	ToolCallID string          `json:"toolCallId,omitempty"`
	ResponseID string          `json:"responseId,omitempty"`
	Value      json.RawMessage `json:"value,omitempty"`
}

// WriteEventStreamToWriter drains the event stream and writes TEXT.DELTA and
// REASONING.DELTA content directly to w (reasoning first, then text, matching
// the order events arrive). REFUSAL events are written to errW with a
// [REFUSAL] prefix. Used for human-readable streaming output.
func WriteEventStreamToWriter(w io.Writer, errW io.Writer, stream agentloop.Stream) error {
	for stream.HasNext() {
		evt := stream.Response()
		switch evt.Type {
		case messages.StreamTypeTextDelta:
			if v, ok := evt.Value.(*messages.TextDeltaValue); ok && v.Content != "" {
				if _, err := io.WriteString(w, v.Content); err != nil {
					return err
				}
			}
		case messages.StreamTypeReasoningDelta:
			if v, ok := evt.Value.(*messages.ReasoningDeltaValue); ok && v.Content != "" {
				if _, err := io.WriteString(w, v.Content); err != nil {
					return err
				}
			}
		case messages.StreamTypeRefusal:
			if v, ok := evt.Value.(*messages.RefusalValue); ok && v.Message != "" {
				WriteRefusal(errW, v.Message)
			}
		}
	}
	return nil
}

// WriteStreamEventJSON marshals a StreamMessage as a single NDJSON line and writes it to w.
// Each concrete StreamMessageValue type already carries JSON tags, so json.Marshal works directly.
func WriteStreamEventJSON(w io.Writer, msg messages.StreamMessage) error {
	evt := streamEventJSON{
		Type:       string(msg.Type),
		Index:      msg.ActorProvidedIndex,
		Role:       string(msg.Role),
		ToolCallID: msg.ToolCallId,
		ResponseID: msg.ResponseID,
	}
	if msg.Value != nil {
		valueBytes, err := json.Marshal(msg.Value)
		if err != nil {
			return fmt.Errorf("marshal stream event value: %w", err)
		}
		evt.Value = valueBytes
	}
	line, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal stream event: %w", err)
	}
	_, err = fmt.Fprintf(w, "%s\n", line)
	return err
}

// contentPartJSON is the wire shape for a content part in the non-streaming JSON response.
type contentPartJSON struct {
	Type      string     `json:"type"`
	Text      string     `json:"text,omitempty"`
	MediaType string     `json:"mediaType,omitempty"`
	URL       string     `json:"url,omitempty"`
	Reasoning string     `json:"reasoning,omitempty"`
	Usage     *usageJSON `json:"usage,omitempty"`
}

// usageJSON is the wire shape for token usage in a usage_info content part.
type usageJSON struct {
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
	ReasoningTokens  int `json:"reasoningTokens,omitempty"`
}

// toolCallJSON is the wire shape for a tool call.
type toolCallJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// messageJSON is the wire shape for a full message.
type messageJSON struct {
	Role         string            `json:"role"`
	ContentParts []contentPartJSON `json:"contentParts,omitempty"`
	ToolCalls    []toolCallJSON    `json:"toolCalls,omitempty"`
	ToolCallID   string            `json:"toolCallId,omitempty"`
}

// messagesOutputJSON is the wire shape for a non-streaming JSON response.
type messagesOutputJSON struct {
	Messages []messageJSON `json:"messages"`
}

// WriteMessagesJSON serializes the full messages slice as a single JSON object to w.
// Used for --output-json without --stream.
func WriteMessagesJSON(w io.Writer, msgs []messages.Message) error {
	out := messagesOutputJSON{
		Messages: make([]messageJSON, 0, len(msgs)),
	}
	for _, m := range msgs {
		mj := messageJSON{
			Role:       string(m.Role),
			ToolCallID: m.ToolCallID,
		}
		for _, tc := range m.ToolCalls {
			mj.ToolCalls = append(mj.ToolCalls, toolCallJSON{
				ID:        tc.ID,
				Name:      tc.Name,
				Arguments: tc.Arguments,
			})
		}
		for _, p := range m.ContentParts {
			switch cp := p.(type) {
			case messages.TextPart:
				mj.ContentParts = append(mj.ContentParts, contentPartJSON{Type: "text", Text: cp.Text})
			case messages.ImagePart:
				mj.ContentParts = append(mj.ContentParts, contentPartJSON{Type: "image", MediaType: cp.MediaType, URL: cp.URL})
			case messages.AudioPart:
				mj.ContentParts = append(mj.ContentParts, contentPartJSON{Type: "audio", MediaType: cp.MediaType, URL: cp.URL})
			case messages.VideoPart:
				mj.ContentParts = append(mj.ContentParts, contentPartJSON{Type: "video", MediaType: cp.MediaType, URL: cp.URL})
			case messages.FilePart:
				mj.ContentParts = append(mj.ContentParts, contentPartJSON{Type: "file", MediaType: cp.MediaType, URL: cp.URL})
			case messages.ReasoningPart:
				mj.ContentParts = append(mj.ContentParts, contentPartJSON{Type: "reasoning", Reasoning: cp.Reasoning})
			case messages.UsageInfoPart:
				mj.ContentParts = append(mj.ContentParts, contentPartJSON{
					Type: "usage_info",
					Usage: &usageJSON{
						PromptTokens:     cp.Usage.PromptTokens,
						CompletionTokens: cp.Usage.CompletionTokens,
						TotalTokens:      cp.Usage.TotalTokens,
						ReasoningTokens:  cp.Usage.ReasoningTokens,
					},
				})
			}
		}
		out.Messages = append(out.Messages, mj)
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal messages: %w", err)
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}
