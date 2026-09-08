package browser

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"

	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

func TestValidatePageScreenshotAcceptsAndCopiesValidPNG(t *testing.T) {
	raw := validPNG(t, 2, 3)
	screenshot := public.PageScreenshot{
		BrowserID: "browser-a",
		TargetID:  "target-a",
		MIMEType:  "IMAGE/PNG; charset=binary",
		Bytes:     raw,
	}

	validated, err := ValidatePageScreenshot(screenshot)
	if err != nil {
		t.Fatalf("ValidatePageScreenshot: %v", err)
	}
	if validated.BrowserID != screenshot.BrowserID || validated.TargetID != screenshot.TargetID || validated.MIMEType != "image/png" || validated.Width != 2 || validated.Height != 3 {
		t.Fatalf("validated capture = %+v", validated)
	}
	if !bytes.Equal(validated.Bytes, raw) {
		t.Fatalf("validated bytes differ from input")
	}
	raw[0] ^= 0xff
	if bytes.Equal(validated.Bytes, raw) {
		t.Fatal("validated bytes alias broker-owned input")
	}
}

func TestValidatePageScreenshotRejectsInvalidCaptureWithBoundedReason(t *testing.T) {
	valid := validPNG(t, 1, 1)
	cases := []struct {
		name       string
		screenshot public.PageScreenshot
		reason     string
	}{
		{name: "missing identity", screenshot: public.PageScreenshot{MIMEType: "image/png", Bytes: valid}, reason: "missing_target_identity"},
		{name: "empty capture", screenshot: public.PageScreenshot{BrowserID: "browser-a", TargetID: "target-a", MIMEType: "image/png"}, reason: "empty_capture"},
		{name: "invalid mime", screenshot: public.PageScreenshot{BrowserID: "browser-a", TargetID: "target-a", MIMEType: "image/png; bad", Bytes: valid}, reason: "invalid_mime_type"},
		{name: "unsupported mime", screenshot: public.PageScreenshot{BrowserID: "browser-a", TargetID: "target-a", MIMEType: "image/webp", Bytes: valid}, reason: "unsupported_mime_type"},
		{name: "malformed image", screenshot: public.PageScreenshot{BrowserID: "browser-a", TargetID: "target-a", MIMEType: "image/png", Bytes: []byte("not an image")}, reason: "malformed_image"},
		{name: "mime mismatch", screenshot: public.PageScreenshot{BrowserID: "browser-a", TargetID: "target-a", MIMEType: "image/jpeg", Bytes: valid}, reason: "mime_mismatch"},
		{name: "dimension mismatch", screenshot: public.PageScreenshot{BrowserID: "browser-a", TargetID: "target-a", MIMEType: "image/png", Bytes: valid, Width: 2}, reason: "dimension_mismatch"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := ValidatePageScreenshot(testCase.screenshot)
			if err == nil {
				t.Fatal("ValidatePageScreenshot succeeded for invalid capture")
			}
			var classified *public.ClassifiedError
			if !errors.As(err, &classified) || classified == nil {
				t.Fatalf("error = %T %v, want classified error", err, err)
			}
			if classified.Code != public.ErrorInvocationFailed {
				t.Fatalf("code = %q, want %q", classified.Code, public.ErrorInvocationFailed)
			}
			got, ok := classified.Details["reason_code"].(string)
			if !ok || got != testCase.reason {
				t.Fatalf("reason_code = %q, want %q", got, testCase.reason)
			}
			if _, err := json.Marshal(classified.Details); err != nil {
				t.Fatalf("details are not JSON-safe: %v", err)
			}
		})
	}
}

func validPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	imageValue := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			imageValue.Set(x, y, color.RGBA{R: uint8(x + 1), G: uint8(y + 1), A: 0xff})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, imageValue); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}
	return encoded.Bytes()
}
