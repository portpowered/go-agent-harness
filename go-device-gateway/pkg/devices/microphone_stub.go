//go:build nomicrophone || !cgo

package devices

import audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"

import "errors"

// NewMicrophoneSource is a stub used when the package is compiled with
// -tags=nomicrophone or when CGO is disabled (e.g. CI without audio/C compiler).
func NewMicrophoneSource() (audio.AudioSource, error) {
	return nil, errors.New("microphone support disabled (compiled with -tags=nomicrophone)")
}
