//go:build linux || darwin

package display

// normalizeDisplayProcess substitutes the production subprocess boundary for
// a nil one. Only the command-based platforms (linux, darwin) use it.
func normalizeDisplayProcess(process DisplayProcess) DisplayProcess {
	if process == nil {
		return defaultDisplayProcess()
	}
	return process
}
