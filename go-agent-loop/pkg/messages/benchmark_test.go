package messages

import (
	"context"
	"fmt"
	"testing"
)

// --- ReconstructModelMessageFromDeltas benchmarks ---

// generateTextDeltas creates a slice of model stream deltas with the given number
// of TEXT.DELTA events. Each delta carries a short text chunk to simulate realistic
// streaming output.
func generateTextDeltas(n int) []StreamMessage {
	deltas := make([]StreamMessage, 0, n+4)
	deltas = append(deltas, StreamMessage{Type: StreamTypeMessageStart, Role: RoleAssistant, Value: NewMessageStartValue()})
	deltas = append(deltas, StreamMessage{Type: StreamTypeTextStart, Role: RoleAssistant, Value: NewTextStartValue()})
	for i := 0; i < n; i++ {
		deltas = append(deltas, StreamMessage{Type: StreamTypeTextDelta, Role: RoleAssistant, Value: NewTextDeltaValue("chunk ")})
	}
	deltas = append(deltas, StreamMessage{Type: StreamTypeTextEnd, Role: RoleAssistant, Value: NewTextEndValue()})
	deltas = append(deltas, StreamMessage{Type: StreamTypeMessageEnd, Role: RoleAssistant, Value: NewMessageEndValue(TokenUsage{})})
	return deltas
}

func BenchmarkReconstructModelMessageFromDeltas_10(b *testing.B) {
	deltas := generateTextDeltas(10)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ReconstructModelMessageFromDeltas(deltas)
	}
}

func BenchmarkReconstructModelMessageFromDeltas_100(b *testing.B) {
	deltas := generateTextDeltas(100)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ReconstructModelMessageFromDeltas(deltas)
	}
}

func BenchmarkReconstructModelMessageFromDeltas_1000(b *testing.B) {
	deltas := generateTextDeltas(1000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ReconstructModelMessageFromDeltas(deltas)
	}
}

func BenchmarkReconstructModelMessageFromDeltas_10000(b *testing.B) {
	deltas := generateTextDeltas(10000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ReconstructModelMessageFromDeltas(deltas)
	}
}

// --- TypedBuffer read/write throughput benchmarks ---

func BenchmarkTypedBuffer_Write(b *testing.B) {
	buf := NewTypedBuffer[int](b.N + 1)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Write(ctx, i)
	}
}

func BenchmarkTypedBuffer_Read(b *testing.B) {
	buf := NewTypedBuffer[int](b.N + 1)
	ctx := context.Background()
	for i := 0; i < b.N; i++ {
		buf.Write(ctx, i)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Read()
	}
}

func BenchmarkTypedBuffer_WriteRead(b *testing.B) {
	buf := NewTypedBuffer[int](64)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Write(ctx, i)
		buf.Read()
	}
}

func BenchmarkTypedBuffer_StreamMessage(b *testing.B) {
	buf := NewTypedBuffer[StreamMessage](64)
	ctx := context.Background()
	delta := StreamMessage{Type: StreamTypeTextDelta, Role: RoleAssistant, Value: NewTextDeltaValue("hello")}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Write(ctx, delta)
		buf.Read()
	}
}

// BenchmarkTypedBuffer_Capacities measures write/read throughput at various buffer sizes.
func BenchmarkTypedBuffer_Capacities(b *testing.B) {
	for _, cap := range []int{1, 8, 64, 256, 1024} {
		b.Run(fmt.Sprintf("cap=%d", cap), func(b *testing.B) {
			buf := NewTypedBuffer[int](cap)
			ctx := context.Background()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				buf.Write(ctx, i)
				buf.Read()
			}
		})
	}
}
