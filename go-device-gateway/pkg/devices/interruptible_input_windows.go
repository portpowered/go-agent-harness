//go:build windows

package devices

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	interruptibleKernel32       = syscall.NewLazyDLL("kernel32.dll")
	interruptibleGetCurrentProc = interruptibleKernel32.NewProc("GetCurrentProcess")
	interruptibleDuplicate      = interruptibleKernel32.NewProc("DuplicateHandle")
)

func openInterruptibleInput(file *os.File) (*os.File, error) {
	if file == nil {
		return nil, fmt.Errorf("%w: nil input", ErrInterruptibleInputUnsupported)
	}
	process, _, _ := interruptibleGetCurrentProc.Call()
	var duplicate syscall.Handle
	result, _, callErr := interruptibleDuplicate.Call(
		process,
		uintptr(file.Fd()),
		process,
		uintptr(unsafe.Pointer(&duplicate)),
		0,
		0, // keep the duplicate private to this process
		2, // DUPLICATE_SAME_ACCESS
	)
	if result == 0 {
		if callErr == syscall.Errno(0) {
			callErr = syscall.EINVAL
		}
		return nil, fmt.Errorf("duplicate input %q: %w", file.Name(), callErr)
	}
	dup := os.NewFile(uintptr(duplicate), file.Name())
	if dup == nil {
		_ = syscall.CloseHandle(duplicate)
		return nil, fmt.Errorf("create duplicated input %q", file.Name())
	}
	return dup, nil
}
