//go:build linux

package mouse

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	display "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/display"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

const (
	linuxScreenFixtureEnv = "GO_AGENT_HARNESS_SCREEN_FIXTURE"
	linuxXdotoolLogEnv    = "GO_AGENT_HARNESS_XDOTOOL_LOG"
	linuxXdotoolModeEnv   = "GO_AGENT_HARNESS_XDOTOOL_MODE"
	linuxXrandrModeEnv    = "GO_AGENT_HARNESS_XRANDR_MODE"
)

func screenDisplayCount() int {
	count, err := display.NewHostDisplaySurface().DisplayCount(context.Background())
	if err != nil {
		return 0
	}
	return count
}

// steppingClock advances its own time by each requested wait and fires the
// timer immediately, so recording frame pacing does not sleep.
type steppingClock struct{ now time.Time }

func (c *steppingClock) Now() time.Time { return c.now }

func (c *steppingClock) NewTimer(duration time.Duration) platformclock.Timer {
	c.now = c.now.Add(duration)
	fired := make(chan time.Time, 1)
	fired <- c.now
	return firedTimer{c: fired}
}

type firedTimer struct{ c chan time.Time }

func (t firedTimer) C() <-chan time.Time { return t.c }
func (firedTimer) Stop() bool            { return false }

func fakeLinuxDesktop(t *testing.T, fixture []byte) (dir, logPath string) {
	t.Helper()
	dir = t.TempDir()
	fixturePath := filepath.Join(dir, "fixture.png")
	if err := os.WriteFile(fixturePath, fixture, 0o644); err != nil {
		t.Fatal(err)
	}
	logPath = filepath.Join(dir, "xdotool.log")
	t.Setenv(linuxScreenFixtureEnv, fixturePath)
	t.Setenv(linuxXdotoolLogEnv, logPath)
	t.Setenv(linuxXdotoolModeEnv, "ok")
	t.Setenv(linuxXrandrModeEnv, "ok")
	writeFakeCommand(t, dir, "xrandr", `if [ "${GO_AGENT_HARNESS_XRANDR_MODE}" = ok ]; then printf 'Monitors: 2\n'; else exit 1; fi`)
	writeFakeCommand(t, dir, "xdotool", `
printf '%s\n' "$*" >> "$GO_AGENT_HARNESS_XDOTOOL_LOG"
case "${1-}" in
  getdisplaygeometry) [ "${GO_AGENT_HARNESS_XDOTOOL_MODE}" = ok ] && printf '8 6\n' || exit 1 ;;
  getmouselocation) printf 'X=2 Y=3 screen=0 window=0\n' ;;
esac`)
	writeFakeCommand(t, dir, "scrot", `
if [ "${GO_AGENT_HARNESS_XDOTOOL_MODE}" = fail ]; then
  printf 'capture failed\n' >&2
  exit 2
fi
cp "$GO_AGENT_HARNESS_SCREEN_FIXTURE" "${3}"`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir, logPath
}

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	img.Set(1, 1, color.RGBA{G: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func grayPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 2, 2))
	img.SetGray(1, 1, color.Gray{Y: 200})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestS12LinuxScreenFakeCaptureAndRecord(t *testing.T) {
	fakeLinuxDesktop(t, tinyPNG(t))
	tool := display.NewScreenToolWithOptions(display.ScreenToolOptions{
		DisplaySurface: display.NewHostDisplaySurface(),
		Clock:          &steppingClock{now: time.Unix(1, 0)},
	})

	msgs, err := tool.Execute(context.Background(), map[string]any{"display": float64(1)})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || len(msgs[0].ContentParts) != 2 {
		t.Fatalf("screenshot result shape = %#v", msgs)
	}
	assertScreenResult(t, msgs[0], "image/jpeg", 2, 2)

	msgs, err = tool.Execute(context.Background(), map[string]any{"action": "record", "display": float64(1), "duration": float64(1), "fps": float64(2)})
	if err != nil {
		t.Fatal(err)
	}
	recording := assertScreenResult(t, msgs[0], "image/gif", 2, 2)
	if recording.MediaType != "image/gif" || len(recording.Bytes) == 0 {
		t.Fatalf("recording image = %#v", recording)
	}
	decoded, err := gif.DecodeAll(bytes.NewReader(recording.Bytes))
	if err != nil || len(decoded.Image) != 2 {
		t.Fatalf("recording GIF frames = %d, err = %v", len(decoded.Image), err)
	}
}

func TestS12LinuxMouseFakeOperations(t *testing.T) {
	cases := []struct {
		action  string
		args    map[string]any
		want    string
		wantLog []string
	}{
		{"move", map[string]any{"action": "move", "x": float64(2), "y": float64(3)}, "Mouse moved to (2, 3)", []string{"mousemove 2 3"}},
		{"click", map[string]any{"action": "click", "x": float64(2), "y": float64(3), "button": "right"}, "right click at (2, 3)", []string{"mousemove 2 3 click 3"}},
		{"double", map[string]any{"action": "double_click", "x": float64(2), "y": float64(3), "button": "middle"}, "middle double-click at (2, 3)", []string{"mousemove 2 3 click 2", "mousemove 2 3 click 2"}},
		{"down", map[string]any{"action": "down", "x": float64(2), "y": float64(3)}, "left button held at (2, 3)", []string{"mousemove 2 3", "mousedown 1"}},
		{"up", map[string]any{"action": "up", "x": float64(2), "y": float64(3)}, "left button released at (2, 3)", []string{"mousemove 2 3", "mouseup 1"}},
		{"drag", map[string]any{"action": "drag", "x": float64(2), "y": float64(3), "to_x": float64(6), "to_y": float64(7)}, "left drag from (2, 3) to (6, 7)", expectedLinuxDragLog(2, 3, 6, 7)},
	}
	for _, tt := range cases {
		t.Run(tt.action, func(t *testing.T) {
			t.Parallel()
			process := &fakeMouseProcess{}
			var sleeps recordedSleeps
			tool := NewMouseToolWithOptions(MouseToolOptions{Process: process, Sleep: sleeps.sleep})
			msgs, err := tool.Execute(context.Background(), tt.args)
			if err != nil {
				t.Fatal(err)
			}
			if len(msgs) != 1 || msgs[0].TextContent() != tt.want {
				t.Fatalf("result = %#v, want %q", msgs, tt.want)
			}
			assertMouseCalls(t, process, "xdotool", tt.wantLog)
		})
	}
}

// TestS12LinuxMouseToolRunsXdotoolSubprocess keeps the production process
// runner covered end to end with one fake xdotool executable on PATH.
func TestS12LinuxMouseToolRunsXdotoolSubprocess(t *testing.T) {
	_, logPath := fakeLinuxDesktop(t, tinyPNG(t))
	msgs, err := NewMouseTool().Execute(context.Background(), map[string]any{"action": "move", "x": float64(2), "y": float64(3)})
	if err != nil || len(msgs) != 1 || msgs[0].TextContent() != "Mouse moved to (2, 3)" {
		t.Fatalf("mouse result = %#v, err = %v", msgs, err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != "mousemove 2 3" {
		t.Fatalf("xdotool log = %q, want %q", got, "mousemove 2 3")
	}
}

func expectedLinuxDragLog(fromX, fromY, toX, toY int) []string {
	lines := []string{fmt.Sprintf("mousemove %d %d", fromX, fromY), "mousedown 1"}
	const steps = 20
	for i := 1; i <= steps; i++ {
		ix := fromX + (toX-fromX)*i/steps
		iy := fromY + (toY-fromY)*i/steps
		lines = append(lines, fmt.Sprintf("mousemove %d %d", ix, iy))
	}
	return append(lines, "mouseup 1")
}

func TestS12LinuxHelpersAndCapabilityErrors(t *testing.T) {
	dir, _ := fakeLinuxDesktop(t, grayPNG(t))
	assertLinuxDisplayHelpers(t)
	assertLinuxImageHelpers(t, dir)
	assertLinuxCaptureFailures(t)
	assertLinuxMissingExecutables(t)
	assertLinuxMouseFailures(t)
}

func assertLinuxDisplayHelpers(t *testing.T) {
	t.Helper()
	if got := screenDisplayCount(); got != 2 {
		t.Fatalf("screenDisplayCount = %d, want 2", got)
	}
	if got := screenDisplayBounds(0); got.Dx() != 8 || got.Dy() != 6 {
		t.Fatalf("screenDisplayBounds = %v", got)
	}
	t.Setenv(linuxXrandrModeEnv, "fail")
	if got := screenDisplayCount(); got != 0 {
		t.Fatalf("xrandr failed count = %d, want unavailable zero", got)
	}
	t.Setenv(linuxXrandrModeEnv, "ok")
	t.Setenv(linuxXdotoolModeEnv, "fail")
	if got := screenDisplayBounds(0); !got.Empty() {
		t.Fatalf("xdotool failed bounds = %v, want empty unavailable bounds", got)
	}
	t.Setenv(linuxXdotoolModeEnv, "ok")
}

func assertLinuxImageHelpers(t *testing.T, dir string) {
	t.Helper()
	img, err := display.NewHostDisplaySurface().Capture(context.Background(), image.Rect(0, 0, 2, 2))
	if err != nil || img.Bounds().Dx() != 2 {
		t.Fatalf("screenCapture = %v, err = %v", img.Bounds(), err)
	}
	if decoded, err := loadPNGasRGBA(filepath.Join(dir, "fixture.png")); err != nil || decoded.Bounds().Empty() {
		t.Fatalf("loadPNGasRGBA gray image = %v, err = %v", decoded.Bounds(), err)
	}
	if _, err := loadPNGasRGBA(filepath.Join(dir, "missing.png")); err == nil || !strings.Contains(err.Error(), "open screenshot") {
		t.Fatalf("missing PNG error = %v", err)
	}
	bad := filepath.Join(dir, "bad.png")
	if err := os.WriteFile(bad, []byte("not png"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPNGasRGBA(bad); err == nil || !strings.Contains(err.Error(), "decode screenshot") {
		t.Fatalf("invalid PNG error = %v", err)
	}
}

func assertLinuxCaptureFailures(t *testing.T) {
	t.Helper()
	t.Setenv(linuxXdotoolModeEnv, "fail")
	if _, err := display.NewHostDisplaySurface().Capture(context.Background(), image.Rect(0, 0, 2, 2)); err == nil || !strings.Contains(err.Error(), "scrot -a") {
		t.Fatalf("failed scrot error = %v", err)
	}
	t.Setenv(linuxXdotoolModeEnv, "ok")
	if got := xdotoolButton("left"); got != "1" || xdotoolButton("right") != "3" || xdotoolButton("middle") != "2" || xdotoolButton("other") != "1" {
		t.Fatalf("xdotoolButton mapping incorrect")
	}
}

func assertLinuxMissingExecutables(t *testing.T) {
	t.Helper()
	missingDir := t.TempDir()
	t.Setenv("PATH", missingDir)
	if _, err := display.NewHostDisplaySurface().Capture(context.Background(), image.Rect(0, 0, 2, 2)); err == nil || !strings.Contains(err.Error(), "scrot not found") {
		t.Fatalf("missing scrot error = %v", err)
	}
	if err := newMouseDriver(MouseToolOptions{}).runXdotool("move"); err == nil || !strings.Contains(err.Error(), "xdotool not found") {
		t.Fatalf("missing xdotool error = %v", err)
	}
}

func assertLinuxMouseFailures(t *testing.T) {
	t.Helper()
	var sleeps recordedSleeps
	moveFails := &fakeMouseProcess{run: func([]string) ([]byte, error) { return nil, errors.New("exit status 7") }}
	driver := newFakeMouseDriver(moveFails, &sleeps)
	if err := driver.buttonDown(1, 2, "left"); err == nil {
		t.Fatal("mousedown failure was not returned")
	}
	if err := driver.buttonUp(1, 2, "left"); err == nil {
		t.Fatal("mouseup failure was not returned")
	}
	assertMouseCalls(t, moveFails, "xdotool", []string{"mousemove 1 2", "mousemove 1 2"})
	missing := &fakeMouseProcess{run: func([]string) ([]byte, error) { return nil, helperNotFound("xdotool") }}
	if err := newFakeMouseDriver(missing, &sleeps).move(1, 2); err == nil || !strings.Contains(err.Error(), "xdotool not found") {
		t.Fatalf("missing xdotool error = %v", err)
	}
	stepFailure := &fakeMouseProcess{run: func(args []string) ([]byte, error) {
		if len(args) == 3 && args[1] != "1" {
			return nil, errors.New("exit status 7")
		}
		return nil, nil
	}}
	if err := newFakeMouseDriver(stepFailure, &sleeps).drag(1, 1, 21, 21, "left"); err == nil || !strings.Contains(err.Error(), "drag step 1") {
		t.Fatalf("drag step error = %v", err)
	}
	assertMouseCalls(t, stepFailure, "xdotool", []string{"mousemove 1 1", "mousedown 1", "mousemove 2 2", "mouseup 1"})
	if sleeps.total() != mouseDragPause {
		t.Fatalf("sleeps = %v, want only the drag press pause", sleeps)
	}
}

func TestS12LinuxRealCapabilities(t *testing.T) {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		t.Skipf("%s: unavailable capability: display server (DISPLAY/WAYLAND_DISPLAY)", runtime.GOOS)
	}
	for _, command := range []string{"scrot", "xdotool"} {
		if _, err := exec.LookPath(command); err != nil {
			t.Skipf("%s: unavailable capability: %s executable", runtime.GOOS, command)
		}
	}
	tool := display.NewScreenTool()
	msgs, err := tool.Execute(context.Background(), map[string]any{"action": "screenshot"})
	if err != nil {
		t.Skipf("%s: unavailable capability: live screen capture (%v)", runtime.GOOS, err)
	}
	part, ok := msgs[0].ContentParts[1].(messages.ImagePart)
	if !ok {
		t.Fatalf("live screenshot content part = %T, want messages.ImagePart", msgs[0].ContentParts[1])
	}
	if part.MediaType != "image/jpeg" || len(part.Bytes) == 0 {
		t.Fatalf("live screenshot did not produce non-empty JPEG: %#v", part)
	}
}

func TestS4LinuxProcessErrorIdentity(t *testing.T) {
	process := &fakeMouseProcess{run: func([]string) ([]byte, error) {
		return []byte("command failed\n"), errors.New("exit status 9")
	}}
	var sleeps recordedSleeps
	err := newFakeMouseDriver(process, &sleeps).runXdotool("mousemove", "1", "2")
	if err == nil || !strings.Contains(err.Error(), "xdotool [mousemove 1 2]") || !strings.Contains(err.Error(), "command failed") {
		t.Fatalf("process error = %v", err)
	}

}
