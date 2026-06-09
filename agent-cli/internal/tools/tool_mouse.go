package tools

import (
	"context"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// MouseTool lets the model control the mouse: move, click, double-click, hold,
// drag, and release.  Platform-specific implementations live in
// tool_mouse_windows.go and tool_mouse_other.go.
type MouseTool struct{}

func NewMouseTool() *MouseTool { return &MouseTool{} }

func (t *MouseTool) Name() string { return "mouse" }

func (t *MouseTool) Description() string {
	return "Control the mouse cursor: move, click, double-click, hold a button, drag, or release. " +
		"Coordinates are screen pixels from the top-left corner of the primary display."
}

func (t *MouseTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string",
				"description": "Mouse action: " +
					"'move' – move cursor to (x, y); " +
					"'click' – press and release a button at (x, y); " +
					"'double_click' – two clicks in quick succession at (x, y); " +
					"'down' – hold a mouse button at (x, y); " +
					"'up' – release a mouse button at (x, y); " +
					"'drag' – hold button at (x, y), move to (to_x, to_y), release.",
				"enum": []string{"move", "click", "double_click", "down", "up", "drag"},
			},
			"x": map[string]any{
				"type":        "integer",
				"description": "X coordinate in screen pixels (from left edge).",
			},
			"y": map[string]any{
				"type":        "integer",
				"description": "Y coordinate in screen pixels (from top edge).",
			},
			"to_x": map[string]any{
				"type":        "integer",
				"description": "Destination X coordinate for the 'drag' action.",
			},
			"to_y": map[string]any{
				"type":        "integer",
				"description": "Destination Y coordinate for the 'drag' action.",
			},
			"button": map[string]any{
				"type":        "string",
				"description": "Which mouse button to use: 'left', 'right', or 'middle'. Defaults to 'left'.",
				"enum":        []string{"left", "right", "middle"},
			},
		},
		"required": []string{"action", "x", "y"},
	}
}

func (t *MouseTool) Execute(ctx context.Context, args map[string]any) ([]messages.Message, error) {
	action, _ := args["action"].(string)
	if action == "" {
		return nil, fmt.Errorf("action is required")
	}

	xf, okX := args["x"].(float64)
	yf, okY := args["y"].(float64)
	if !okX || !okY {
		return nil, fmt.Errorf("x and y coordinates are required")
	}
	x, y := int(xf), int(yf)

	button := "left"
	if b, ok := args["button"].(string); ok && b != "" {
		button = b
	}

	var err error
	var result string

	switch action {
	case "move":
		err = mouseMove(x, y)
		result = fmt.Sprintf("Mouse moved to (%d, %d)", x, y)

	case "click":
		err = mouseClick(x, y, button)
		result = fmt.Sprintf("%s click at (%d, %d)", button, x, y)

	case "double_click":
		err = mouseDoubleClick(x, y, button)
		result = fmt.Sprintf("%s double-click at (%d, %d)", button, x, y)

	case "down":
		err = mouseButtonDown(x, y, button)
		result = fmt.Sprintf("%s button held at (%d, %d)", button, x, y)

	case "up":
		err = mouseButtonUp(x, y, button)
		result = fmt.Sprintf("%s button released at (%d, %d)", button, x, y)

	case "drag":
		toXf, okTX := args["to_x"].(float64)
		toYf, okTY := args["to_y"].(float64)
		if !okTX || !okTY {
			return nil, fmt.Errorf("to_x and to_y are required for the drag action")
		}
		toX, toY := int(toXf), int(toYf)
		err = mouseDrag(x, y, toX, toY, button)
		result = fmt.Sprintf("%s drag from (%d, %d) to (%d, %d)", button, x, y, toX, toY)

	default:
		return nil, fmt.Errorf("unknown action %q", action)
	}

	if err != nil {
		return nil, err
	}
	return []messages.Message{messages.NewTextMessage(messages.RoleTool, result)}, nil
}
