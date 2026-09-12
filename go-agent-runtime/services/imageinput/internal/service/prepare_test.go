package service

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/imageinput"
)

func TestPrepareRejectsCapabilityBeforeLoading(t *testing.T) {
	loader := &contentLoader{}
	service := New(loader)
	_, err := service.Prepare(context.Background(), []string{"input.png"}, imageinput.Capabilities{Model: "text-only"})
	if !errors.Is(err, imageinput.ErrCapability) {
		t.Fatalf("error = %v, want capability identity", err)
	}
	var typed *imageinput.CapabilityError
	if !errors.As(err, &typed) || typed.Model != "text-only" {
		t.Fatalf("error = %T, want capability details", err)
	}
	if got := loader.callCount(); got != 0 {
		t.Fatalf("loader calls = %d, want zero on capability rejection", got)
	}
}

func TestPrepareNormalizesAndClonesImageInput(t *testing.T) {
	data := testPNG(t)
	loader := &contentLoader{parts: map[string]messages.ContentPart{
		"input.png": messages.ImagePart{Bytes: data, MediaType: " IMAGE/PNG "},
	}}
	service := New(loader)
	supported := []string{" IMAGE/PNG ", "image/png", "IMAGE/JPEG"}
	parts, err := service.Prepare(context.Background(), []string{"input.png"}, imageinput.Capabilities{
		Model:                   "vision",
		SupportsImageInput:      true,
		SupportedInputMIMETypes: supported,
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(parts) != 1 || parts[0].MediaType != "image/png" {
		t.Fatalf("parts = %#v, want one normalized PNG", parts)
	}
	if string(parts[0].Bytes) != string(data) {
		t.Fatal("prepared bytes differ from loader bytes")
	}
	parts[0].Bytes[0] ^= 0xff
	if parts[0].Bytes[0] == data[0] {
		t.Fatal("prepared bytes were not independently owned")
	}
	if supported[0] != " IMAGE/PNG " {
		t.Fatal("supported MIME input was mutated")
	}
}

func TestPrepareAcceptsJPEGAndCanonicalizesMIME(t *testing.T) {
	loader := &contentLoader{parts: map[string]messages.ContentPart{
		"input.jpeg": messages.ImagePart{Bytes: testJPEG(t), MediaType: " IMAGE/JPEG "},
	}}
	parts, err := New(loader).Prepare(context.Background(), []string{"input.jpeg"}, imageinput.Capabilities{
		SupportsImageInput:      true,
		SupportedInputMIMETypes: []string{"image/png", " IMAGE/JPEG "},
	})
	if err != nil {
		t.Fatalf("Prepare JPEG: %v", err)
	}
	if len(parts) != 1 || parts[0].MediaType != "image/jpeg" || len(parts[0].Bytes) == 0 {
		t.Fatalf("JPEG parts = %#v, want one canonical non-empty JPEG", parts)
	}
}

func TestPrepareRejectsMIMEThatDoesNotMatchDecodedImage(t *testing.T) {
	loader := &contentLoader{parts: map[string]messages.ContentPart{
		"mismatch": messages.ImagePart{Bytes: testJPEG(t), MediaType: "image/png"},
	}}
	_, err := New(loader).Prepare(context.Background(), []string{"mismatch"}, imageinput.Capabilities{
		SupportsImageInput:      true,
		SupportedInputMIMETypes: []string{"image/png", "image/jpeg"},
	})
	if !errors.Is(err, imageinput.ErrInvalidContent) {
		t.Fatalf("mismatch error = %v, want invalid-content identity", err)
	}
	var typed *imageinput.InvalidContentError
	if !errors.As(err, &typed) || typed.DetectedMIME != "image/png" {
		t.Fatalf("mismatch error = %#v, want typed PNG declaration", err)
	}
}

func TestPrepareReturnsTypedCausalFailures(t *testing.T) {
	decodeCause := errors.New("decoder cause")
	loader := &contentLoader{
		parts: map[string]messages.ContentPart{
			"unsupported": messages.FilePart{Bytes: []byte("file"), MediaType: "text/plain"},
			"invalid":     messages.ImagePart{Bytes: []byte("not an image"), MediaType: "image/png"},
			"empty":       messages.ImagePart{MediaType: "image/png"},
		},
		errs: map[string]error{
			"missing":    os.ErrNotExist,
			"unreadable": decodeCause,
		},
	}
	service := New(loader)
	cases := []struct {
		name  string
		path  string
		kind  imageinput.ErrorKind
		as    any
		cause error
	}{
		{name: "missing", path: "missing", kind: imageinput.ErrMissingFile, as: (*imageinput.MissingFileError)(nil), cause: os.ErrNotExist},
		{name: "unreadable", path: "unreadable", kind: imageinput.ErrUnreadableFile, as: (*imageinput.UnreadableFileError)(nil), cause: decodeCause},
		{name: "unsupported", path: "unsupported", kind: imageinput.ErrUnsupportedMIME, as: (*imageinput.UnsupportedMIMEError)(nil)},
		{name: "invalid", path: "invalid", kind: imageinput.ErrInvalidContent, as: (*imageinput.InvalidContentError)(nil)},
		{name: "empty", path: "empty", kind: imageinput.ErrEmptyFile, as: (*imageinput.EmptyFileError)(nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.Prepare(context.Background(), []string{tc.path}, imageinput.Capabilities{SupportsImageInput: true})
			if !errors.Is(err, tc.kind) {
				t.Fatalf("error = %v, want %s", err, tc.kind)
			}
			if tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Fatalf("error = %v, want cause %v", err, tc.cause)
			}
			if !matchesImageError(err, tc.as) {
				t.Fatalf("error = %T, want %T", err, tc.as)
			}
		})
	}
}

func TestPreparePreservesCancellationIdentity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	loader := &contentLoader{parts: map[string]messages.ContentPart{
		"input.png": messages.ImagePart{Bytes: testPNG(t), MediaType: "image/png"},
	}}
	_, err := New(loader).Prepare(ctx, []string{"input.png"}, imageinput.Capabilities{SupportsImageInput: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if loader.callCount() != 0 {
		t.Fatal("cancelled preparation called the loader")
	}
}

func matchesImageError(err error, target any) bool {
	switch target.(type) {
	case *imageinput.MissingFileError:
		var typed *imageinput.MissingFileError
		return errors.As(err, &typed)
	case *imageinput.UnreadableFileError:
		var typed *imageinput.UnreadableFileError
		return errors.As(err, &typed)
	case *imageinput.UnsupportedMIMEError:
		var typed *imageinput.UnsupportedMIMEError
		return errors.As(err, &typed)
	case *imageinput.InvalidContentError:
		var typed *imageinput.InvalidContentError
		return errors.As(err, &typed)
	case *imageinput.EmptyFileError:
		var typed *imageinput.EmptyFileError
		return errors.As(err, &typed)
	default:
		return false
	}
}
