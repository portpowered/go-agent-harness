package tools

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"io"
	"strings"
	"testing"
	"time"
)

type recordingTestSurface struct {
	frame       *image.RGBA
	captureFunc func(context.Context) (*image.RGBA, error)
	probes      int
	boundCalls  int
	captures    int
}

func (s *recordingTestSurface) Probe(ctx context.Context) (DisplayCapability, error) {
	s.probes++
	if err := ctx.Err(); err != nil {
		return DisplayCapability{}, err
	}
	return UsableDisplayCapability(1), nil
}

func (s *recordingTestSurface) DisplayCount(context.Context) (int, error) { return 1, nil }

func (s *recordingTestSurface) Bounds(ctx context.Context, _ int) (image.Rectangle, error) {
	s.boundCalls++
	if err := ctx.Err(); err != nil {
		return image.Rectangle{}, err
	}
	return s.frame.Bounds(), nil
}

func (s *recordingTestSurface) Capture(ctx context.Context, _ image.Rectangle) (*image.RGBA, error) {
	s.captures++
	if s.captureFunc != nil {
		return s.captureFunc(ctx)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.frame, nil
}

func recordingTestFrame() *image.RGBA {
	frame := image.NewRGBA(image.Rect(0, 0, 4, 3))
	frame.Set(0, 0, color.RGBA{R: 255, A: 255})
	frame.Set(1, 1, color.RGBA{G: 255, A: 255})
	frame.Set(2, 2, color.RGBA{B: 255, A: 255})
	return frame
}

func TestScreenRecordingLimitsAndDefaults(t *testing.T) {
	tool := NewScreenToolWithDisplaySurface(&recordingTestSurface{frame: recordingTestFrame()})
	params := tool.Parameters()["properties"].(map[string]any)
	duration := params["duration"].(map[string]any)
	fps := params["fps"].(map[string]any)
	if duration["maximum"] != maxScreenRecordingDurationSeconds || fps["maximum"] != maxScreenRecordingFPS {
		t.Fatalf("recording schema limits = duration:%v fps:%v, want duration:%v fps:%v", duration["maximum"], fps["maximum"], maxScreenRecordingDurationSeconds, maxScreenRecordingFPS)
	}

	defaults, err := parseScreenRecordingOptions(nil)
	if err != nil {
		t.Fatalf("default recording options: %v", err)
	}
	if defaults.durationSeconds != defaultScreenRecordingDurationSeconds || defaults.fps != defaultScreenRecordingFPS || defaults.maxFrames != 6 {
		t.Fatalf("default recording options = %#v", defaults)
	}
	maximum, err := newScreenRecordingOptions(maxScreenRecordingDurationSeconds, maxScreenRecordingFPS)
	if err != nil {
		t.Fatalf("maximum recording options: %v", err)
	}
	if maximum.maxFrames != 10 || maximum.frameInterval != 500*time.Millisecond || maximum.delayCS != 50 {
		t.Fatalf("maximum recording options = %#v", maximum)
	}
}

func TestScreenRecordingRejectsOutOfRangeValuesBeforeDisplayProbe(t *testing.T) {
	for _, test := range []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "duration above cap", args: map[string]any{"action": "record", "duration": 5.01}, want: "duration"},
		{name: "duration below minimum", args: map[string]any{"action": "record", "duration": 0.5}, want: "duration"},
		{name: "fps above cap", args: map[string]any{"action": "record", "fps": 2.01}, want: "fps"},
		{name: "fps below minimum", args: map[string]any{"action": "record", "fps": 0.5}, want: "fps"},
		{name: "duration wrong type", args: map[string]any{"action": "record", "duration": "5s"}, want: "duration"},
	} {
		t.Run(test.name, func(t *testing.T) {
			surface := &recordingTestSurface{frame: recordingTestFrame()}
			msgs, err := NewScreenToolWithDisplaySurface(surface).Execute(context.Background(), test.args)
			var validationErr *ScreenRecordingValidationError
			if msgs != nil || err == nil || !errors.As(err, &validationErr) || !errors.Is(err, ErrInvalidScreenRecording) {
				t.Fatalf("validation result = %#v, err = %v", msgs, err)
			}
			if validationErr.Field != test.want || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %#v / %q, want field %q", validationErr, err, test.want)
			}
			if surface.probes != 0 || surface.boundCalls != 0 || surface.captures != 0 {
				t.Fatalf("invalid request caused display side effects: probes:%d bounds:%d captures:%d", surface.probes, surface.boundCalls, surface.captures)
			}
		})
	}
}

func TestScreenRecordingSuccessIsBoundedAndDecodable(t *testing.T) {
	surface := &recordingTestSurface{frame: recordingTestFrame()}
	msgs, err := NewScreenToolWithDisplaySurface(surface).Execute(context.Background(), map[string]any{
		"action":   "record",
		"duration": 1.0,
		"fps":      2.0,
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if len(msgs) != 1 || len(msgs[0].ContentParts) != 2 {
		t.Fatalf("record result = %#v, want one text and one image", msgs)
	}
	part := assertScreenResult(t, msgs[0], "image/gif", 4, 3)
	decoded, err := gif.DecodeAll(bytes.NewReader(part.Bytes))
	if err != nil {
		t.Fatalf("decode recording: %v", err)
	}
	if len(decoded.Image) != 2 || len(decoded.Delay) != 2 {
		t.Fatalf("decoded recording frames/delays = %d/%d, want 2/2", len(decoded.Image), len(decoded.Delay))
	}
	if surface.probes != 1 || surface.boundCalls != 1 || surface.captures != 2 {
		t.Fatalf("record surface calls = probes:%d bounds:%d captures:%d", surface.probes, surface.boundCalls, surface.captures)
	}
}

func TestScreenRecordingCancellationDuringCaptureIsClassified(t *testing.T) {
	captureStarted := make(chan struct{})
	surface := &recordingTestSurface{
		frame: recordingTestFrame(),
		captureFunc: func(ctx context.Context) (*image.RGBA, error) {
			select {
			case <-captureStarted:
			default:
				close(captureStarted)
			}
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := NewScreenToolWithDisplaySurface(surface).Execute(ctx, map[string]any{"action": "record", "duration": 1.0, "fps": 2.0})
		result <- err
	}()
	select {
	case <-captureStarted:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("recording did not start capture")
	}
	err := <-result
	var captureErr *ScreenCaptureError
	if err == nil || !errors.As(err, &captureErr) || captureErr.State != ScreenCaptureCanceled || !errors.Is(err, context.Canceled) {
		t.Fatalf("capture cancellation = %v, want classified context cancellation", err)
	}
	if surface.captures != 1 {
		t.Fatalf("capture calls after cancellation = %d, want 1", surface.captures)
	}
}

func TestScreenRecordingCancellationDuringEncodingIsClassified(t *testing.T) {
	encodeStarted := make(chan struct{})
	encoder := ScreenRecordingEncoderFunc(func(ctx context.Context, _ io.Writer, recording *gif.GIF) error {
		if len(recording.Image) != 1 {
			return errors.New("unexpected frame count")
		}
		close(encodeStarted)
		<-ctx.Done()
		return ctx.Err()
	})
	surface := &recordingTestSurface{frame: recordingTestFrame()}
	tool := NewScreenToolWithOptions(ScreenToolOptions{DisplaySurface: surface, RecordingEncoder: encoder})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := tool.Execute(ctx, map[string]any{"action": "record", "duration": 1.0, "fps": 1.0})
		result <- err
	}()
	select {
	case <-encodeStarted:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("recording did not start encoding")
	}
	err := <-result
	var captureErr *ScreenCaptureError
	if err == nil || !errors.As(err, &captureErr) || captureErr.State != ScreenCaptureCanceled || !errors.Is(err, context.Canceled) {
		t.Fatalf("encoding cancellation = %v, want classified context cancellation", err)
	}
	if surface.captures != 1 {
		t.Fatalf("capture calls during encoding cancellation = %d, want 1", surface.captures)
	}
}

func TestScreenRecordingSlowCaptureHonorsDeadlineWithoutPartialSuccess(t *testing.T) {
	captureStarted := make(chan struct{})
	surface := &recordingTestSurface{
		frame: recordingTestFrame(),
		captureFunc: func(ctx context.Context) (*image.RGBA, error) {
			select {
			case <-captureStarted:
			default:
				close(captureStarted)
			}
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	msgs, err := NewScreenToolWithDisplaySurface(surface).Execute(ctx, map[string]any{"action": "record", "duration": 1.0, "fps": 1.0})
	if msgs != nil {
		t.Fatalf("slow capture returned partial messages: %#v", msgs)
	}
	var captureErr *ScreenCaptureError
	if err == nil || !errors.As(err, &captureErr) || captureErr.State != ScreenCaptureTimedOut || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow capture deadline = %v, want classified timeout", err)
	}
	select {
	case <-captureStarted:
	default:
		t.Fatal("slow capture did not start")
	}
}
