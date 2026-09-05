package agentruntime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const defaultRoomRunDirectoryPrefix = "room-run-"

// CreateFreshRoomRunDirectory allocates one collision-safe room evidence
// directory below the effective agent config directory. The directory is
// created with private permissions and is empty when returned, so the room
// evidence writer can retain its existing exclusive-file guarantees.
func CreateFreshRoomRunDirectory(configDir string) (string, error) {
	resolvedConfigDir, err := RoomLaunchConfigDir(configDir)
	if err != nil {
		return "", fmt.Errorf("resolve room config directory: %w", err)
	}
	if err := os.MkdirAll(resolvedConfigDir, 0o700); err != nil {
		return "", fmt.Errorf("create room config directory %q: %w", resolvedConfigDir, err)
	}
	runDirectory, err := os.MkdirTemp(resolvedConfigDir, defaultRoomRunDirectoryPrefix)
	if err != nil {
		return "", fmt.Errorf("create fresh room run directory under %q: %w", resolvedConfigDir, err)
	}
	return filepath.Clean(runDirectory), nil
}

func normalizeRoomRecordingOptions(opts RoomRunOptions) (RoomRunOptions, error) {
	if !opts.Manifest.Room.RecordingEnabled() {
		// An explicit manifest disablement is authoritative, including when a
		// caller accidentally leaves the CLI's ordinary output default in place.
		opts.OutputDir = ""
		return opts, nil
	}
	if strings.TrimSpace(opts.OutputDir) != "" {
		return opts, nil
	}
	if destination := opts.Manifest.Room.RecordingDirectory(); destination != "" {
		opts.OutputDir = destination
		return opts, nil
	}
	if opts.LaunchPlan == nil || opts.LaunchPlan.Mode != RoomLaunchModeBare {
		return opts, nil
	}
	destination, err := CreateFreshRoomRunDirectory(opts.ConfigDir)
	if err != nil {
		return RoomRunOptions{}, err
	}
	opts.OutputDir = destination
	return opts, nil
}
