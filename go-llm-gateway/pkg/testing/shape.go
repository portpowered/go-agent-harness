package testing

import "encoding/json"

// chatCompletionShape represents the structural skeleton of an OpenAI-style chat
// completion request: message count, ordered roles, and ordered content block
// types per message. Used for replay matching without comparing full body bytes.
type chatCompletionShape struct {
	Messages []messageShape `json:"messages"`
}

type messageShape struct {
	Role          string   `json:"role"`
	ContentBlocks []string `json:"-"` // filled by extractContentBlocks
}

// extractChatCompletionShape parses body as JSON and extracts the chat completion
// shape if the structure looks like an OpenAI chat completion request. Returns
// nil if parsing fails or the body is not a chat completion request.
func extractChatCompletionShape(body []byte) *chatCompletionShape {
	var raw struct {
		Messages []struct {
			Role    json.RawMessage `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &raw); err != nil || len(raw.Messages) == 0 {
		return nil
	}
	shape := &chatCompletionShape{Messages: make([]messageShape, len(raw.Messages))}
	for i := range raw.Messages {
		shape.Messages[i].Role = extractRole(raw.Messages[i].Role)
		shape.Messages[i].ContentBlocks = extractContentBlocks(raw.Messages[i].Content)
	}
	return shape
}

func extractRole(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

func extractContentBlocks(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	// content can be a string (single text block)
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil
		}
		return []string{"text"}
	}
	// content can be an array of parts, each with "type"
	if raw[0] == '[' {
		var parts []struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &parts); err != nil {
			return nil
		}
		blockTypes := make([]string, 0, len(parts))
		for _, p := range parts {
			t := p.Type
			if t == "" {
				t = "unknown"
			}
			blockTypes = append(blockTypes, t)
		}
		return blockTypes
	}
	return nil
}

func chatCompletionShapesEqual(a, b *chatCompletionShape) bool {
	if a == nil || b == nil {
		return a == b
	}
	if len(a.Messages) != len(b.Messages) {
		return false
	}
	for i := range a.Messages {
		if a.Messages[i].Role != b.Messages[i].Role {
			return false
		}
		if !stringSlicesEqual(a.Messages[i].ContentBlocks, b.Messages[i].ContentBlocks) {
			return false
		}
	}
	return true
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
