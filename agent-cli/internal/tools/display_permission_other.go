//go:build !darwin

package tools

// Non-macOS platforms retain their existing display admission behavior. They
// do not have a macOS TCC preflight boundary to install.
func defaultDisplayPermissionChecker() DisplayPermissionChecker { return nil }
