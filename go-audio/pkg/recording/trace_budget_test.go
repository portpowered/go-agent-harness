package recording

import (
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"testing"
)

func TestRuntimeEvidenceByteBudgetPrecedesPacketLimit(t *testing.T) {
	trace := &Trace{clock: clock.Real{}, events: make(chan sessionAudioTraceBlock, 100)}
	payload := make([]byte, 512<<10)
	for i := 0; i < 64; i++ {
		trace.ObserveRuntime(RuntimeEvent{Kind: "tool_result", Payload: payload})
	}
	if trace.queuedBytes.Load() > MaxQueuedBytes || len(trace.events) >= 100 || trace.droppedRuntime.Load() == 0 {
		t.Fatalf("budget not enforced: bytes=%d packets=%d dropped=%d", trace.queuedBytes.Load(), len(trace.events), trace.droppedRuntime.Load())
	}
	for len(trace.events) > 0 {
		block := <-trace.events
		trace.queuedBytes.Add(-block.cost)
	}
	if trace.queuedBytes.Load() != 0 {
		t.Fatal("retained accounting did not drain")
	}
	trace.ObserveRuntime(RuntimeEvent{Kind: "tool_result", Payload: payload})
	if len(trace.events) != 1 {
		t.Fatal("capacity not reusable after drain")
	}
}
