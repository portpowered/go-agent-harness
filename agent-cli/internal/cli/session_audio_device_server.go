package cli

import (
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
)

func (c *SessionCommand) sessionRuntimeDependencies(audioDeviceServer string) (audio.DeviceRegistry, *cliTools.FilesystemPolicy, error) {
	registry := c.deviceRegistry
	if strings.TrimSpace(audioDeviceServer) != "" {
		remoteRegistry, err := audio.NewRemoteDeviceRegistry(audioDeviceServer)
		if err != nil {
			return nil, nil, fmt.Errorf("configure --audio-device-server: %w", err)
		}
		if _, err := remoteRegistry.List(); err != nil {
			return nil, nil, fmt.Errorf("connect --audio-device-server %q: %w", audioDeviceServer, err)
		}
		registry = remoteRegistry
	}
	filesystemPolicy, err := cliTools.ResolveFilesystemPolicy(globalWorkDir(c.globalFlags), globalAllowPaths(c.globalFlags)...)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve filesystem scope: %w", err)
	}
	return registry, filesystemPolicy, nil
}
