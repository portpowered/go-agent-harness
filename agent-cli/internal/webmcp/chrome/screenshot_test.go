package chrome

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/png"
	"sync"
	"testing"

	"github.com/chromedp/cdproto/page"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

type screenshotExecutor struct {
	mu       sync.Mutex
	methods  []string
	formats  []page.CaptureScreenshotFormat
	fromSurf []bool
	data     []byte
}

func (e *screenshotExecutor) Execute(ctx context.Context, method string, params, result any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	e.mu.Lock()
	e.methods = append(e.methods, method)
	if capture, ok := params.(*page.CaptureScreenshotParams); ok {
		e.formats = append(e.formats, capture.Format)
		e.fromSurf = append(e.fromSurf, capture.FromSurface)
	}
	data := append([]byte(nil), e.data...)
	e.mu.Unlock()
	if returns, ok := result.(*page.CaptureScreenshotReturns); ok {
		returns.Data = base64.StdEncoding.EncodeToString(data)
	}
	return nil
}

func TestTargetSessionCapturePageScreenshotUsesAttachedCDPTarget(t *testing.T) {
	executor := &screenshotExecutor{data: screenshotPNGBytes(t)}
	session := newInvocationTestSession(t, executor)

	got, err := session.CapturePageScreenshot(context.Background())
	if err != nil {
		t.Fatalf("capture screenshot: %v", err)
	}
	if got.BrowserID != "browser-invocation" || got.TargetID != "target-invocation" || got.MIMEType != "image/png" || got.Width != 2 || got.Height != 1 || !bytes.Equal(got.Bytes, executor.data) {
		t.Fatalf("capture = %+v, want attached target PNG", got)
	}
	executor.mu.Lock()
	methods := append([]string(nil), executor.methods...)
	formats := append([]page.CaptureScreenshotFormat(nil), executor.formats...)
	fromSurface := append([]bool(nil), executor.fromSurf...)
	executor.mu.Unlock()
	if len(methods) != 1 {
		t.Fatalf("CDP methods = %#v, want one Page.captureScreenshot", methods)
	}
	if methods[0] != page.CommandCaptureScreenshot {
		t.Fatalf("CDP method = %q, want Page.captureScreenshot", methods[0])
	}
	if methods[0] != webmcp.PageCaptureScreenshotMethod {
		t.Fatalf("CDP methods = %#v, want one Page.captureScreenshot", methods)
	}
	if len(formats) != 1 || formats[0] != page.CaptureScreenshotFormatPng || len(fromSurface) != 1 || !fromSurface[0] {
		t.Fatalf("capture params format=%#v fromSurface=%#v, want PNG/from-surface", formats, fromSurface)
	}
}

func TestTargetSessionCapturePageScreenshotHonorsCanceledContext(t *testing.T) {
	executor := &screenshotExecutor{data: screenshotPNGBytes(t)}
	session := newInvocationTestSession(t, executor)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := session.CapturePageScreenshot(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled capture error = %v, want context canceled", err)
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if len(executor.methods) != 0 {
		t.Fatalf("canceled capture dispatched CDP methods = %#v, want none", executor.methods)
	}
}

func screenshotPNGBytes(t *testing.T) []byte {
	t.Helper()
	imageValue := image.NewRGBA(image.Rect(0, 0, 2, 1))
	imageValue.SetRGBA(0, 0, color.RGBA{R: 0xff, A: 0xff})
	imageValue.SetRGBA(1, 0, color.RGBA{G: 0xff, A: 0xff})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, imageValue); err != nil {
		t.Fatalf("encode screenshot PNG: %v", err)
	}
	return buffer.Bytes()
}
