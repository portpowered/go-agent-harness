package tools

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// SleepTool pauses execution for a given duration. Useful for rate limiting,
// waiting for external state, or simulating delays.
type SleepTool struct{}

// NewSleepTool creates a SleepTool.
func NewSleepTool() *SleepTool {
	return &SleepTool{}
}

func (t *SleepTool) Name() string {
	return "sleep"
}

func (t *SleepTool) Description() string {
	return "Sleep for a given duration (e.g. 2s, 1m). Use for rate limiting or waiting. Accepts Go duration strings (e.g. 500ms, 2s, 1m) or a number of seconds."
}

func (t *SleepTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"duration": map[string]any{
				"type":        "string",
				"description": "Duration as a string (e.g. 1s, 2m, 500ms) or number of seconds",
			},
		},
		"required": []string{"duration"},
	}
}

func (t *SleepTool) Execute(ctx context.Context, args map[string]any) ([]messages.Message, error) {
	var d time.Duration

	switch v := args["duration"].(type) {
	case string:
		parsed, err := time.ParseDuration(v)
		if err != nil {
			// Try as seconds
			if sec, err := strconv.ParseFloat(v, 64); err == nil && sec >= 0 {
				d = time.Duration(sec * float64(time.Second))
			} else {
				return nil, fmt.Errorf("invalid duration %q: %w (use e.g. 1s, 2m, 500ms)", v, err)
			}
		} else {
			d = parsed
		}
	case float64:
		if v < 0 {
			return nil, fmt.Errorf("duration must be non-negative, got %g", v)
		}
		d = time.Duration(v * float64(time.Second))
	case int:
		if v < 0 {
			return nil, fmt.Errorf("duration must be non-negative, got %d", v)
		}
		d = time.Duration(v) * time.Second
	default:
		return nil, fmt.Errorf("duration is required (string like 1s or number of seconds)")
	}

	if d <= 0 {
		return []messages.Message{messages.NewTextMessage(messages.RoleTool, "Slept for 0s (no-op).")}, nil
	}

	// Sleep in a way that respects context cancellation
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		msg := fmt.Sprintf("Slept for %s.", d)
		return []messages.Message{messages.NewTextMessage(messages.RoleTool, msg)}, nil
	}
}
