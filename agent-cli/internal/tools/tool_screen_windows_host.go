//go:build windows

package tools

import "syscall"

// windowsScreenProcs are the user32/gdi32 entry points the host display
// adapter calls. Mouse interaction is owned by the reusable tools service and
// is intentionally absent here.
type windowsScreenProcs struct {
	getSystemMetrics, getDC, releaseDC                    *syscall.LazyProc
	createCompatibleDC, createCompatibleBitmap, selectObj *syscall.LazyProc
	bitBlt, getDIBits, deleteObject, deleteDC             *syscall.LazyProc
}

// newWindowsScreenProcs resolves the display entry points lazily on first call.
func newWindowsScreenProcs() windowsScreenProcs {
	user32 := syscall.NewLazyDLL("user32.dll")
	gdi32 := syscall.NewLazyDLL("gdi32.dll")
	return windowsScreenProcs{
		getSystemMetrics:       user32.NewProc("GetSystemMetrics"),
		getDC:                  user32.NewProc("GetDC"),
		releaseDC:              user32.NewProc("ReleaseDC"),
		createCompatibleDC:     gdi32.NewProc("CreateCompatibleDC"),
		createCompatibleBitmap: gdi32.NewProc("CreateCompatibleBitmap"),
		selectObj:              gdi32.NewProc("SelectObject"),
		bitBlt:                 gdi32.NewProc("BitBlt"),
		getDIBits:              gdi32.NewProc("GetDIBits"),
		deleteObject:           gdi32.NewProc("DeleteObject"),
		deleteDC:               gdi32.NewProc("DeleteDC"),
	}
}

// primaryDisplaySize reports the primary display size in pixels.
func (p windowsScreenProcs) primaryDisplaySize() (uintptr, uintptr) {
	w, _, _ := p.getSystemMetrics.Call(uintptr(smCxScreen))
	h, _, _ := p.getSystemMetrics.Call(uintptr(smCyScreen))
	return w, h
}

const (
	smCxScreen = 0
	smCyScreen = 1
)
