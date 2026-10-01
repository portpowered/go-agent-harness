//go:build !windows && !linux && !darwin

package mouse

import (
	"context"
	"fmt"
)

const platformMouseErr = "mouse control is not yet supported on this platform"

func (mouseDriver) move(_ context.Context, _, _ int) error { return fmt.Errorf(platformMouseErr) }
func (mouseDriver) click(_ context.Context, _, _ int, _ string) error {
	return fmt.Errorf(platformMouseErr)
}
func (mouseDriver) doubleClick(_ context.Context, _, _ int, _ string) error {
	return fmt.Errorf(platformMouseErr)
}
func (mouseDriver) buttonDown(_ context.Context, _, _ int, _ string) error {
	return fmt.Errorf(platformMouseErr)
}
func (mouseDriver) buttonUp(_ context.Context, _, _ int, _ string) error {
	return fmt.Errorf(platformMouseErr)
}
func (mouseDriver) drag(_ context.Context, _, _, _, _ int, _ string) error {
	return fmt.Errorf(platformMouseErr)
}
