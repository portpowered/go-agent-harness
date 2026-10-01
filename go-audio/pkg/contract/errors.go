// Package contract contains small lifecycle contracts shared by the audio
// transport and its focused analysis owners. Keeping these values here avoids
// making either side depend on the other's implementation package.
package contract

import "errors"

// ErrClosed identifies an operation attempted after an owned audio component
// has released its retained state.
var ErrClosed = errors.New("audio adapter is closed")

// ErrNilContext reports an audio or device operation called with a nil
// context. Operations require a non-nil context; pass context.Background()
// (or a test's t.Context()) when no cancellation is wanted.
var ErrNilContext = errors.New("audio operation requires a non-nil context")
