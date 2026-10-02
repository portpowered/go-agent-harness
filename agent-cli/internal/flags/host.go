package flags

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
)

// HostDirs looks up the process host directories. ProcessHostDirs is the one
// host boundary that reads them from the operating system; everything below
// the CLI receives the resolved values instead of reading process state.
type HostDirs struct {
	HomeDir func() (string, error)
	WorkDir func() (string, error)
}

// ProcessHostDirs returns lookups backed by the running process.
func ProcessHostDirs() HostDirs {
	return HostDirs{
		HomeDir: os.UserHomeDir, //nolint:forbidigo // The CLI host boundary: the one place the user home directory is read from the process.
		WorkDir: os.Getwd,       //nolint:forbidigo // The CLI host boundary: the one place the working directory is read from the process.
	}
}

// hostDirError is a constant sentinel error for unavailable host directories.
type hostDirError string

func (e hostDirError) Error() string { return string(e) }

const (
	errHomeDirUnavailable = hostDirError("user home directory is not available")
	errWorkDirUnavailable = hostDirError("working directory is not available")
)

// HostHomeDir returns the injected user home directory.
func (f *GlobalFlags) HostHomeDir() (string, error) {
	if f == nil || f.Host.HomeDir == nil {
		return "", errHomeDirUnavailable
	}
	home, err := f.Host.HomeDir()
	if err != nil {
		return "", errors.Join(errHomeDirUnavailable, err)
	}
	if home == "" {
		return "", errHomeDirUnavailable
	}
	return home, nil
}

// HostWorkDir returns the injected process working directory. It is the
// default filesystem root when --workdir is omitted.
func (f *GlobalFlags) HostWorkDir() (string, error) {
	if f == nil || f.Host.WorkDir == nil {
		return "", errWorkDirUnavailable
	}
	workDir, err := f.Host.WorkDir()
	if err != nil {
		return "", errors.Join(errWorkDirUnavailable, err)
	}
	if workDir == "" {
		return "", errWorkDirUnavailable
	}
	return workDir, nil
}

// EffectiveWorkDir returns --workdir when set, otherwise the host working
// directory.
func (f *GlobalFlags) EffectiveWorkDir() (string, error) {
	if workDir := f.WorkDir(); workDir != "" {
		return workDir, nil
	}
	return f.HostWorkDir()
}

// defaultConfigDir is ~/.agent-cli below the injected home, or empty when the
// home directory is unavailable.
func (f *GlobalFlags) defaultConfigDir() string {
	home, err := f.HostHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, config.ConfigDirName)
}

// HostHomeDirOrEmpty returns the injected home directory, or "" when the host
// cannot report one.
func (f *GlobalFlags) HostHomeDirOrEmpty() string {
	if home, err := f.HostHomeDir(); err == nil {
		return home
	}
	return ""
}

// HostWorkDirOrEmpty returns the injected working directory, or "" when the
// host cannot report one.
func (f *GlobalFlags) HostWorkDirOrEmpty() string {
	if workDir, err := f.HostWorkDir(); err == nil {
		return workDir
	}
	return ""
}
