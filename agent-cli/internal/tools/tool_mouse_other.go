//go:build !windows && !linux && !darwin

package tools

import "fmt"

const platformMouseErr = "mouse control is not yet supported on this platform"

func mouseMove(_, _ int) error                  { return fmt.Errorf(platformMouseErr) }
func mouseClick(_, _ int, _ string) error       { return fmt.Errorf(platformMouseErr) }
func mouseDoubleClick(_, _ int, _ string) error { return fmt.Errorf(platformMouseErr) }
func mouseButtonDown(_, _ int, _ string) error  { return fmt.Errorf(platformMouseErr) }
func mouseButtonUp(_, _ int, _ string) error    { return fmt.Errorf(platformMouseErr) }
func mouseDrag(_, _, _, _ int, _ string) error  { return fmt.Errorf(platformMouseErr) }
