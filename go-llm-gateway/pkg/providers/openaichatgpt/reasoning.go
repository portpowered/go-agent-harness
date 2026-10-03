package openaichatgpt

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

// maxReplayedTurns bounds how many model turns keep their reasoning for
// replay; the oldest are dropped first.
const maxReplayedTurns = 256

// reasoningReplay keeps the reasoning items (with encrypted_content) of each
// completed model turn, in their original order relative to the turn's text
// and function calls, so the next request can send them back where they
// were. With store:false this is how reasoning carries across tool steps and
// user turns: Codex replays every ResponseItem::Reasoning it received
// (codex-rs/core/src/client.rs build_responses_request) and OpenClaw replays
// an assistant turn's thinking, text and toolCall blocks in their stream
// order (openai-responses-replay-messages-internal.ts, the assistant branch
// of convertResponsesMessagesWithStyle).
//
// The gateway's message history has no field for opaque provider items, so
// the provider keeps them for the life of one Provider (one conversation),
// keyed by the assistant message they belong to. An opaque field on gateway
// messages would have to travel through the loop's stream protocol, its
// message reconstruction and every session store; this cache needs none of
// that, and a turn it cannot match is replayed without reasoning, which the
// backend accepts.
type reasoningReplay struct {
	mu    sync.Mutex
	turns map[string]turnLayout
	order []string
}

func newReasoningReplay() *reasoningReplay {
	return &reasoningReplay{turns: map[string]turnLayout{}}
}

// replayBlockKind is the kind of one output item of a model turn.
type replayBlockKind int

const (
	replayReasoning replayBlockKind = iota + 1
	replayText
	replayCall
)

// replayBlock is one output item of a model turn: a replayable reasoning
// item, a message's text, or a function call's call_id.
type replayBlock struct {
	kind      replayBlockKind
	reasoning json.RawMessage
	text      string
	callID    string
}

// turnLayout is the ordered output of one model turn.
type turnLayout []replayBlock

func (l turnLayout) text() string {
	var b strings.Builder
	for _, block := range l {
		if block.kind == replayText {
			b.WriteString(block.text)
		}
	}
	return b.String()
}

func (l turnLayout) callIDs() []string {
	var ids []string
	for _, block := range l {
		if block.kind == replayCall {
			ids = append(ids, block.callID)
		}
	}
	return ids
}

func (l turnLayout) hasReasoning() bool {
	return slices.ContainsFunc(l, func(block replayBlock) bool { return block.kind == replayReasoning })
}

// matches reports whether msg is the assistant message this turn produced:
// the same text and the same function calls in the same order.
func (l turnLayout) matches(msg models.Message) bool {
	ids := make([]string, 0, len(msg.ToolCalls))
	for _, call := range msg.ToolCalls {
		ids = append(ids, call.ID)
	}
	return l.text() == msg.TextContent() && slices.Equal(l.callIDs(), ids)
}

// reasoningBefore returns the reasoning items that came after the previous
// function call and before callID.
func (l turnLayout) reasoningBefore(callID string) []json.RawMessage {
	var pending []json.RawMessage
	for _, block := range l {
		switch block.kind {
		case replayReasoning:
			pending = append(pending, block.reasoning)
		case replayCall:
			if block.callID == callID {
				return pending
			}
			pending = nil
		case replayText:
		}
	}
	return nil
}

// turnKey names the assistant message a turn produced. A message with
// function calls is named by its first call_id, which the backend makes
// unique. A text-only message is named by its text and the conversation
// before it (prefix), so two turns that said the same words stay apart.
func turnKey(prefix, text string, callIDs []string) string {
	if len(callIDs) > 0 {
		return "call:" + callIDs[0]
	}
	sum := sha256.Sum256([]byte(prefix + "\x00" + text))
	return "text:" + hex.EncodeToString(sum[:])
}

// remember keeps layout, the output of the turn that answered a request
// whose conversation fingerprint is prefix. A turn without reasoning is not
// kept.
func (r *reasoningReplay) remember(prefix string, layout turnLayout) {
	if r == nil || !layout.hasReasoning() {
		return
	}
	key := turnKey(prefix, layout.text(), layout.callIDs())
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, seen := r.turns[key]; !seen {
		r.order = append(r.order, key)
	}
	r.turns[key] = slices.Clone(layout)
	for len(r.order) > maxReplayedTurns {
		delete(r.turns, r.order[0])
		r.order = r.order[1:]
	}
}

// lookup returns the kept layout of the turn that produced msg after the
// conversation with fingerprint prefix, or nil.
func (r *reasoningReplay) lookup(prefix string, msg models.Message) turnLayout {
	if r == nil {
		return nil
	}
	ids := make([]string, 0, len(msg.ToolCalls))
	for _, call := range msg.ToolCalls {
		ids = append(ids, call.ID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.turns[turnKey(prefix, msg.TextContent(), ids)]
}

// conversationFingerprint chains the non-system messages of a conversation
// into one hash, so the text-only turn that answered a conversation can be
// found again when that conversation comes back as the prefix of a later
// request. System messages are left out because instructions may change
// between turns without changing what the model answered.
type conversationFingerprint struct {
	sum string
}

// fingerprintedMessage is the part of a message the fingerprint covers.
type fingerprintedMessage struct {
	Prefix     string   `json:"p"`
	Role       string   `json:"r"`
	Text       string   `json:"t"`
	Calls      []string `json:"c,omitempty"`
	ToolCallID string   `json:"i,omitempty"`
}

func (f *conversationFingerprint) add(msg models.Message) {
	calls := make([]string, 0, len(msg.ToolCalls))
	for _, call := range msg.ToolCalls {
		calls = append(calls, call.ID)
	}
	encoded, err := json.Marshal(fingerprintedMessage{Prefix: f.sum, Role: string(msg.Role), Text: msg.TextContent(), Calls: calls, ToolCallID: msg.ToolCallID})
	if err != nil {
		return
	}
	sum := sha256.Sum256(encoded)
	f.sum = hex.EncodeToString(sum[:])
}

// replayedReasoning is the reasoning input item sent back: summary and
// encrypted_content, without id, status or plain content. OpenClaw's ChatGPT
// path replays reasoning without its item id (replayResponsesItemIds:false,
// openai-responses-replay-messages-internal.ts:460-462): this provider sends
// function_call items without ids, and the Responses API rejects a
// reasoning id whose required following item is missing.
type replayedReasoning struct {
	Type             string          `json:"type"`
	Summary          json.RawMessage `json:"summary"`
	EncryptedContent string          `json:"encrypted_content"`
}

// replayableReasoning returns the input item for a completed reasoning
// output item, or nil when it carries no encrypted_content (with store:false
// a reasoning item without its ciphertext cannot be resolved, so OpenClaw
// drops those too).
func replayableReasoning(item *outputItem) json.RawMessage {
	if item.EncryptedContent == "" {
		return nil
	}
	summary := item.Summary
	if len(summary) == 0 || string(summary) == "null" {
		summary = json.RawMessage(`[]`)
	}
	encoded, err := json.Marshal(replayedReasoning{Type: itemTypeReasoning, Summary: summary, EncryptedContent: item.EncryptedContent})
	if err != nil {
		return nil
	}
	return encoded
}

// replayTarget is where a response's output is kept for replay: store (nil
// keeps nothing) under the fingerprint of the conversation it answered.
type replayTarget struct {
	store  *reasoningReplay
	prefix string
}
