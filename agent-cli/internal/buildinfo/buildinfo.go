// Package buildinfo reports the build version of the yui binary.
package buildinfo

import (
	"runtime/debug"
	"strings"
)

// version is set at link time:
//
//	go build -ldflags "-X github.com/portpowered/go-agent-harness/agent-cli/internal/buildinfo.version=v1.2.3"
var version string

// develVersion is the version of a build with no version information.
const develVersion = "dev"

// Version is the build version: the linked version, else the main module
// version recorded by the Go toolchain, else "dev".
func Version() string {
	info, _ := debug.ReadBuildInfo()
	return resolve(version, info)
}

func resolve(linked string, info *debug.BuildInfo) string {
	if linked = strings.TrimSpace(linked); linked != "" {
		return linked
	}
	if info != nil {
		if module := strings.TrimSpace(info.Main.Version); module != "" && module != "(devel)" {
			return module
		}
	}
	return develVersion
}
