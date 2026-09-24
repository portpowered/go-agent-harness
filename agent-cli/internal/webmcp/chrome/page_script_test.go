package chrome

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/png"
	"reflect"
	"sync"
	"testing"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

func (s *targetSession) installPageScript(ctx context.Context, source string) error {
	if source == "" {
		return errors.New("site adapter source is empty")
	}
	return s.run(ctx, pageScriptAction(source))
}

type focusExecutor struct {
	mu             sync.Mutex
	enabled        []bool
	failNextEnable bool
}

func (e *focusExecutor) Execute(ctx context.Context, method string, params, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if method != emulation.CommandSetFocusEmulationEnabled {
		return errors.New("unexpected CDP method")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	focus, ok := params.(*emulation.SetFocusEmulationEnabledParams)
	if !ok {
		return errors.New("unexpected focus parameters")
	}
	e.enabled = append(e.enabled, focus.Enabled)
	if e.failNextEnable && focus.Enabled {
		e.failNextEnable = false
		return errors.New("focus response lost after enabling")
	}
	return nil
}

func TestTargetFocusFailedAcquisitionCleanupPreservesLaterLease(t *testing.T) {
	e := &focusExecutor{failNextEnable: true}
	s := newInvocationTestSession(t, e)
	failedCleanup, err := s.AcquirePageFocus(context.Background())
	if err == nil || failedCleanup == nil {
		t.Fatal("expected failed acquisition with cleanup")
	}
	release, err := s.AcquirePageFocus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := failedCleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.enabled, []bool{true, true}) {
		t.Fatalf("failed cleanup disabled active lease: %v", e.enabled)
	}
	if err := release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := failedCleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.enabled, []bool{true, true, false}) {
		t.Fatalf("calls=%v", e.enabled)
	}
}

func TestTargetFocusFailedAcquisitionCleanupRestoresOnce(t *testing.T) {
	e := &focusExecutor{failNextEnable: true}
	s := newInvocationTestSession(t, e)
	cleanup, err := s.AcquirePageFocus(context.Background())
	if err == nil || cleanup == nil {
		t.Fatal("expected failed acquisition with cleanup")
	}
	for range 2 {
		if err := cleanup(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(e.enabled, []bool{true, false}) {
		t.Fatalf("calls=%v", e.enabled)
	}
}
func TestTargetFocusLeaseRestoresOnceAfterOperationCancellation(t *testing.T) {
	e := &focusExecutor{}
	s := newInvocationTestSession(t, e)
	ctx, cancel := context.WithCancel(context.Background())
	release, err := s.AcquirePageFocus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.enabled, []bool{true, false}) {
		t.Fatalf("calls=%v", e.enabled)
	}
}
func TestTargetFocusOverlappingLeasesRestoreAfterLastRelease(t *testing.T) {
	e := &focusExecutor{}
	s := newInvocationTestSession(t, e)
	first, err := s.AcquirePageFocus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.AcquirePageFocus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := first(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.enabled, []bool{true}) {
		t.Fatalf("premature release=%v", e.enabled)
	}
	if err := second(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.enabled, []bool{true, false}) {
		t.Fatalf("calls=%v", e.enabled)
	}
}

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
