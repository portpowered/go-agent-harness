package tools

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/sight"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type scriptedDisplaySurface struct {
	capability DisplayCapability
	probeErr   error
	bounds     image.Rectangle
	image      *image.RGBA
	boundErr   error
	captureErr error
	probes     int
	boundsN    int
	captures   int
	deadline   bool
}

type screenPermissionRecheckSurface struct {
	*scriptedDisplaySurface
	permission DisplayPermission
	rechecks   int
}

func (s *screenPermissionRecheckSurface) ScreenRecordingPermissionRecheckSupported() bool {
	return true
}

func (s *screenPermissionRecheckSurface) RecheckScreenRecordingPermission(context.Context) (DisplayPermission, error) {
	s.rechecks++
	return s.permission, nil
}

func (s *scriptedDisplaySurface) Probe(ctx context.Context) (DisplayCapability, error) {
	s.probes++
	_, s.deadline = ctx.Deadline()
	return s.capability, s.probeErr
}

func (s *scriptedDisplaySurface) DisplayCount(context.Context) (int, error) {
	return s.capability.DisplayCount, nil
}

func (s *scriptedDisplaySurface) Bounds(ctx context.Context, _ int) (image.Rectangle, error) {
	s.boundsN++
	if err := ctx.Err(); err != nil {
		return image.Rectangle{}, err
	}
	return s.bounds, s.boundErr
}

func (s *scriptedDisplaySurface) Capture(ctx context.Context, _ image.Rectangle) (*image.RGBA, error) {
	s.captures++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.image, s.captureErr
}

func TestScreenToolWithDisplaySurfaceCapturesOneDecodableImage(t *testing.T) {
	imagePixels := image.NewRGBA(image.Rect(0, 0, 3, 2))
	imagePixels.Set(0, 0, color.RGBA{R: 255, A: 255})
	surface := &scriptedDisplaySurface{
		capability: UsableDisplayCapability(1),
		bounds:     imagePixels.Bounds(),
		image:      imagePixels,
	}

	msgs, err := NewScreenToolWithDisplaySurface(surface).Execute(context.Background(), map[string]any{"action": "screenshot"})
	if err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if len(msgs) != 1 || len(msgs[0].ContentParts) != 2 {
		t.Fatalf("screenshot result = %#v, want one text and one image part", msgs)
	}
	part, ok := msgs[0].ContentParts[1].(messages.ImagePart)
	if !ok || part.MediaType != "image/jpeg" || len(part.Bytes) == 0 {
		t.Fatalf("screenshot image part = %#v", msgs[0].ContentParts[1])
	}
	decoded, err := jpeg.Decode(bytes.NewReader(part.Bytes))
	if err != nil || decoded.Bounds().Dx() != 3 || decoded.Bounds().Dy() != 2 {
		t.Fatalf("decoded screenshot = %v, err = %v", decoded.Bounds(), err)
	}
	if surface.probes != 1 || surface.boundsN != 1 || surface.captures != 1 || !surface.deadline {
		t.Fatalf("surface calls = probes:%d bounds:%d captures:%d deadline:%v", surface.probes, surface.boundsN, surface.captures, surface.deadline)
	}
}

func TestScreenToolPermissionDenialIsTypedAndActionable(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "iTerm.app")
	surface := &scriptedDisplaySurface{
		capability: DisplayCapability{State: ScreenCaptureDenied, Reason: "macOS Screen Recording access was denied"},
	}

	msgs, err := NewScreenToolWithDisplaySurface(surface).Execute(context.Background(), map[string]any{"action": "screenshot"})
	var captureErr *ScreenCaptureError
	if err == nil || !errors.As(err, &captureErr) || captureErr.State != ScreenCaptureDenied || msgs != nil {
		t.Fatalf("denial result = %#v, err = %v", msgs, err)
	}
	if !errors.Is(err, ErrScreenRecordingPermissionDenied) {
		t.Fatalf("denial error identity = %v", err)
	}
	for _, want := range []string{
		"Screen-recording permission is not granted",
		"System Settings → Privacy & Security → Screen & System Audio Recording",
		"iTerm2",
		"completely quit and restart",
		"macOS Sequoia",
		"monthly re-confirmation",
		"asking again",
		"Tell the customer",
		"cannot grant",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("denial error %q does not contain %q", err, want)
		}
	}
	if surface.captures != 0 {
		t.Fatalf("permission denial captured %d images", surface.captures)
	}
}

func TestScreenToolUnavailableAndCommandFailureNeverReturnPixels(t *testing.T) {
	tests := []struct {
		name       string
		capability DisplayCapability
		bounds     image.Rectangle
		captureErr error
		state      ScreenCaptureState
		errIs      error
	}{
		{
			name:       "unavailable display",
			capability: UnavailableDisplayCapability("no usable display was discovered"),
			state:      ScreenCaptureUnavailable,
			errIs:      ErrDisplayUnavailable,
		},
		{
			name:       "command failure",
			capability: UsableDisplayCapability(1),
			bounds:     image.Rect(0, 0, 1, 1),
			captureErr: errors.New("screencapture exited with status 7"),
			state:      ScreenCaptureFailed,
			errIs:      ErrScreenCaptureFailed,
		},
		{
			name:       "capture permission failure",
			capability: UsableDisplayCapability(1),
			bounds:     image.Rect(0, 0, 1, 1),
			captureErr: &ScreenRecordingPermissionError{Detail: "operation not permitted"},
			state:      ScreenCaptureDenied,
			errIs:      ErrScreenRecordingPermissionDenied,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			surface := &scriptedDisplaySurface{
				capability: tt.capability,
				bounds:     tt.bounds,
				image:      image.NewRGBA(image.Rect(0, 0, 1, 1)),
				captureErr: tt.captureErr,
			}
			msgs, err := NewScreenToolWithDisplaySurface(surface).Execute(context.Background(), map[string]any{"action": "screenshot"})
			var captureErr *ScreenCaptureError
			if err == nil || !errors.As(err, &captureErr) || captureErr.State != tt.state || msgs != nil {
				t.Fatalf("failure result = %#v, err = %v", msgs, err)
			}
			if !errors.Is(err, tt.errIs) {
				t.Fatalf("failure identity = %v, want %v", err, tt.errIs)
			}
		})
	}
}

func TestScreenToolCanceledAndTimedOutContextsAreClassified(t *testing.T) {
	for _, tt := range []struct {
		name  string
		ctx   context.Context
		state ScreenCaptureState
		want  error
	}{
		{name: "canceled", ctx: canceledContext(), state: ScreenCaptureCanceled, want: context.Canceled},
		{name: "timed out", ctx: expiredContext(), state: ScreenCaptureTimedOut, want: context.DeadlineExceeded},
	} {
		t.Run(tt.name, func(t *testing.T) {
			surface := &scriptedDisplaySurface{capability: UsableDisplayCapability(1)}
			msgs, err := NewScreenToolWithDisplaySurface(surface).Execute(tt.ctx, map[string]any{"action": "screenshot"})
			var captureErr *ScreenCaptureError
			if err == nil || !errors.As(err, &captureErr) || captureErr.State != tt.state || msgs != nil {
				t.Fatalf("context failure result = %#v, err = %v", msgs, err)
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("context failure identity = %v, want %v", err, tt.want)
			}
			if surface.probes != 0 || surface.captures != 0 {
				t.Fatalf("context failure invoked surface: probes=%d captures=%d", surface.probes, surface.captures)
			}
		})
	}
}

func TestScreenToolCaptureDeadlineIsClassified(t *testing.T) {
	surface := &scriptedDisplaySurface{
		capability: UsableDisplayCapability(1),
		bounds:     image.Rect(0, 0, 1, 1),
		image:      image.NewRGBA(image.Rect(0, 0, 1, 1)),
		captureErr: context.DeadlineExceeded,
	}
	msgs, err := NewScreenToolWithDisplaySurface(surface).Execute(context.Background(), map[string]any{"action": "screenshot"})
	var captureErr *ScreenCaptureError
	if err == nil || !errors.As(err, &captureErr) || captureErr.State != ScreenCaptureTimedOut || msgs != nil {
		t.Fatalf("deadline capture result = %#v, err = %v", msgs, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) || surface.captures != 1 {
		t.Fatalf("deadline capture identity/calls = %v/%d", err, surface.captures)
	}
}

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func expiredContext() context.Context {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	cancel()
	return ctx
}

func TestDisplayPermissionCheckerStopsProbeBeforeDisplaySideEffects(t *testing.T) {
	var processCalls int
	permission := DisplayPermissionCheckerFunc(func(ctx context.Context) (DisplayPermission, error) {
		if ctx == nil {
			t.Fatal("permission checker received nil context")
		}
		return DisplayPermission{State: DisplayPermissionDenied, Reason: "permission test denial"}, nil
	})
	process := DisplayProcessAdapter{
		RunFunc: func(context.Context, string, ...string) ([]byte, error) {
			processCalls++
			return nil, nil
		},
		LookPathFunc: func(string) (string, error) {
			processCalls++
			return "capture", nil
		},
	}
	surface := NewHostDisplaySurfaceWithOptions(HostDisplaySurfaceOptions{
		Process:           process,
		PermissionChecker: permission,
	})
	capability, err := surface.Probe(context.Background())
	var captureErr *ScreenCaptureError
	if err == nil || !errors.As(err, &captureErr) || captureErr.State != ScreenCaptureDenied {
		t.Fatalf("permission probe = %#v, err = %v", capability, err)
	}
	if capability.State != ScreenCaptureDenied || capability.Reason != "permission test denial" || processCalls != 0 {
		t.Fatalf("permission probe result = %#v, process calls = %d", capability, processCalls)
	}
}

func TestScreenPermissionRecheckPropagatesThroughRegistryAndComposition(t *testing.T) {
	surface := &screenPermissionRecheckSurface{
		scriptedDisplaySurface: &scriptedDisplaySurface{capability: UsableDisplayCapability(1)},
		permission:             DisplayPermission{State: DisplayPermissionDenied, Reason: "recheck denied"},
	}
	tool := NewScreenToolWithDisplaySurface(surface)
	registry := NewEmptyToolRegistry()
	if err := registry.Register(tool); err != nil {
		t.Fatalf("register screen tool: %v", err)
	}
	executor := NewRegistryExecutor(registry)
	rechecker, ok := any(executor).(ScreenRecordingPermissionRechecker)
	if !ok || !rechecker.ScreenRecordingPermissionRecheckSupported() {
		t.Fatalf("registry executor does not expose screen permission recheck: %T", executor)
	}
	permission, err := rechecker.RecheckScreenRecordingPermission(context.Background())
	if err != nil || permission.State != DisplayPermissionDenied {
		t.Fatalf("registry recheck = %#v, %v, want denied permission", permission, err)
	}

	composed, err := ComposeToolSurface(
		executor,
		[]messages.ToolDefinition{{Name: ScreenToolID}},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("compose screen surface: %v", err)
	}
	composedRechecker, ok := composed.Executor.(ScreenRecordingPermissionRechecker)
	if !ok || !composedRechecker.ScreenRecordingPermissionRecheckSupported() {
		t.Fatalf("composed executor does not expose screen permission recheck: %T", composed.Executor)
	}
	permission, err = composedRechecker.RecheckScreenRecordingPermission(context.Background())
	if err != nil || permission.State != DisplayPermissionDenied {
		t.Fatalf("composed recheck = %#v, %v, want denied permission", permission, err)
	}
	if surface.rechecks != 2 {
		t.Fatalf("permission checker calls = %d, want two calls through the same surface contract", surface.rechecks)
	}
}

func TestDeniedScreenPreflightStopsScreenshotAndRecordingSideEffects(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "iTerm.app")
	for _, action := range []string{"screenshot", "record"} {
		t.Run(action, func(t *testing.T) {
			permissionCalls := 0
			processCalls := 0
			captureCalls := 0
			encodeCalls := 0
			surface := NewHostDisplaySurfaceWithOptions(HostDisplaySurfaceOptions{
				Process: DisplayProcessAdapter{
					RunFunc: func(context.Context, string, ...string) ([]byte, error) {
						processCalls++
						return nil, nil
					},
					LookPathFunc: func(string) (string, error) {
						processCalls++
						return "capture", nil
					},
				},
				PermissionChecker: DisplayPermissionCheckerFunc(func(context.Context) (DisplayPermission, error) {
					permissionCalls++
					return DisplayPermission{State: DisplayPermissionDenied, Reason: "preflight denied"}, nil
				}),
				Capturer: DisplayCapturerFunc(func(context.Context, int, image.Rectangle) (*image.RGBA, error) {
					captureCalls++
					return image.NewRGBA(image.Rect(0, 0, 1, 1)), nil
				}),
			})
			tool := NewScreenToolWithOptions(ScreenToolOptions{
				DisplaySurface: surface,
				RecordingEncoder: ScreenRecordingEncoderFunc(func(context.Context, io.Writer, *gif.GIF) error {
					encodeCalls++
					return nil
				}),
			})

			args := map[string]any{"action": action}
			if action == "record" {
				args["duration"] = 1.0
				args["fps"] = 1.0
			}
			messages, err := tool.Execute(context.Background(), args)
			var captureErr *ScreenCaptureError
			if err == nil || !errors.As(err, &captureErr) || captureErr.State != ScreenCaptureDenied || messages != nil {
				t.Fatalf("%s denial = %#v, err = %v", action, messages, err)
			}
			if permissionCalls != 1 || processCalls != 0 || captureCalls != 0 || encodeCalls != 0 {
				t.Fatalf("%s side effects = permission:%d process:%d capture:%d encode:%d", action, permissionCalls, processCalls, captureCalls, encodeCalls)
			}

			result, decodeErr := sight.Decode([]byte(ScreenToolErrorResult(err)))
			if decodeErr != nil {
				t.Fatalf("decode %s denial envelope: %v", action, decodeErr)
			}
			if result.Version != sight.ResultVersion || result.Status != sight.StatusError || result.Source != sight.SourceScreen || result.ErrorCode != ScreenRecordingPermissionDeniedErrorCode || result.MIMEType != "" || result.TypedProjection != "" {
				t.Fatalf("%s denial envelope = %+v, want version 2 text-only permission denial", action, result)
			}
			for _, want := range []string{
				"System Settings → Privacy & Security → Screen & System Audio Recording",
				"hosting application",
				"completely quit and restart",
				"macOS Sequoia",
				"monthly re-confirmation",
			} {
				if !strings.Contains(result.Error, want) {
					t.Errorf("%s denial error %q does not contain %q", action, result.Error, want)
				}
			}
		})
	}
}

func TestDisplaySurfaceProbeUsesContextAndDoesNotCapture(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("display admission process seam is covered only on command-based platforms")
	}
	var calls []string
	process := DisplayProcessAdapter{
		RunFunc: func(ctx context.Context, name string, _ ...string) ([]byte, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			calls = append(calls, name)
			switch name {
			case "system_profiler":
				return []byte("Resolution: 16 x 10\n"), nil
			case "xrandr":
				return []byte("Monitors: 1\n"), nil
			case "xdotool":
				if runtime.GOOS == "linux" {
					return []byte("8 6\n"), nil
				}
				return nil, errors.New("unexpected process")
			default:
				return nil, errors.New("unexpected process")
			}
		},
		LookPathFunc: func(name string) (string, error) {
			calls = append(calls, "lookpath:"+name)
			return name, nil
		},
	}
	capability, err := NewHostDisplaySurface(process).Probe(context.Background())
	if err != nil || !capability.Usable() {
		t.Fatalf("display probe = %#v, err = %v", capability, err)
	}
	for _, call := range calls {
		if call == "screencapture" || call == "scrot" {
			t.Fatalf("probe attempted image capture through %q", call)
		}
	}
}

func TestShowRemainsAdvertisedWhenDisplayIsUnavailable(t *testing.T) {
	registry := NewToolRegistry()
	if _, ok := registry.Get("show"); !ok {
		t.Fatal("show is not present in the static tool surface")
	}
}
