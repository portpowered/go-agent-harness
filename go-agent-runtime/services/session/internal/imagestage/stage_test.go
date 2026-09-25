package imagestage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

func readImageDefinitions(description string) []messages.ToolDefinition {
	return []messages.ToolDefinition{{
		Name: tools.ReadImageToolID,
		Parameters: []messages.ToolParameter{{
			Name: "path", Type: "string", Description: description, Required: true,
		}},
	}}
}

func imageRequest(parts ...messages.ContentPart) session.LiveRequest {
	base := readImageDefinitions("Path to the local image to attach")
	return session.LiveRequest{
		OpeningContentParts: parts,
		Capabilities: &session.LiveCapabilities{
			Definitions: base,
			RefreshDefinitions: func(context.Context) ([]messages.ToolDefinition, error) {
				return base, nil
			},
		},
	}
}

func TestStageOpeningImagesRewritesAndCleansCapabilityPaths(t *testing.T) {
	root := t.TempDir()
	imageBytes := []byte("staged-image")
	liveRequest := imageRequest(messages.ImagePart{Bytes: imageBytes, MediaType: "image/png"})

	cleanup, err := New().StageOpeningImages(session.LiveImageStageRequest{Directory: root, SourcePaths: []string{filepath.Join(root, "source.png")}}, &liveRequest)
	if err != nil {
		t.Fatalf("StageOpeningImages: %v", err)
	}
	path := stagedReadImagePath(t, liveRequest.Capabilities.Definitions)
	if !filepath.IsAbs(path) || !strings.HasPrefix(path, filepath.Clean(root)+string(os.PathSeparator)) || filepath.Ext(path) != ".png" {
		t.Fatalf("staged path = %q, want an absolute .png path under %q", path, root)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(imageBytes) {
		t.Fatalf("staged image = (%q, %v), want %q", got, err, imageBytes)
	}
	if !strings.HasPrefix(liveRequest.Capabilities.Definitions[0].Parameters[0].Description, "Path to the local image to attach\n") {
		t.Fatalf("existing path description was not preserved: %q", liveRequest.Capabilities.Definitions[0].Parameters[0].Description)
	}
	refreshed, err := liveRequest.Capabilities.RefreshDefinitions(context.Background())
	if err != nil {
		t.Fatalf("refresh staged definitions: %v", err)
	}
	if refreshedPath := stagedReadImagePath(t, refreshed); refreshedPath != path {
		t.Fatalf("refreshed staged path = %q, want %q", refreshedPath, path)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup staged images: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("staged image after cleanup: err=%v, want not-exist", err)
	}
}

type fakeHandle struct {
	definitions []messages.ToolDefinition
	events      chan session.LiveCapabilityEvent
	closed      bool
}

func (h *fakeHandle) Initialize(context.Context) error { return nil }
func (h *fakeHandle) RefreshDefinitions(context.Context) ([]messages.ToolDefinition, error) {
	return h.definitions, nil
}
func (h *fakeHandle) Close() error { h.closed = true; return nil }
func (h *fakeHandle) BrowserWatch(context.Context) <-chan session.LiveCapabilityEvent {
	return h.events
}

func TestStageOpeningImagesWrapsCapabilityHandle(t *testing.T) {
	root := t.TempDir()
	inner := &fakeHandle{definitions: []messages.ToolDefinition{{Name: tools.ReadImageToolID}}, events: make(chan session.LiveCapabilityEvent)}
	liveRequest := session.LiveRequest{
		OpeningContentParts: []messages.ContentPart{messages.ImagePart{Bytes: []byte("x"), MediaType: "application/octet-stream"}},
		Capabilities:        &session.LiveCapabilities{Definitions: inner.definitions, Handle: inner},
	}
	cleanup, err := New().StageOpeningImages(session.LiveImageStageRequest{Directory: root, SourcePaths: []string{"photo.webp"}}, &liveRequest)
	if err != nil {
		t.Fatalf("StageOpeningImages: %v", err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Errorf("cleanup staged images: %v", err)
		}
	})
	handle := liveRequest.Capabilities.Handle
	if err := handle.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	refreshed, err := handle.RefreshDefinitions(context.Background())
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if path := stagedReadImagePath(t, refreshed); filepath.Ext(path) != ".webp" {
		t.Fatalf("staged path %q should keep the source extension when the media type is unknown", path)
	}
	watcher, ok := handle.(session.LiveCapabilityWatcher)
	if !ok || watcher.BrowserWatch(context.Background()) == nil {
		t.Fatal("staged handle must forward the inner browser watch")
	}
	if err := handle.Close(); err != nil || !inner.closed {
		t.Fatalf("close = %v, inner closed = %v", err, inner.closed)
	}
}

func TestStageOpeningImagesLeavesRequestsWithoutReadImageUnchanged(t *testing.T) {
	liveRequest := session.LiveRequest{Capabilities: &session.LiveCapabilities{Definitions: []messages.ToolDefinition{{Name: "exec"}}}}
	before := liveRequest.Capabilities
	cleanup, err := New().StageOpeningImages(session.LiveImageStageRequest{SourcePaths: []string{"a.png"}}, &liveRequest)
	if err != nil || cleanup() != nil || liveRequest.Capabilities != before {
		t.Fatalf("stage without read_image = %v, capabilities replaced = %v", err, liveRequest.Capabilities != before)
	}
}

func TestStageOpeningImagesRejectsInconsistentRequests(t *testing.T) {
	for _, test := range []struct {
		name    string
		request session.LiveImageStageRequest
		parts   []messages.ContentPart
		want    string
	}{
		{name: "count", request: session.LiveImageStageRequest{Directory: t.TempDir(), SourcePaths: []string{"a.png", "b.png"}}, parts: []messages.ContentPart{messages.ImagePart{}}, want: "source path count 2 does not match image part count 1"},
		{name: "non-image", request: session.LiveImageStageRequest{Directory: t.TempDir(), SourcePaths: []string{"a.png"}}, parts: []messages.ContentPart{messages.TextPart{Text: "x"}}, want: "is not an image"},
		{name: "directory", request: session.LiveImageStageRequest{SourcePaths: []string{"a.png"}}, parts: []messages.ContentPart{messages.ImagePart{}}, want: "config directory is required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			liveRequest := imageRequest(test.parts...)
			_, err := New().StageOpeningImages(test.request, &liveRequest)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestStageOpeningImagesReportsUnwritableDirectory(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parent, nil, 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	liveRequest := imageRequest(messages.ImagePart{Bytes: []byte("x"), MediaType: "image/jpeg; q=1"})
	_, err := New().StageOpeningImages(session.LiveImageStageRequest{Directory: parent, SourcePaths: []string{"a"}}, &liveRequest)
	var pathErr *os.PathError
	if err == nil || !errors.As(err, &pathErr) {
		t.Fatalf("error = %v, want a filesystem failure", err)
	}
}

func stagedReadImagePath(t *testing.T, definitions []messages.ToolDefinition) string {
	t.Helper()
	for _, definition := range definitions {
		if definition.Name != tools.ReadImageToolID {
			continue
		}
		for _, parameter := range definition.Parameters {
			if parameter.Name != "path" {
				continue
			}
			index := strings.Index(parameter.Description, pathAdvertisement)
			if index < 0 {
				t.Fatalf("read_image path description = %q, want staging marker", parameter.Description)
			}
			path := parameter.Description[index+len(pathAdvertisement):]
			if lineEnd := strings.IndexByte(path, '\n'); lineEnd >= 0 {
				path = path[:lineEnd]
			}
			return path
		}
	}
	t.Fatal("read_image definition or path parameter missing")
	return ""
}
