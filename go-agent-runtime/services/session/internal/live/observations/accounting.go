package observations

import (
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	sessiontrace "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
)

type streamAccounting struct {
	mu           sync.Mutex
	sink         *metrics.InMemorySink
	usage        messages.TokenUsage
	outputInTurn bool
	toolDeltas   map[string]struct{}
	err          error
}

func newStreamAccounting() *streamAccounting {
	sink, err := metrics.NewInMemorySink()
	if err != nil {
		panic(err)
	}
	return &streamAccounting{sink: sink, toolDeltas: make(map[string]struct{})}
}

func (a *streamAccounting) observeMessage(msg messages.StreamMessage) {
	if a == nil || a.sink == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if msg.Type == messages.StreamTypeMessageStart {
		a.outputInTurn = false
	}
	a.observeContent(msg.Role, msg.Value)
	a.observeTool(msg)
	if value, ok := msg.Value.(*messages.MessageEndValue); ok && value != nil {
		a.accountUsage(value.Usage)
	}
}

func (a *streamAccounting) observeContent(role messages.Role, raw any) {
	switch value := raw.(type) {
	case *messages.AudioDeltaValue:
		if value != nil {
			a.accountBytes(role, metrics.ModalityAudio, len(value.Content))
		}
	case *messages.TextDeltaValue:
		if value != nil {
			a.accountBytes(role, metrics.ModalityText, len(value.Content))
		}
	case *messages.TranscriptDeltaValue:
		if value != nil {
			a.accountBytes(role, metrics.ModalityText, len(value.Text))
		}
	case *messages.ImageDeltaValue:
		if value != nil {
			a.accountBytes(role, metrics.ModalityImage, len(value.Content))
		}
	}
}

func (a *streamAccounting) observeTool(msg messages.StreamMessage) {
	switch value := msg.Value.(type) {
	case *messages.ToolCallDeltaValue:
		if value != nil {
			a.accountToolDelta(msg.ToolCallId, len(value.PartialJSON))
		}
	case *messages.ToolCallEndValue:
		a.accountToolEnd(msg.ToolCallId, value)
	}
}

func (a *streamAccounting) accountBytes(role messages.Role, modality metrics.Modality, byteCount int) {
	direction := metrics.DirectionOutput
	if role == messages.RoleUser {
		direction = metrics.DirectionInput
	}
	a.record(direction, modality, byteCount)
	if direction == metrics.DirectionOutput && byteCount > 0 {
		a.outputInTurn = true
	}
}

func (a *streamAccounting) accountToolDelta(id string, byteCount int) {
	a.record(metrics.DirectionOutput, metrics.ModalityTool, byteCount)
	if byteCount > 0 {
		a.outputInTurn = true
		a.toolDeltas[id] = struct{}{}
	}
}

func (a *streamAccounting) accountToolEnd(id string, value *messages.ToolCallEndValue) {
	if value == nil {
		return
	}
	if value.ToolCallID != "" {
		id = value.ToolCallID
	}
	if _, streamed := a.toolDeltas[id]; !streamed {
		a.record(metrics.DirectionOutput, metrics.ModalityTool, len(value.Arguments))
	}
	delete(a.toolDeltas, id)
}

func (a *streamAccounting) accountUsage(usage messages.TokenUsage) {
	if a.outputInTurn && usage.PromptTokens >= 0 && usage.CompletionTokens >= 0 && usage.TotalTokens >= 0 && usage.ReasoningTokens >= 0 {
		a.usage.PromptTokens += usage.PromptTokens
		a.usage.CompletionTokens += usage.CompletionTokens
		a.usage.TotalTokens += usage.TotalTokens
		a.usage.ReasoningTokens += usage.ReasoningTokens
	}
	a.outputInTurn = false
}

func (a *streamAccounting) inputAudio(byteCount int) {
	if a == nil || a.sink == nil {
		return
	}
	a.mu.Lock()
	a.record(metrics.DirectionInput, metrics.ModalityAudio, byteCount)
	a.mu.Unlock()
}

func (a *streamAccounting) record(direction metrics.Direction, modality metrics.Modality, byteCount int) {
	if byteCount <= 0 {
		return
	}
	if err := a.sink.Record(direction, modality, int64(byteCount)); err != nil && a.err == nil {
		a.err = fmt.Errorf("record %s/%s live metrics: %w", direction, modality, err)
	}
}

func (a *streamAccounting) errorValue() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.err
}

func (a *streamAccounting) snapshot() *sessiontrace.SessionFinalAccounting {
	if a == nil || a.sink == nil {
		return nil
	}
	a.mu.Lock()
	usage := a.usage
	a.mu.Unlock()
	return &sessiontrace.SessionFinalAccounting{
		PromptTokens: uint64(usage.PromptTokens), CompletionTokens: uint64(usage.CompletionTokens),
		TotalTokens: uint64(usage.TotalTokens), ReasoningTokens: uint64(usage.ReasoningTokens),
		UsageSemantics: sessiontrace.SessionTokenUsageIncremental, Metrics: a.sink.Snapshot(),
	}
}
