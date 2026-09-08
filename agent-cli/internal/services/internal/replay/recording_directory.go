package replay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	publicreplay "github.com/portpowered/go-agent-harness/agent-cli/internal/services/replay"
	runtimeReplayWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/wire"
)

// validateRecordingBundle is deliberately only a root-manifest detector. A
// standalone trace directory has no manifest and retains its existing narrow
// trace replay scope. Once a root manifest is present, the shared runtime
// admission boundary validates every declared artifact before this service
// selects timeline.jsonl or opens any replay evidence.
func validateRecordingBundle(ctx context.Context, bundlePath string) error {
	manifestPath := filepath.Join(bundlePath, "manifest.json")
	info, err := os.Lstat(manifestPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("%w: inspect recording manifest %s: %w", publicreplay.ErrBundleIncomplete, manifestPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: recording manifest %s is a symlink", publicreplay.ErrBundleIncomplete, manifestPath)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: recording manifest %s is not a regular file", publicreplay.ErrBundleIncomplete, manifestPath)
	}
	if _, err := runtimeReplayWire.NewService().ResolveCapturePath(ctx, bundlePath); err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		return fmt.Errorf("%w: validate recording bundle: %w", publicreplay.ErrBundleIncomplete, err)
	}
	return nil
}

func prepareTraceDirectory(ctx context.Context, bundlePath string) (string, error) {
	if err := validateRecordingBundle(ctx, bundlePath); err != nil {
		return "", err
	}
	return resolveTraceDirectory(bundlePath)
}
