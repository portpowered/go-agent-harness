//go:build windows

package tools

import (
	"context"
	"fmt"
	"image"
	"syscall"
	"unsafe"
)

var (
	gdi32dll = syscall.NewLazyDLL("gdi32.dll")

	// user32.dll procs for screen capture.  user32dll is declared in
	// tool_mouse_windows.go; both files share the same //go:build windows tag
	// so the variable is visible across the package.
	procGetDC     = user32dll.NewProc("GetDC")
	procReleaseDC = user32dll.NewProc("ReleaseDC")

	procCreateCompatibleDC     = gdi32dll.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = gdi32dll.NewProc("CreateCompatibleBitmap")
	procSelectObject           = gdi32dll.NewProc("SelectObject")
	procBitBlt                 = gdi32dll.NewProc("BitBlt")
	procGetDIBits              = gdi32dll.NewProc("GetDIBits")
	procDeleteObject           = gdi32dll.NewProc("DeleteObject")
	procDeleteDC               = gdi32dll.NewProc("DeleteDC")
)

const (
	srccopy      uint32 = 0x00CC0020
	dibRGBColors uint32 = 0
	biRGB        uint32 = 0
)

// bitmapInfoHeader mirrors BITMAPINFOHEADER from wingdi.h.
type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

// bitmapInfo mirrors BITMAPINFO (header + one colour table entry).
type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]uint32
}

// screenDisplayCountWithContextAndProcess returns the primary display only;
// the process parameter is unused because Windows uses GDI directly.
func screenDisplayCountWithContextAndProcess(ctx context.Context, _ DisplayProcess) (int, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
	}
	w, _, _ := procGetSysMetrics.Call(uintptr(smCxScreen))
	h, _, _ := procGetSysMetrics.Call(uintptr(smCyScreen))
	if w == 0 || h == 0 {
		return 0, fmt.Errorf("GetSystemMetrics returned an empty display")
	}
	return 1, nil
}

// screenDisplayBoundsWithContextAndProcess returns the bounding rectangle of
// the primary display. The process parameter is unused because Windows uses
// GDI directly.
func screenDisplayBoundsWithContextAndProcess(ctx context.Context, idx int, _ DisplayProcess) (image.Rectangle, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return image.Rectangle{}, err
		}
	}
	if idx != 0 {
		return image.Rectangle{}, fmt.Errorf("display %d not available (only 1 display(s) found)", idx)
	}
	w, _, _ := procGetSysMetrics.Call(uintptr(smCxScreen))
	h, _, _ := procGetSysMetrics.Call(uintptr(smCyScreen))
	if w == 0 || h == 0 {
		return image.Rectangle{}, fmt.Errorf("GetSystemMetrics returned an empty display")
	}
	return image.Rect(0, 0, int(w), int(h)), nil
}

func screenCapturePrerequisitesWithContextAndProcess(ctx context.Context, _ DisplayProcess) error {
	if ctx != nil {
		return ctx.Err()
	}
	return nil
}

// screenCapture uses the Windows GDI API to copy the given screen region into
// an image.RGBA. No CGO or third-party libraries are required.
func screenCaptureWithContextAndProcess(ctx context.Context, bounds image.Rectangle, _ DisplayProcess) (*image.RGBA, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	width := bounds.Dx()
	height := bounds.Dy()
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("display bounds are empty")
	}

	// Obtain the device context for the entire screen.
	hScreen, _, _ := procGetDC.Call(0)
	if hScreen == 0 {
		return nil, fmt.Errorf("GetDC failed")
	}
	defer procReleaseDC.Call(0, hScreen)

	// Create a compatible (in-memory) device context.
	hMemDC, _, _ := procCreateCompatibleDC.Call(hScreen)
	if hMemDC == 0 {
		return nil, fmt.Errorf("CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(hMemDC)

	// Create a compatible bitmap to receive the screen content.
	hBitmap, _, _ := procCreateCompatibleBitmap.Call(hScreen, uintptr(width), uintptr(height))
	if hBitmap == 0 {
		return nil, fmt.Errorf("CreateCompatibleBitmap failed")
	}
	defer procDeleteObject.Call(hBitmap)

	// Select the bitmap into the memory DC.
	procSelectObject.Call(hMemDC, hBitmap)

	// BitBlt copies the screen region into our memory bitmap.
	ret, _, _ := procBitBlt.Call(
		hMemDC, 0, 0, uintptr(width), uintptr(height),
		hScreen, uintptr(bounds.Min.X), uintptr(bounds.Min.Y),
		uintptr(srccopy),
	)
	if ret == 0 {
		return nil, fmt.Errorf("BitBlt failed")
	}

	// Prepare a BITMAPINFO for GetDIBits.  Negative height requests top-down
	// row order so that (0,0) is the top-left corner.
	bi := bitmapInfo{}
	bi.Header.Size = uint32(unsafe.Sizeof(bi.Header))
	bi.Header.Width = int32(width)
	bi.Header.Height = -int32(height)
	bi.Header.Planes = 1
	bi.Header.BitCount = 32 // 32-bit BGRA
	bi.Header.Compression = biRGB

	// Allocate a pixel buffer and copy the bitmap bits.
	pix := make([]byte, width*height*4)
	ret, _, _ = procGetDIBits.Call(
		hScreen, hBitmap,
		0, uintptr(height),
		uintptr(unsafe.Pointer(&pix[0])),
		uintptr(unsafe.Pointer(&bi)),
		uintptr(dibRGBColors),
	)
	if ret == 0 {
		return nil, fmt.Errorf("GetDIBits failed")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}

	// GDI returns pixels in BGRA order; convert to Go's RGBA order.
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for i := 0; i < len(pix); i += 4 {
		img.Pix[i+0] = pix[i+2] // R ← B
		img.Pix[i+1] = pix[i+1] // G
		img.Pix[i+2] = pix[i+0] // B ← R
		img.Pix[i+3] = 0xFF     // A = fully opaque
	}
	return img, nil
}
