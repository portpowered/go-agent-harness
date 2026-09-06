package mouse

import (
	"context"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// MouseTool lets the model control the mouse: move, click, double-click, hold,
// drag, and release.  Platform-specific implementations live in
// tool_mouse_windows.go and tool_mouse_other.go.
type MouseTool struct{}

const mouseButtonLeft = "left"

type mouseInvocation struct {
	action       string
	x, y         int
	button       string
	toX, toY     int
	hasDragPoint bool
}

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
				"enum":        []string{mouseButtonLeft, "right", "middle"},
			},
		},
		"required": []string{"action", "x", "y"},
	}
}

func (t *MouseTool) Execute(_ context.Context, args map[string]any) ([]messages.Message, error) {
	invocation, err := parseMouseInvocation(args)
	if err != nil {
		return nil, err
	}
	result, err := executeMouseInvocation(invocation)
	if err != nil {
		return nil, err
	}
	return []messages.Message{messages.NewTextMessage(messages.RoleTool, result)}, nil
}

func parseMouseInvocation(args map[string]any) (mouseInvocation, error) {
	action, ok := args["action"].(string)
	if !ok || action == "" {
		return mouseInvocation{}, fmt.Errorf("action is required")
	}
	xf, okX := args["x"].(float64)
	yf, okY := args["y"].(float64)
	if !okX || !okY {
		return mouseInvocation{}, fmt.Errorf("x and y coordinates are required")
	}
	button := mouseButtonLeft
	if candidate, ok := args["button"].(string); ok && candidate != "" {
		button = candidate
	}
	invocation := mouseInvocation{action: action, x: int(xf), y: int(yf), button: button}
	if action == "drag" {
		toX, toY, ok := dragCoordinates(args)
		if !ok {
			return mouseInvocation{}, fmt.Errorf("to_x and to_y are required for the drag action")
		}
		invocation.toX, invocation.toY, invocation.hasDragPoint = toX, toY, true
	}
	return invocation, nil
}

func dragCoordinates(args map[string]any) (int, int, bool) {
	toX, okX := args["to_x"].(float64)
	toY, okY := args["to_y"].(float64)
	return int(toX), int(toY), okX && okY
}

func executeMouseInvocation(invocation mouseInvocation) (string, error) {
	var err error
	var result string
	switch invocation.action {
	case "move":
		err, result = mouseMove(invocation.x, invocation.y), fmt.Sprintf("Mouse moved to (%d, %d)", invocation.x, invocation.y)
	case "click":
		err, result = mouseClick(invocation.x, invocation.y, invocation.button), fmt.Sprintf("%s click at (%d, %d)", invocation.button, invocation.x, invocation.y)
	case "double_click":
		err, result = mouseDoubleClick(invocation.x, invocation.y, invocation.button), fmt.Sprintf("%s double-click at (%d, %d)", invocation.button, invocation.x, invocation.y)
	case "down":
		err, result = mouseButtonDown(invocation.x, invocation.y, invocation.button), fmt.Sprintf("%s button held at (%d, %d)", invocation.button, invocation.x, invocation.y)
	case "up":
		err, result = mouseButtonUp(invocation.x, invocation.y, invocation.button), fmt.Sprintf("%s button released at (%d, %d)", invocation.button, invocation.x, invocation.y)
	case "drag":
		if !invocation.hasDragPoint {
			return "", fmt.Errorf("to_x and to_y are required for the drag action")
		}
		err = mouseDrag(invocation.x, invocation.y, invocation.toX, invocation.toY, invocation.button)
		result = fmt.Sprintf("%s drag from (%d, %d) to (%d, %d)", invocation.button, invocation.x, invocation.y, invocation.toX, invocation.toY)
	default:
		return "", fmt.Errorf("unknown action %q", invocation.action)
	}
	return result, err
}
