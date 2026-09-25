package mouse

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// MouseTool lets the model control the mouse: move, click, double-click, hold,
// drag, and release.  Platform-specific implementations live in
// tool_mouse_darwin.go, tool_mouse_linux.go, tool_mouse_windows.go and
// tool_mouse_other.go.
type MouseTool struct {
	driver mouseDriver
}

// MouseProcess runs one mouse helper command (cliclick on macOS, xdotool on
// Linux) and returns its combined output. Windows uses Win32 input directly
// and never runs a process.
type MouseProcess interface {
	Run(name string, args ...string) ([]byte, error)
}

// MouseProcessFunc adapts a function to MouseProcess.
type MouseProcessFunc func(name string, args ...string) ([]byte, error)

// Run calls f.
func (f MouseProcessFunc) Run(name string, args ...string) ([]byte, error) { return f(name, args...) }

// MouseToolOptions configures the process and pacing seams of MouseTool.
// Nil fields select the host process runner and time.Sleep.
type MouseToolOptions struct {
	Process MouseProcess
	Sleep   func(time.Duration)
}

type osMouseProcess struct{}

func (osMouseProcess) Run(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// mouseDriver carries the injected seams used by the platform operations.
type mouseDriver struct {
	process MouseProcess
	sleep   func(time.Duration)
}

func newMouseDriver(options MouseToolOptions) mouseDriver {
	driver := mouseDriver{process: options.Process, sleep: options.Sleep}
	if driver.process == nil {
		driver.process = osMouseProcess{}
	}
	if driver.sleep == nil {
		driver.sleep = time.Sleep
	}
	return driver
}

const mouseButtonLeft = "left"

type mouseInvocation struct {
	action       string
	x, y         int
	button       string
	toX, toY     int
	hasDragPoint bool
}

func NewMouseTool() *MouseTool { return NewMouseToolWithOptions(MouseToolOptions{}) }

// NewMouseToolWithOptions injects the process runner and pacing sleep used by
// the platform mouse operations.
func NewMouseToolWithOptions(options MouseToolOptions) *MouseTool {
	return &MouseTool{driver: newMouseDriver(options)}
}

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
	result, err := t.driver.execute(invocation)
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

func (d mouseDriver) execute(invocation mouseInvocation) (string, error) {
	var err error
	var result string
	switch invocation.action {
	case "move":
		err, result = d.move(invocation.x, invocation.y), fmt.Sprintf("Mouse moved to (%d, %d)", invocation.x, invocation.y)
	case "click":
		err, result = d.click(invocation.x, invocation.y, invocation.button), fmt.Sprintf("%s click at (%d, %d)", invocation.button, invocation.x, invocation.y)
	case "double_click":
		err, result = d.doubleClick(invocation.x, invocation.y, invocation.button), fmt.Sprintf("%s double-click at (%d, %d)", invocation.button, invocation.x, invocation.y)
	case "down":
		err, result = d.buttonDown(invocation.x, invocation.y, invocation.button), fmt.Sprintf("%s button held at (%d, %d)", invocation.button, invocation.x, invocation.y)
	case "up":
		err, result = d.buttonUp(invocation.x, invocation.y, invocation.button), fmt.Sprintf("%s button released at (%d, %d)", invocation.button, invocation.x, invocation.y)
	case "drag":
		if !invocation.hasDragPoint {
			return "", fmt.Errorf("to_x and to_y are required for the drag action")
		}
		err = d.drag(invocation.x, invocation.y, invocation.toX, invocation.toY, invocation.button)
		result = fmt.Sprintf("%s drag from (%d, %d) to (%d, %d)", invocation.button, invocation.x, invocation.y, invocation.toX, invocation.toY)
	default:
		return "", fmt.Errorf("unknown action %q", invocation.action)
	}
	return result, err
}
