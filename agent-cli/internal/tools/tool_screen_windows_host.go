//go:build windows

package tools

import "syscall"

// These declarations support the host display adapter. Mouse interaction is
// owned by the reusable tools service and is intentionally absent here.
var (
	user32dll         = syscall.NewLazyDLL("user32.dll")
	procGetSysMetrics = user32dll.NewProc("GetSystemMetrics")
)

const (
	smCxScreen = 0
	smCyScreen = 1
)
