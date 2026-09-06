//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package devices

import (
	"fmt"
	"os"
	"syscall"
)

func openInterruptibleInput(file *os.File) (*os.File, error) {
	if file == nil {
		return nil, fmt.Errorf("%w: nil input", ErrInterruptibleInputUnsupported)
	}
	// Keep duplication and close-on-exec setup under ForkLock. Otherwise a
	// concurrently starting subprocess could inherit the temporary duplicate
	// before CloseOnExec runs and retain the session's stdin lease.
	syscall.ForkLock.RLock()
	fd, err := syscall.Dup(int(file.Fd()))
	if err == nil {
		syscall.CloseOnExec(fd)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, fmt.Errorf("duplicate input %q: %w", file.Name(), err)
	}
	// os.File's poller only provides reliable deadlines for nonblocking
	// descriptors. This matters for inherited Darwin pipes, where closing the
	// original os.Stdin does not wake a goroutine blocked in syscall.Read.
	if err := syscall.SetNonblock(fd, true); err != nil {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("configure duplicated input %q: %w", file.Name(), err)
	}
	dup := os.NewFile(uintptr(fd), file.Name())
	if dup == nil {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("create duplicated input %q", file.Name())
	}
	return dup, nil
}
