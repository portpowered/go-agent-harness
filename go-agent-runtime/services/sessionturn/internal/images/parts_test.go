package images

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io/fs"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

const (
	realtimeModel = "gpt-realtime"
	gifType       = "image/gif"
	fixtureSide   = 4
	validPNG      = "fixture.png"
	validJPEG     = "fixture.jpeg"
)

func encodedFixture(t *testing.T, encode func(*bytes.Buffer, image.Image) error) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, fixtureSide, fixtureSide))
	img.Set(1, 1, color.RGBA{R: 255, A: 255})
	var buffer bytes.Buffer
	if err := encode(&buffer, img); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	return buffer.Bytes()
}

func pngBytes(t *testing.T) []byte {
	return encodedFixture(t, func(b *bytes.Buffer, img image.Image) error { return png.Encode(b, img) })
}

func jpegBytes(t *testing.T) []byte {
	return encodedFixture(t, func(b *bytes.Buffer, img image.Image) error { return jpeg.Encode(b, img, nil) })
}

type loaderEntry struct {
	part messages.ContentPart
	err  error
}

func fakeLoader(entries map[string]loaderEntry) sessionturn.ImageContentLoader {
	return func(path string) (messages.ContentPart, error) {
		entry, ok := entries[path]
		if !ok {
			return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
		}
		return entry.part, entry.err
	}
}

func fixtureLoader(t *testing.T) sessionturn.ImageContentLoader {
	return fakeLoader(map[string]loaderEntry{
		validPNG:          {part: messages.ImagePart{Bytes: pngBytes(t), MediaType: mimePNG}},
		validJPEG:         {part: messages.ImagePart{Bytes: jpegBytes(t), MediaType: mimeJPEG}},
		"unreadable.png":  {err: errors.New("is a directory")},
		"unsupported.gif": {part: messages.ImagePart{Bytes: []byte("GIF89a"), MediaType: gifType}},
		"disguised.png":   {part: messages.ImagePart{Bytes: []byte("plain text, not image bytes"), MediaType: mimePNG}},
		"empty.png":       {part: messages.ImagePart{MediaType: mimePNG}},
		"notes.txt":       {part: messages.FilePart{Bytes: []byte("text"), MediaType: "text/plain"}},
		"sound.wav":       {part: messages.AudioPart{Bytes: []byte{1}, MediaType: "audio/wav"}},
		"clip.mp4":        {part: messages.VideoPart{Bytes: []byte{1}, MediaType: "video/mp4"}},
		"unknown.bin":     {part: messages.TextPart{Text: "text"}},
	})
}

func capabilities() sessionturn.ImageCapabilities {
	return sessionturn.ImageCapabilities{Model: realtimeModel, SupportsImageInput: true, SupportedInputMIMETypes: []string{mimePNG, mimeJPEG}}
}

func fileError(kind sessionturn.Error, path, detected string) func(error) bool {
	return func(err error) bool {
		var typed *sessionturn.ImageFileError
		return errors.As(err, &typed) && typed.Kind == kind && typed.Path == path && typed.DetectedMIME == detected
	}
}

func TestPreparePartsReturnsDistinctTypedErrors(t *testing.T) {
	cases := []struct {
		path string
		want error
		as   func(error) bool
	}{
		{path: "missing.png", want: sessionturn.ErrImageMissingFile, as: fileError(sessionturn.ErrImageMissingFile, "missing.png", "")},
		{path: "", want: sessionturn.ErrImageMissingFile, as: fileError(sessionturn.ErrImageMissingFile, "", "")},
		{path: "unreadable.png", want: sessionturn.ErrImageUnreadableFile, as: fileError(sessionturn.ErrImageUnreadableFile, "unreadable.png", "")},
		{path: "unsupported.gif", want: sessionturn.ErrImageUnsupportedMIME, as: fileError(sessionturn.ErrImageUnsupportedMIME, "unsupported.gif", gifType)},
		{path: "disguised.png", want: sessionturn.ErrImageInvalidContent, as: fileError(sessionturn.ErrImageInvalidContent, "disguised.png", mimePNG)},
		{path: "notes.txt", want: sessionturn.ErrImageUnsupportedMIME, as: fileError(sessionturn.ErrImageUnsupportedMIME, "notes.txt", "text/plain")},
		{path: "sound.wav", want: sessionturn.ErrImageUnsupportedMIME, as: fileError(sessionturn.ErrImageUnsupportedMIME, "sound.wav", "audio/wav")},
		{path: "clip.mp4", want: sessionturn.ErrImageUnsupportedMIME, as: fileError(sessionturn.ErrImageUnsupportedMIME, "clip.mp4", "video/mp4")},
		{path: "unknown.bin", want: sessionturn.ErrImageEmptyFile, as: func(err error) bool {
			var typed *sessionturn.ImageEmptyFileError
			return errors.As(err, &typed) && typed.Path == "unknown.bin"
		}},
		{path: "empty.png", want: sessionturn.ErrImageEmptyFile, as: func(err error) bool {
			var typed *sessionturn.ImageEmptyFileError
			return errors.As(err, &typed) && typed.Path == "empty.png"
		}},
	}
	loader := fixtureLoader(t)
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			_, err := PrepareParts(sessionturn.ImagePartsRequest{Paths: []string{validPNG, tc.path}, Capabilities: capabilities(), Load: loader})
			if !errors.Is(err, tc.want) || !tc.as(err) || err.Error() == "" {
				t.Fatalf("error = %v, want typed %v", err, tc.want)
			}
		})
	}
}

func TestPreparePartsCapabilityAndLoaderGates(t *testing.T) {
	_, err := PrepareParts(sessionturn.ImagePartsRequest{Paths: []string{validPNG}, Capabilities: sessionturn.ImageCapabilities{Model: " text-only-model "}, Load: fixtureLoader(t)})
	var capabilityErr *sessionturn.ImageCapabilityError
	if !errors.Is(err, sessionturn.ErrImageCapability) || !errors.As(err, &capabilityErr) || capabilityErr.Model != " text-only-model " || capabilityErr.Capability != sessionturn.ImageInputCapability {
		t.Fatalf("capability error = %v", err)
	}
	if _, err := PrepareParts(sessionturn.ImagePartsRequest{Paths: []string{validPNG}, Capabilities: capabilities()}); !errors.Is(err, errNoLoader) {
		t.Fatalf("missing loader = %v", err)
	}
	narrow := capabilities()
	narrow.SupportedInputMIMETypes = []string{mimePNG}
	if _, err := PrepareParts(sessionturn.ImagePartsRequest{Paths: []string{validJPEG}, Capabilities: narrow, Load: fixtureLoader(t)}); !errors.Is(err, sessionturn.ErrImageUnsupportedMIME) {
		t.Fatalf("narrow MIME set = %v", err)
	}
}

func TestPreparePartsReturnsOrderedIndependentParts(t *testing.T) {
	loader := fixtureLoader(t)
	defaults := capabilities()
	defaults.SupportedInputMIMETypes = nil
	parts, err := PrepareParts(sessionturn.ImagePartsRequest{Paths: []string{validPNG, validJPEG}, Capabilities: defaults, Load: loader})
	if err != nil || len(parts) != 2 || parts[0].MediaType != mimePNG || parts[1].MediaType != mimeJPEG {
		t.Fatalf("parts = %#v, %v", parts, err)
	}
	reloaded, err := loader(validPNG)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	loaded, ok := reloaded.(messages.ImagePart)
	if !ok || !bytes.Equal(parts[0].Bytes, loaded.Bytes) {
		t.Fatal("prepared bytes differ from the loaded image")
	}
	if got := DefaultMIMETypes(); len(got) != 2 || got[0] != mimePNG {
		t.Fatalf("default MIME types = %v", got)
	}
}
