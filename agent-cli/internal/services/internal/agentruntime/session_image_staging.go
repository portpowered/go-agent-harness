package agentruntime

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
)

// prepareSessionImageToolAccess gives read_image a stable, session-owned copy
// of each initial image and advertises those exact paths to the provider. The
// inline image turn still uses the validated parts supplied by the caller;
// staging is only needed for a later model-issued read_image call.
func prepareSessionImageToolAccess(opts SessionRunOptions, sourcePaths []string, parts []messages.ImagePart) (SessionRunOptions, func(), error) {
	if !sessionHasTool(opts.ToolDefinitions, runtimeTools.ReadImageToolID) {
		return opts, noOpSessionImageCleanup, nil
	}
	if len(sourcePaths) != len(parts) {
		return opts, noOpSessionImageCleanup, fmt.Errorf("stage session images: source path count %d does not match image part count %d", len(sourcePaths), len(parts))
	}

	configDir, err := sessionImageStagingConfigDir(opts.ConfigDir)
	if err != nil {
		return opts, noOpSessionImageCleanup, fmt.Errorf("stage session images: %w", err)
	}
	staged, err := runtimeToolsWire.NewImageStaging().Stage(context.Background(), runtimeTools.ImageStagingRequest{
		StagingRoot:            configDir,
		SourcePaths:            sourcePaths,
		ImageParts:             parts,
		ToolDefinitions:        opts.ToolDefinitions,
		RefreshToolDefinitions: opts.RefreshToolDefinitions,
	})
	if err != nil {
		return opts, noOpSessionImageCleanup, err
	}
	opts.ToolDefinitions = staged.ToolDefinitions
	opts.RefreshToolDefinitions = staged.RefreshToolDefinitions
	cleanup := func() {
		if staged.Cleanup != nil {
			if err := staged.Cleanup(); err != nil {
				log.Printf("stage session images: cleanup: %v", err)
			}
		}
	}
	return opts, cleanup, nil
}

func noOpSessionImageCleanup() {}

func sessionImageStagingConfigDir(configDir string) (string, error) {
	configDir = strings.TrimSpace(configDir)
	if configDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		configDir = filepath.Join(homeDir, config.ConfigDirName)
	}
	abs, err := filepath.Abs(configDir)
	if err != nil {
		return "", fmt.Errorf("resolve config directory %q: %w", configDir, err)
	}
	return filepath.Clean(abs), nil
}
