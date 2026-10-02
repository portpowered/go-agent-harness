package openaichatgpt

import (
	"encoding/json"
	"sync"
)

// maxReplayedCalls bounds how many function calls keep their reasoning for
// replay; the oldest are dropped first.
const maxReplayedCalls = 256

// reasoningReplay keeps the reasoning items (with encrypted_content) that
// preceded each function call, keyed by call_id, so the next request can
// send them back before that call. With store:false this is how reasoning
// carries across tool steps: Codex replays every ResponseItem::Reasoning it
// received (codex-rs/core/src/client.rs build_responses_request) and
// OpenClaw replays the reasoning item it kept as the thinking signature
// (openai-responses-stream-internal.ts, output_item.done for reasoning).
// The gateway's message history has no field for opaque provider items, so
// the provider keeps them for the life of one Provider (one conversation).
type reasoningReplay struct {
	mu    sync.Mutex
	items map[string][]json.RawMessage
	order []string
}

func newReasoningReplay() *reasoningReplay {
	return &reasoningReplay{items: map[string][]json.RawMessage{}}
}

func (r *reasoningReplay) remember(callID string, items []json.RawMessage) {
	if r == nil || callID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, seen := r.items[callID]; !seen {
		r.order = append(r.order, callID)
	}
	r.items[callID] = append([]json.RawMessage(nil), items...)
	for len(r.order) > maxReplayedCalls {
		delete(r.items, r.order[0])
		r.order = r.order[1:]
	}
}

// before returns the reasoning items to send before the function call
// callID, or nil.
func (r *reasoningReplay) before(callID string) []json.RawMessage {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.items[callID]
}

// replayedReasoning is the reasoning input item sent back: the fields Codex
// keeps (id, summary, encrypted_content), without status or plain content.
type replayedReasoning struct {
	Type             string          `json:"type"`
	ID               string          `json:"id,omitempty"`
	Summary          json.RawMessage `json:"summary"`
	EncryptedContent string          `json:"encrypted_content"`
}

// replayableReasoning returns the input item for a completed reasoning
// output item, or nil when it carries no encrypted_content (with store:false
// a bare reasoning id cannot be resolved, so OpenClaw drops those too).
func replayableReasoning(item *outputItem) json.RawMessage {
	if item.EncryptedContent == "" {
		return nil
	}
	summary := item.Summary
	if len(summary) == 0 || string(summary) == "null" {
		summary = json.RawMessage(`[]`)
	}
	encoded, err := json.Marshal(replayedReasoning{Type: itemTypeReasoning, ID: item.ID, Summary: summary, EncryptedContent: item.EncryptedContent})
	if err != nil {
		return nil
	}
	return encoded
}
