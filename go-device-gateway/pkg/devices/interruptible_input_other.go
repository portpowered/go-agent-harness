//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package devices

import (
	"fmt"
	"os"
)

func openInterruptibleInput(file *os.File) (*os.File, error) {
	return nil, fmt.Errorf("%w", ErrInterruptibleInputUnsupported)
}
