package engine

import (
	"context"
	"fmt"
	"testing"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-loop/pkg/participants"
	"github.com/portpowered/go-agent-loop/pkg/subsystems"
)

// benchInferencer returns a canned streaming response for each InferStream call.
type benchInferencer struct {
	deltas []messages.StreamMessage
}

func (bi *benchInferencer) Infer(ctx context.Context, req messages.InferenceRequest) (messages.InferenceResult, error) {
	return messages.InferenceResult{}, nil
}

func (bi *benchInferencer) InferStream(ctx context.Context, req messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	ch := make(chan messages.StreamMessage, len(bi.deltas))
	for _, d := range bi.deltas {
		ch <- d
	}
	close(ch)
	return ch, nil
}

func newBenchDeltas(textChunks int) []messages.StreamMessage {
	deltas := make([]messages.StreamMessage, 0, textChunks+4)
	deltas = append(deltas, messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()})
	deltas = append(deltas, messages.StreamMessage{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()})
	for i := 0; i < textChunks; i++ {
		deltas = append(deltas, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("chunk ")})
	}
	deltas = append(deltas, messages.StreamMessage{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, Value: messages.NewTextEndValue()})
	deltas = append(deltas, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
	return deltas
}

// newBenchEngineWithCap creates a minimal engine with the given buffer capacity.
func newBenchEngineWithCap(bufCap int) (*Engine, *participants.ModelRunner, *participants.ToolRunner, *participants.UserRunner, *participants.KernelRunner) {
	modelRunner := participants.NewModelRunner(&noopInferencer{}, bufCap)
	toolRunner := participants.NewToolRunner(&messages.DefaultToolExecutor{}, bufCap)
	userRunner := participants.NewUserRunner(bufCap)
	kernelRunner := participants.NewKernelRunner(nil, bufCap)

	coord := subsystems.NewCoordinator(nil)
	coordDelta := subsystems.NewCoordinatorDelta(*kernelRunner.DeltaInbox, nil)
	hlps := []subsystems.Subsystem{coord, coordDelta}

	eng := NewEngine(ModeAskOnce, nil, hlps, modelRunner, toolRunner, userRunner, kernelRunner, nil)
	return eng, modelRunner, toolRunner, userRunner, kernelRunner
}

// --- ReadTick throughput benchmarks ---

// BenchmarkReadTick measures the throughput of consuming one delta per tick from
// the model runner's DeltaOutbox at various conversation history sizes.
func BenchmarkReadTick(b *testing.B) {
	for _, preload := range []int{0, 10, 100} {
		b.Run(fmt.Sprintf("history=%d", preload), func(b *testing.B) {
			// Buffer capacity must be >= b.N so all pre-filled writes succeed.
			bufCap := b.N + 1
			eng, modelRunner, _, userRunner, _ := newBenchEngineWithCap(bufCap)
			ordering := NewGlobalOrdering(modelRunner, nil, userRunner, nil)
			ctx := context.Background()
			ls := eng.State().LoopState

			for i := 0; i < preload; i++ {
				ls.History.ConversationBuffer = append(
					ls.History.ConversationBuffer,
					messages.NewTextMessage(messages.RoleUser, fmt.Sprintf("message %d", i)),
				)
			}

			for i := 0; i < b.N; i++ {
				modelRunner.DeltaOutbox.Write(ctx, messages.StreamMessage{
					Type:  messages.StreamTypeTextDelta,
					Role:  messages.RoleAssistant,
					Value: messages.NewTextDeltaValue("x"),
				})
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = ordering.ReadTick(ctx, ls)
			}
		})
	}
}

// --- UpdateWorldHistory benchmarks ---

// BenchmarkUpdateWorldHistory measures the cost of moving tick inputs into history
// at various conversation lengths.
func BenchmarkUpdateWorldHistory(b *testing.B) {
	for _, histLen := range []int{0, 10, 50, 200} {
		b.Run(fmt.Sprintf("history=%d", histLen), func(b *testing.B) {
			eng, modelRunner, _, userRunner, _ := newBenchEngineWithCap(64)
			ordering := NewGlobalOrdering(modelRunner, nil, userRunner, nil)
			ls := eng.State().LoopState

			for i := 0; i < histLen; i++ {
				ls.History.ConversationBuffer = append(
					ls.History.ConversationBuffer,
					messages.NewTextMessage(messages.RoleUser, "msg"),
				)
			}

			delta := messages.StreamMessage{
				Type:  messages.StreamTypeTextDelta,
				Role:  messages.RoleAssistant,
				Value: messages.NewTextDeltaValue("x"),
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ls.Inputs.ModelInputDelta = append(ls.Inputs.ModelInputDelta[:0], delta)
				ordering.UpdateWorldHistory(ls)
				ls.History.CurrentModelDeltaCount = 0
				ls.History.ModelDeltaStartIndex = len(ls.History.ConversationDeltaBuffer)
				ls.Inputs.ModelInputDelta = ls.Inputs.ModelInputDelta[:0]
			}
		})
	}
}

// --- Full tick cycle benchmarks ---

// BenchmarkFullTickCycle measures one complete engine tick: ReadTick +
// UpdateWorldHistory + executeWorldState + FlushInputs.
func BenchmarkFullTickCycle(b *testing.B) {
	bufCap := b.N + 1
	eng, modelRunner, _, _, _ := newBenchEngineWithCap(bufCap)
	ctx := context.Background()

	for i := 0; i < b.N; i++ {
		modelRunner.DeltaOutbox.Write(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeTextDelta,
			Role:  messages.RoleAssistant,
			Value: messages.NewTextDeltaValue("x"),
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = eng.Tick(ctx)
	}
}

// --- End-to-end Execute benchmarks ---

// BenchmarkExecute measures a complete Execute cycle (user message → hot loop →
// model response collected via kernel events) with varying response sizes.
func BenchmarkExecute(b *testing.B) {
	for _, chunks := range []int{1, 10, 100} {
		b.Run(fmt.Sprintf("chunks=%d", chunks), func(b *testing.B) {
			deltas := newBenchDeltas(chunks)
			inf := &benchInferencer{deltas: deltas}

			bufCap := 4096
			modelRunner := participants.NewModelRunner(inf, bufCap)
			toolRunner := participants.NewToolRunner(&messages.DefaultToolExecutor{}, bufCap)
			userRunner := participants.NewUserRunner(bufCap)
			kernelRunner := participants.NewKernelRunner(nil, bufCap)

			coord := subsystems.NewCoordinator(nil)
			coordDelta := subsystems.NewCoordinatorDelta(*kernelRunner.DeltaInbox, nil)
			hlps := []subsystems.Subsystem{coord, coordDelta}

			eng := NewEngine(ModeAskOnce, nil, hlps, modelRunner, toolRunner, userRunner, kernelRunner, nil)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()

				ls := eng.State().LoopState
				ls.History.ConversationBuffer = ls.History.ConversationBuffer[:0]
				ls.History.ConversationDeltaBuffer = ls.History.ConversationDeltaBuffer[:0]
				ls.History.ModelDeltaStartIndex = 0
				ls.History.CurrentModelDeltaCount = 0
				ls.History.CurrentToolDeltaCount = 0
				ls.History.ToolDeltaStartIndex = 0
				ls.History.CurrentPassID = 0
				ls.History.NextGlobalIndex = 0
				eng.tickCount = 0

				eng.AddMessages([]messages.Message{
					messages.NewTextMessage(messages.RoleUser, "hello"),
				})

				ctx, cancel := context.WithCancel(context.Background())
				kernelEventCh := kernelRunner.NewDeltaEventReader(256)

				b.StartTimer()

				done := make(chan error, 1)
				go func() {
					done <- eng.RunHotLoop(ctx)
				}()

				for range kernelEventCh {
				}
				cancel()
				<-done
			}
		})
	}
}
