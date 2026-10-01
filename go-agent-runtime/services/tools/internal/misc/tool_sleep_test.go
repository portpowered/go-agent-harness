package misc

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestSleepTool_Execute_DurationString(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tool := NewSleepTool()

		start := time.Now()
		msgs, err := tool.Execute(t.Context(), map[string]any{"duration": "50ms"})
		elapsed := time.Since(start)

		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if len(msgs) != 1 {
			t.Fatalf("expected 1 message, got %d", len(msgs))
		}
		if elapsed != 50*time.Millisecond {
			t.Errorf("slept %v, want exactly 50ms", elapsed)
		}
		if text := msgs[0].TextContent(); text != "Slept for 50ms." {
			t.Errorf("unexpected message: %q", text)
		}
	})
}

func TestSleepTool_Execute_SecondsNumber(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tool := NewSleepTool()

		start := time.Now()
		msgs, err := tool.Execute(t.Context(), map[string]any{"duration": 0.05}) // 50ms as seconds
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if len(msgs) != 1 {
			t.Fatalf("expected 1 message, got %d", len(msgs))
		}
		if elapsed := time.Since(start); elapsed != 50*time.Millisecond {
			t.Errorf("slept %v, want exactly 50ms", elapsed)
		}
	})
}

func TestSleepTool_Execute_ContextCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		tool := NewSleepTool()

		result := make(chan error, 1)
		go func() {
			_, err := tool.Execute(ctx, map[string]any{"duration": "30s"})
			result <- err
		}()
		// The tool is now waiting on its timer.
		synctest.Wait()
		cancel()

		err := <-result
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	})
}

func TestSleepTool_Execute_InvalidDuration(t *testing.T) {
	tool := NewSleepTool()

	_, err := tool.Execute(t.Context(), map[string]any{"duration": "not-a-duration"})
	if err == nil {
		t.Error("expected error for invalid duration")
	}
}
