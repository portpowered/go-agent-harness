package livehost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

func TestStageLiveOpeningImagesRewritesAndCleansCapabilityPaths(t *testing.T) {
	root := t.TempDir()
	imageBytes := []byte("staged-image")
	baseDefinitions := []messages.ToolDefinition{{
		Name: runtimeTools.ReadImageToolID,
		Parameters: []messages.ToolParameter{{
			Name:        "path",
			Type:        "string",
			Description: "Path to the local image to attach",
			Required:    true,
		}},
	}}
	request := serviceSession.Request{
		ConfigDir:  root,
		ImagePaths: []string{filepath.Join(root, "source.png")},
	}
	liveRequest := runtimeSession.LiveRequest{
		OpeningContentParts: []messages.ContentPart{
			messages.ImagePart{Bytes: imageBytes, MediaType: "image/png"},
		},
		Capabilities: &runtimeSession.LiveCapabilities{
			Definitions: baseDefinitions,
			RefreshDefinitions: func(context.Context) ([]messages.ToolDefinition, error) {
				return baseDefinitions, nil
			},
		},
	}

	cleanup, err := stageLiveOpeningImages(request, &liveRequest)
	if err != nil {
		t.Fatalf("stageLiveOpeningImages: %v", err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Errorf("cleanup staged images: %v", err)
		}
	})

	path := stagedReadImagePath(t, liveRequest.Capabilities.Definitions)
	if !filepath.IsAbs(path) || !strings.HasPrefix(path, filepath.Clean(root)+string(os.PathSeparator)) {
		t.Fatalf("staged path = %q, want an absolute path under %q", path, root)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read staged image: %v", err)
	}
	if string(got) != string(imageBytes) {
		t.Fatalf("staged image = %q, want %q", got, imageBytes)
	}

	refreshed, err := liveRequest.Capabilities.RefreshDefinitions(context.Background())
	if err != nil {
		t.Fatalf("refresh staged definitions: %v", err)
	}
	refreshedPath := stagedReadImagePath(t, refreshed)
	if refreshedPath != path {
		t.Fatalf("refreshed staged path = %q, want %q", refreshedPath, path)
	}

	if err := cleanup(); err != nil {
		t.Fatalf("cleanup staged images: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("staged image after cleanup: err=%v, want not-exist", err)
	}
}

func stagedReadImagePath(t *testing.T, definitions []messages.ToolDefinition) string {
	t.Helper()
	const marker = "Session-staged image path(s) (use one of these exact absolute paths):\n- "
	for _, definition := range definitions {
		if definition.Name != runtimeTools.ReadImageToolID {
			continue
		}
		for _, parameter := range definition.Parameters {
			if parameter.Name != "path" {
				continue
			}
			index := strings.Index(parameter.Description, marker)
			if index < 0 {
				t.Fatalf("read_image path description = %q, want staging marker", parameter.Description)
			}
			path := parameter.Description[index+len(marker):]
			if lineEnd := strings.IndexByte(path, '\n'); lineEnd >= 0 {
				path = path[:lineEnd]
			}
			return path
		}
	}
	t.Fatal("read_image definition or path parameter missing")
	return ""
}
