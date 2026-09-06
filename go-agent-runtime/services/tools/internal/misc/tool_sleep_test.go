package misc

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSleepTool_Execute_DurationString(t *testing.T) {
	ctx := context.Background()
	tool := NewSleepTool()

	start := time.Now()
	msgs, err := tool.Execute(ctx, map[string]any{"duration": "50ms"})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if elapsed < 45*time.Millisecond {
		t.Errorf("expected to sleep at least 45ms, elapsed %v", elapsed)
	}
	text := msgs[0].TextContent()
	if text == "" || text[:5] != "Slept" {
		t.Errorf("unexpected message: %q", text)
	}
}

func TestSleepTool_Execute_SecondsNumber(t *testing.T) {
	ctx := context.Background()
	tool := NewSleepTool()

	msgs, err := tool.Execute(ctx, map[string]any{"duration": 0.05}) // 50ms as seconds
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
}

func TestSleepTool_Execute_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tool := NewSleepTool()

	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	_, err := tool.Execute(ctx, map[string]any{"duration": "30s"})
	if err == nil {
		t.Error("expected context cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestSleepTool_Execute_InvalidDuration(t *testing.T) {
	ctx := context.Background()
	tool := NewSleepTool()

	_, err := tool.Execute(ctx, map[string]any{"duration": "not-a-duration"})
	if err == nil {
		t.Error("expected error for invalid duration")
	}
}
