package livehost

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

func writePNG(t *testing.T, dir string) string {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, 1, 1))
	canvas.Set(0, 0, color.White)
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, canvas); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	path := filepath.Join(dir, "pixel.png")
	if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
		t.Fatalf("write png: %v", err)
	}
	return path
}

func TestOpenImagesValidatesSessionImages(t *testing.T) {
	dir := t.TempDir()
	valid := writePNG(t, dir)
	parts, err := OpenImages([]string{valid})
	if err != nil || len(parts) != 1 {
		t.Fatalf("OpenImages(valid) = (%v, %v)", parts, err)
	}
	if part, ok := parts[0].(messages.ImagePart); !ok || part.MediaType != "image/png" || len(part.Bytes) == 0 {
		t.Fatalf("opened part = %#v, want PNG image bytes", parts[0])
	}
	if parts, err := OpenImages(nil); parts != nil || err != nil {
		t.Fatalf("OpenImages(nil) = (%v, %v), want nothing", parts, err)
	}
	empty := filepath.Join(dir, "empty.png")
	text := filepath.Join(dir, "note.png")
	corrupt := filepath.Join(dir, "corrupt.png")
	for path, data := range map[string][]byte{empty: nil, text: []byte("plain text"), corrupt: []byte("\x89PNG\r\n\x1a\nbroken")} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	for _, test := range []struct{ path, want string }{
		{path: " ", want: "--image path is empty"},
		{path: filepath.Join(dir, "missing.png"), want: "is missing"},
		{path: empty, want: "is empty"},
		{path: text, want: "unsupported MIME type"},
		{path: corrupt, want: "is not valid image/png content"},
	} {
		if _, err := OpenImages([]string{valid, test.path}); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("OpenImages(%q) error = %v, want %q", test.path, err, test.want)
		}
	}
}

func TestStageOpeningImagesRequiresStagerOnlyForImages(t *testing.T) {
	cleanup, err := stageOpeningImages(serviceSession.Request{}, &runtimeSession.LiveRequest{}, nil)
	if err != nil || cleanup() != nil {
		t.Fatalf("stage without images = %v", err)
	}
	if _, err := stageOpeningImages(serviceSession.Request{ImagePaths: []string{"a.png"}}, &runtimeSession.LiveRequest{}, nil); err == nil {
		t.Fatal("staging images without a stager must fail")
	}
}

type plainExecutor struct{ messages.ToolExecutor }

func TestBindImagePreparerLeavesExecutorsWithoutImageRouteUnchanged(t *testing.T) {
	executor := plainExecutor{}
	if bound := BindImagePreparer(executor, func([]string) ([]messages.ContentPart, error) { return nil, errors.New("unused") }); bound != executor {
		t.Fatalf("BindImagePreparer = %#v, want the original executor", bound)
	}
}

func TestRealtimeEndpointDerivesProviderWebSocketURL(t *testing.T) {
	for _, test := range []struct{ provider, base, want string }{
		{provider: "openai", base: "", want: ""},
		{provider: "openai", base: "https://api.example.test/v1", want: "wss://api.example.test/v1/realtime"},
		{provider: "openai", base: "http://127.0.0.1:9/v1/realtime/", want: "ws://127.0.0.1:9/v1/realtime/"},
		{provider: "grok", base: "https://api.x.test/v1", want: "wss://api.x.test/v1"},
		{provider: "openai", base: "not a url", want: "not a url"},
	} {
		if got := realtimeEndpoint(test.provider, test.base); got != test.want {
			t.Fatalf("realtimeEndpoint(%q, %q) = %q, want %q", test.provider, test.base, got, test.want)
		}
	}
}
