package agentruntime

import (
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/conversationlog"
	conversationlogwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/conversationlog/wire"
)

// sessionToolLifecycleObserver is the recording host's narrow execution-boundary
// hook. The runtime reducer owns the durable policy behind this adapter.
type sessionToolLifecycleObserver interface {
	observeToolCall(messages.ToolCall)
	observeToolResult(messages.ToolCall, messages.ToolCallResponse, bool)
}

// Deprecated: use conversationlog.TurnInput.
type sessionConversationTurnInput = conversationlog.TurnInput

// Deprecated: use conversationlog.TurnTiming.
type sessionConversationTurnTiming = conversationlog.TurnTiming

// Deprecated: use conversationlog.TurnResponse.
type sessionConversationTurnResponse = conversationlog.TurnResponse

// Deprecated: use conversationlog.LogEntry.
type sessionConversationLogEntry = conversationlog.LogEntry

// Deprecated: use conversationlog.ToolEvent.
type sessionConversationToolEvent = conversationlog.ToolEvent

// Deprecated: use conversationlog.ImageEvidence.
type sessionConversationImageEvidence = conversationlog.ImageEvidence

type sessionConversationClock struct {
	now func() time.Time
}

func (c sessionConversationClock) Now() time.Time {
	if c.now == nil {
		return time.Time{}
	}
	return c.now()
}

// Deprecated: use conversationlog.Service.
//
// sessionConversationCollector is a source-compatible, decision-free bridge
// for the recording package. It lazily constructs the dedicated runtime Wire
// graph so the existing recording constructor and callers remain unchanged.
type sessionConversationCollector struct {
	now     func() time.Time
	mu      sync.Mutex
	reducer conversationlog.Service
}

func (c *sessionConversationCollector) service() conversationlog.Service {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reducer == nil {
		if c.now == nil {
			c.reducer = conversationlogwire.NewService(nil)
		} else {
			c.reducer = conversationlogwire.NewService(sessionConversationClock{now: c.now})
		}
	}
	return c.reducer
}

func (c *sessionConversationCollector) observe(msg messages.StreamMessage, outbound bool, inputIndex, outputIndex int) {
	if reducer := c.service(); reducer != nil {
		reducer.Observe(msg, outbound, inputIndex, outputIndex)
	}
}

func (c *sessionConversationCollector) observeToolCall(call messages.ToolCall) {
	if reducer := c.service(); reducer != nil {
		reducer.ObserveToolCall(call)
	}
}

func (c *sessionConversationCollector) observeToolResult(call messages.ToolCall, response messages.ToolCallResponse, failed bool, image ...*sessionConversationImageEvidence) {
	if reducer := c.service(); reducer != nil {
		if len(image) == 0 {
			reducer.ObserveToolResult(call, response, failed)
			return
		}
		reducer.ObserveToolResult(call, response, failed, image[0])
	}
}

func (c *sessionConversationCollector) entries() []sessionConversationLogEntry {
	if reducer := c.service(); reducer != nil {
		return reducer.Entries()
	}
	return nil
}

func (c *sessionConversationCollector) timingEntries() []sessionConversationTurnTiming {
	if reducer := c.service(); reducer != nil {
		return reducer.TimingEntries()
	}
	return nil
}

// sessionConversationLogJSON renders the detached runtime snapshot. A nil
// collector remains an empty recording rather than panicking at the CLI edge.
func sessionConversationLogJSON(collector *sessionConversationCollector) ([]byte, error) {
	if collector == nil {
		return nil, nil
	}
	if reducer := collector.service(); reducer != nil {
		return reducer.JSONL()
	}
	return nil, nil
}
