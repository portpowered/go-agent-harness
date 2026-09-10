package agentruntime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	"github.com/stretchr/testify/require"
)

func TestPrepareSessionImageToolAccess_NoReadImageSkipsHostResolution(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-created")
	opts := SessionRunOptions{
		ConfigDir:       "\x00invalid-config-path",
		ToolDefinitions: []messages.ToolDefinition{{Name: "unrelated"}},
	}
	got, cleanup, err := prepareSessionImageToolAccess(opts, []string{"image.png"}, nil)
	if err != nil {
		t.Fatalf("prepare without read_image returned error: %v", err)
	}
	if len(got.ToolDefinitions) != 1 || got.ToolDefinitions[0].Name != "unrelated" {
		t.Fatalf("tool definitions = %#v, want unchanged no-op", got.ToolDefinitions)
	}
	cleanup()
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected staging root stat error = %v", err)
	}
}

func TestPrepareSessionImageToolAccess_StagesAndRefreshesThroughToolsContract(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config")
	base := []messages.ToolDefinition{
		{Name: "unrelated", Description: "keep"},
		{Name: runtimeTools.ReadImageToolID, Parameters: []messages.ToolParameter{{Name: "path", Type: "string", Description: "original", Required: true}}},
	}
	opts := SessionRunOptions{ConfigDir: configDir, ToolDefinitions: base}
	imageBytes := []byte("literal-image")
	got, cleanup, err := prepareSessionImageToolAccess(opts, []string{"source.any"}, []messages.ImagePart{{Bytes: imageBytes, MediaType: "image/png"}})
	require.NoError(t, err)
	require.Nil(t, got.RefreshToolDefinitions)
	const marker = "Session-staged image path(s) (use one of these exact absolute paths):\n- "
	path := ""
	for _, definition := range got.ToolDefinitions {
		if definition.Name != runtimeTools.ReadImageToolID {
			continue
		}
		for _, parameter := range definition.Parameters {
			if parameter.Name != "path" {
				continue
			}
			index := strings.Index(parameter.Description, marker)
			if index >= 0 {
				path = strings.TrimSpace(strings.Split(parameter.Description[index+len(marker):], "\n- ")[0])
			}
		}
	}
	require.NotEmpty(t, path, "read_image definition missing staged path marker")
	require.True(t, filepath.IsAbs(path), "advertised path = %q, want absolute path", path)
	require.Equal(t, ".png", filepath.Ext(path))
	actual, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, imageBytes, actual)
	_, err = os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	cleanup()
	cleanup()
	_, err = os.Stat(filepath.Dir(path))
	require.ErrorIs(t, err, os.ErrNotExist)
}
