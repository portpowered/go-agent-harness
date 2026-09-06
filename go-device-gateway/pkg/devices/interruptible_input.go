package devices

import (
	"errors"
	"os"
)

// ErrInterruptibleInputUnsupported indicates that the current platform
// cannot duplicate an inherited input descriptor for cancellation-safe reads.
var ErrInterruptibleInputUnsupported = errors.New("interruptible input is unsupported on this platform")

// OpenInterruptibleInput duplicates file and returns an independently owned
// descriptor suitable for a session's stdin audio reader. The caller retains
// ownership of file; the returned descriptor must be closed by its caller.
// Platform implementations configure the duplicate so read deadlines and
// closing it can interrupt a blocked pipe read. On Unix, dup shares the
// underlying open-file status flags with file, so the input descriptor must be
// exclusively consumed while this adapter is active.
func OpenInterruptibleInput(file *os.File) (*os.File, error) {
	return openInterruptibleInput(file)
}
