package livesession

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

// MaxAppendTokens is GPT-Live's limit on one context append.
const MaxAppendTokens = 500

// maxAppendBytes bounds one append's content in UTF-8 bytes. A byte-level
// BPE tokenizer never emits more tokens than input bytes, so a chunk of at
// most MaxAppendTokens bytes is within the token limit whatever it holds
// (digits, ids, base64, emoji or rare CJK, which an estimate per character
// undercounts by up to twice the limit).
const maxAppendBytes = MaxAppendTokens

// ContextAppender is the Dialect extension that carries CONTEXT.APPEND. A
// dialect without it refuses CONTEXT.APPEND with ErrNoWireEvent.
type ContextAppender interface {
	// ContextAppend builds the wire event of one chunk of value's content.
	// seq numbers the session's append events from 1, for event ids. ok is
	// false for a kind the dialect has no event for.
	ContextAppend(value *messages.ContextAppendValue, chunk string, seq int64) (event models.SessionEvent, ok bool, err error)
}

// contextAppend sends one CONTEXT.APPEND as the dialect's append events.
// Content over maxAppendBytes is split at sentence or word boundaries into
// chunks that each fit, sent in order under the same delegation id (design
// open question Q6: the backend is told to summarize, and this split is the
// safety net). Success means the events were queued; the provider
// acknowledges or rejects them later.
//
// appendMu serializes appends, so the chunks of two concurrent appends never
// interleave on the wire. A multi-chunk append is not atomic: if a chunk is
// not admitted, the earlier chunks are already queued and the outcome
// reports the failure, so a sender that retries the whole append repeats
// them.
func (s *Session) contextAppend(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	appender, ok := s.dialect.(ContextAppender)
	value, isAppend := msg.Value.(*messages.ContextAppendValue)
	if !ok || !isAppend || value == nil || strings.TrimSpace(value.Content) == "" {
		return s.noWireEvent(msg)
	}
	s.appendMu.Lock()
	defer s.appendMu.Unlock()
	chunks := splitAppendContent(value.Content, maxAppendBytes)
	events := make([]models.SessionEvent, 0, len(chunks))
	for _, chunk := range chunks {
		event, known, err := appender.ContextAppend(value, chunk, s.appends.Add(1))
		if err != nil {
			return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure, Err: err}
		}
		if !known {
			return s.noWireEvent(msg)
		}
		events = append(events, event)
	}
	return s.base.EnqueueEvents(ctx, events)
}

// splitAppendContent splits content into chunks of at most limit bytes, each
// cut at a rune boundary (an invalid byte counts as a one-byte rune). A chunk
// ends at the last sentence end that fits, else at the last space, else at
// the last rune that fits. Joined back together the chunks are content with
// the whitespace at each cut trimmed.
func splitAppendContent(content string, limit int) []string {
	var chunks []string
	rest := strings.TrimSpace(content)
	for len(rest) > limit {
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
// longest prefix of whole runes within limit bytes, shortened to its last
// sentence end or else its last space when it has one. It is at least one
// rune, so the split always progresses.
func cutPoint(text string, limit int) int {
	fit, sentence, space := 0, -1, -1
	var previous rune
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if i+size > limit {
			break
		}
		if unicode.IsSpace(r) {
			space = i
			if strings.ContainsRune(".!?", previous) || r == '\n' {
				sentence = i
			}
		}
		previous = r
		i += size
		fit = i
	}
	switch {
	case sentence > 0:
		return sentence
	case space > 0:
		return space
	case fit > 0:
		return fit
	default:
		// limit admits not even the first rune; take it whole.
		_, size := utf8.DecodeRuneInString(text)
		return size
	}
}
