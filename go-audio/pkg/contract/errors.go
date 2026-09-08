// Package contract contains small lifecycle contracts shared by the audio
// transport and its focused analysis owners. Keeping these values here avoids
// making either side depend on the other's implementation package.
package contract

import "errors"

// ErrClosed identifies an operation attempted after an owned audio component
// has released its retained state.
var ErrClosed = errors.New("audio adapter is closed")
