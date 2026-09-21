package admission

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence/internal/pathguard"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

type manifestLimitError int64

func (e manifestLimitError) Error() string {
	return fmt.Sprintf("room replay manifest exceeds the %d-byte limit", int64(e))
}

const errManifestTooLarge = manifestLimitError(MaxManifestBytes)

func resolveBundleRoot(bundle string) (string, error) {
	root, err := filepath.Abs(strings.TrimSpace(bundle))
	if err != nil {
		return "", incomplete("bundle", err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", incomplete("bundle", fmt.Errorf("bundle directory is unavailable"))
	}
	if err := pathguard.ValidateNoSymlink(root, root); err != nil {
		return "", mismatch("bundle", err)
	}
	return root, nil
}

func loadManifestObject(root string) (object, string, error) {
	manifestPath := filepath.Join(root, rooms.RoomReplayBundleManifestPath)
	if err := pathguard.ValidateNoSymlink(root, manifestPath); err != nil {
		return nil, "", mismatch("manifest", err)
	}
	if err := validateManifestRegularFile(manifestPath); err != nil {
		return nil, "", err
	}
	data, err := readManifest(manifestPath)
	if err != nil {
		if errors.Is(err, errManifestTooLarge) {
			return nil, "", mismatch("manifest", err)
		}
		return nil, "", incomplete("manifest", err)
	}
	decoded, err := decodeObject(data)
	if err != nil {
		return nil, "", mismatch("manifest", err)
	}
	return decoded, manifestPath, nil
}

func readManifest(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, MaxManifestBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if int64(len(data)) > MaxManifestBytes {
		return nil, errManifestTooLarge
	}
	return data, nil
}

func validateManifestRegularFile(path string) error {
	if err := pathguard.ValidateRegularFile(path); err != nil {
		if os.IsNotExist(err) {
			return incomplete("manifest", fmt.Errorf("manifest is unavailable"))
		}
		return mismatch("manifest", err)
	}
	return nil
}
