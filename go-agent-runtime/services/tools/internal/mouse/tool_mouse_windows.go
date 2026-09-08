//go:build windows

package mouse

import (
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

// Windows user32.dll handle and procedures shared by both the mouse tool and
// the screen capture tool (which needs GetSystemMetrics).
var (
	user32dll = syscall.NewLazyDLL("user32.dll")

	procSendInput     = user32dll.NewProc("SendInput")
	procSetCursorPos  = user32dll.NewProc("SetCursorPos")
	procGetSysMetrics = user32dll.NewProc("GetSystemMetrics")
)

const (
	smCxScreen = 0
	smCyScreen = 1

	inputTypeMouse = 0

	// MOUSEEVENTF_* flags (winuser.h)
	mouseeventfMove     = uint32(0x0001)
	mouseeventfLDown    = uint32(0x0002)
	mouseeventfLUp      = uint32(0x0004)
	mouseeventfRDown    = uint32(0x0008)
	mouseeventfRUp      = uint32(0x0010)
	mouseeventfMDown    = uint32(0x0020)
	mouseeventfMUp      = uint32(0x0040)
	mouseeventfAbsolute = uint32(0x8000)
)

// winMouseInput mirrors the MOUSEINPUT struct from winuser.h.
//
// Go's alignment rules produce the same in-memory layout as the C struct:
//
//	On 64-bit  (uintptr = 8):  DX(4)+DY(4)+MouseData(4)+Flags(4)+Time(4)+[4pad]+ExtraInfo(8)  = 32 bytes
//	On 32-bit  (uintptr = 4):  DX(4)+DY(4)+MouseData(4)+Flags(4)+Time(4)+ExtraInfo(4)          = 24 bytes
type winMouseInput struct {
	DX        int32
	DY        int32
	MouseData uint32
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}

// winInput mirrors the INPUT struct.  Go inserts the correct padding between
// Type and MI so that MI is aligned to its own natural alignment (8 bytes on
// 64-bit, 4 bytes on 32-bit), matching the Windows union layout exactly.
//
//	64-bit: Type(4)+[4pad]+MI(32) = 40 bytes
//	32-bit: Type(4)+MI(24)        = 28 bytes
type winInput struct {
	Type uint32
	MI   winMouseInput
}

// primaryScreenSize returns the width and height of the primary display in
// screen pixels.
func primaryScreenSize() (w, h int) {
	cw, _, _ := procGetSysMetrics.Call(uintptr(smCxScreen))
	ch, _, _ := procGetSysMetrics.Call(uintptr(smCyScreen))
	return int(cw), int(ch)
}

// sendMouseEvent fires a single synthetic mouse event via SendInput.  The dx/dy
// values are in screen pixels; they are normalised to the [0, 65535] range
// required by MOUSEEVENTF_ABSOLUTE before being sent.
func sendMouseEvent(x, y int, flags uint32) error {
	sw, sh := primaryScreenSize()
	normX, normY := int32(0), int32(0)
	if sw > 0 && sh > 0 {
		normX = int32(x * 65535 / sw)
		normY = int32(y * 65535 / sh)
	}

	inp := winInput{
		Type: inputTypeMouse,
		MI: winMouseInput{
			DX:    normX,
			DY:    normY,
			Flags: flags,
		},
	}

	ret, _, err := procSendInput.Call(1, uintptr(unsafe.Pointer(&inp)), unsafe.Sizeof(inp))
	if ret == 0 {
		return fmt.Errorf("SendInput failed: %w", err)
	}
	return nil
}

// buttonFlags returns the press/release event flags for a named mouse button.
func buttonFlags(button string) (downFlag, upFlag uint32) {
	switch button {
	case "right":
		return mouseeventfRDown, mouseeventfRUp
	case "middle":
		return mouseeventfMDown, mouseeventfMUp
	default: // "left"
		return mouseeventfLDown, mouseeventfLUp
	}
}

// mouseMove moves the cursor to the given screen coordinates using SetCursorPos
// for pixel-accurate positioning.
func mouseMove(x, y int) error {
	ret, _, err := procSetCursorPos.Call(uintptr(x), uintptr(y))
	if ret == 0 {
		return fmt.Errorf("SetCursorPos failed: %w", err)
	}
	return nil
}

// mouseClick moves to (x, y), presses, then releases the specified button.
func mouseClick(x, y int, button string) error {
	down, up := buttonFlags(button)
	// Combine MOVE + button-down into a single event so applications see the
	// correct cursor position when they receive the WM_BUTTONDOWN message.
	if err := sendMouseEvent(x, y, mouseeventfMove|mouseeventfAbsolute|down); err != nil {
		return err
	}
	time.Sleep(50 * time.Millisecond)
	return sendMouseEvent(x, y, mouseeventfMove|mouseeventfAbsolute|up)
}

// mouseDoubleClick sends two click events in quick succession.
func mouseDoubleClick(x, y int, button string) error {
	if err := mouseClick(x, y, button); err != nil {
		return err
	}
	time.Sleep(50 * time.Millisecond)
	return mouseClick(x, y, button)
}

// mouseButtonDown moves to (x, y) and holds the specified button.
func mouseButtonDown(x, y int, button string) error {
	down, _ := buttonFlags(button)
	return sendMouseEvent(x, y, mouseeventfMove|mouseeventfAbsolute|down)
}

// mouseButtonUp moves to (x, y) and releases the specified button.
func mouseButtonUp(x, y int, button string) error {
	_, up := buttonFlags(button)
	return sendMouseEvent(x, y, mouseeventfMove|mouseeventfAbsolute|up)
}

// mouseDrag presses the button at (fromX, fromY), glides the cursor in small
// incremental steps to (toX, toY), then releases.
func mouseDrag(fromX, fromY, toX, toY int, button string) error {
	down, up := buttonFlags(button)

	// Press at the start position.
	if err := sendMouseEvent(fromX, fromY, mouseeventfMove|mouseeventfAbsolute|down); err != nil {
		return fmt.Errorf("drag start: %w", err)
	}
	time.Sleep(30 * time.Millisecond)

	// Move in 20 equal steps so the operating system sees smooth cursor motion,
	// which is required for drag-sensitive widgets (e.g. sliders, scrollbars).
	const steps = 20
	for i := 1; i <= steps; i++ {
		ix := fromX + (toX-fromX)*i/steps
		iy := fromY + (toY-fromY)*i/steps
		if err := sendMouseEvent(ix, iy, mouseeventfMove|mouseeventfAbsolute); err != nil {
			return fmt.Errorf("drag step %d: %w", i, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Release at the destination.
	return sendMouseEvent(toX, toY, mouseeventfMove|mouseeventfAbsolute|up)
}
