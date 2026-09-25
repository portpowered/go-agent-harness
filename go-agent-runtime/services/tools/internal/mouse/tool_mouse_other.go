//go:build !windows && !linux && !darwin

package mouse

import "fmt"

const platformMouseErr = "mouse control is not yet supported on this platform"

func (mouseDriver) move(_, _ int) error                  { return fmt.Errorf(platformMouseErr) }
func (mouseDriver) click(_, _ int, _ string) error       { return fmt.Errorf(platformMouseErr) }
func (mouseDriver) doubleClick(_, _ int, _ string) error { return fmt.Errorf(platformMouseErr) }
func (mouseDriver) buttonDown(_, _ int, _ string) error  { return fmt.Errorf(platformMouseErr) }
func (mouseDriver) buttonUp(_, _ int, _ string) error    { return fmt.Errorf(platformMouseErr) }
func (mouseDriver) drag(_, _, _, _ int, _ string) error  { return fmt.Errorf(platformMouseErr) }
