package openailive

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

// MaxAppendTokens is GPT-Live's limit on one context append.
const MaxAppendTokens = 500

// asciiCharsPerToken is the conservative estimate of ASCII characters per
// token used to keep an append under MaxAppendTokens. English averages about
// four; three leaves headroom. Every other rune counts as a whole token.
const asciiCharsPerToken = 3

// appendEventIDPrefix numbers the event ids of context appends, so an error
// or acknowledgement can be matched to the append it answers.
const appendEventIDPrefix = "evt_ctx_"

// contextAppend sends one CONTEXT.APPEND as GPT-Live append commands, with
// delegation_id the value's id or null. Content over MaxAppendTokens is split
// at sentence or word boundaries into appends that each fit, sent in order
// under the same delegation id (design open question Q6: the backend is told
// to summarize, and this split is the safety net). Success means the appends
// were queued; GPT-Live acknowledges them later, and a rejection arrives as
// an ERROR carrying the append's event id.
func (s *liveSession) contextAppend(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	value, ok := msg.Value.(*messages.ContextAppendValue)
	if !ok || value == nil || strings.TrimSpace(value.Content) == "" {
		return s.noWireEvent(msg)
	}
	eventType, ok := appendEventType(value.Kind)
	if !ok {
		return s.noWireEvent(msg)
	}
	chunks := splitAppendContent(value.Content, MaxAppendTokens)
	events := make([]models.SessionEvent, 0, len(chunks))
	for _, chunk := range chunks {
		body := ContextAppend{
			EventID:      fmt.Sprintf("%s%d", appendEventIDPrefix, s.appends.Add(1)),
			DelegationID: value.DelegationID,
			Content:      chunk,
		}
		data, err := json.Marshal(body)
		if err != nil {
			return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure, Err: err}
		}
		events = append(events, models.SessionEvent{Type: models.SessionEventType(eventType), Data: data})
	}
	return s.base.EnqueueEvents(ctx, events)
}

// appendEventType maps a CONTEXT.APPEND kind to its append command.
func appendEventType(kind messages.ContextAppendKind) (string, bool) {
	switch kind {
	case messages.ContextAppendInstructions:
		return TypeInstructionsAppend, true
	case messages.ContextAppendThinking:
		return TypeThinkingAppend, true
	case messages.ContextAppendCommentary:
		return TypeCommentaryAppend, true
	default:
		return "", false
	}
}

// estimateTokens is a conservative token count of text: ASCII characters at
// asciiCharsPerToken, every other rune as one token.
func estimateTokens(text string) int {
	ascii, other := 0, 0
	for _, r := range text {
		if r < utf8.RuneSelf {
			ascii++
		} else {
			other++
		}
	}
	return (ascii+asciiCharsPerToken-1)/asciiCharsPerToken + other
}

// splitAppendContent splits content into chunks of at most limit estimated
// tokens. A chunk ends at the last sentence end that fits, else at the last
// space, else mid-word. Joined back together the chunks are content with the
// whitespace at each cut trimmed.
func splitAppendContent(content string, limit int) []string {
	var chunks []string
	rest := strings.TrimSpace(content)
	for estimateTokens(rest) > limit {
		cut := cutPoint(rest, limit)
		chunks = append(chunks, strings.TrimSpace(rest[:cut]))
		rest = strings.TrimSpace(rest[cut:])
	}
	if rest != "" {
		chunks = append(chunks, rest)
	}
	return chunks
}

// cutPoint is the byte offset at which the first chunk of text ends: the
// longest prefix within limit tokens, shortened to its last sentence end or
// else its last space when it has one.
func cutPoint(text string, limit int) int {
	ascii, other, fit := 0, 0, 0
	sentence, space := -1, -1
	var previous rune
	for i, r := range text {
		if r < utf8.RuneSelf {
			ascii++
		} else {
			other++
		}
		if (ascii+asciiCharsPerToken-1)/asciiCharsPerToken+other > limit {
			break
		}
		fit = i + utf8.RuneLen(r)
		if unicode.IsSpace(r) {
			space = i
			if strings.ContainsRune(".!?", previous) || r == '\n' {
				sentence = i
			}
		}
		previous = r
	}
	switch {
	case sentence > 0:
		return sentence
	case space > 0:
		return space
	case fit > 0:
		return fit
	default:
		// limit admits not even one rune; take one so the split progresses.
		_, size := utf8.DecodeRuneInString(text)
		return size
	}
}
