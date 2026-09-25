package devices

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// FilePacing selects how finite file inputs (Input, InputTurns and
// Interruptions) are delivered to the provider. Stdin is never paced.
//
// The zero value is the production default: each file input is delivered at
// its encoded real-time cadence on FileMediaRequest.Scheduler, so a finite
// clip reaches the provider exactly as a live microphone would.
//
// Hosts that do not need arrival cadence (for example a replay, or a hermetic
// test against a scripted provider) may accelerate or disable pacing. Pacing
// never changes which samples are sent, their order, or turn boundaries; it
// only changes when each frame is released.
type FilePacing struct {
	// Unpaced delivers file inputs as fast as the bounded provider queue
	// accepts them. Speed is ignored when Unpaced is set.
	Unpaced bool
	// Speed multiplies the paced cadence. Zero and one keep real time; ten
	// releases ten seconds of audio per scheduler second. It must be finite
	// and not negative.
	Speed float64
}

// Validate reports an unusable pacing selection.
func (p FilePacing) Validate() error {
	if math.IsNaN(p.Speed) || math.IsInf(p.Speed, 0) || p.Speed < 0 {
		return fmt.Errorf("%w: file input pacing speed %v must be finite and not negative", ErrInvalidRequest, p.Speed)
	}
	return nil
}

// Realtime reports whether the selection is the production real-time cadence.
func (p FilePacing) Realtime() bool {
	return !p.Unpaced && (p.Speed == 0 || p.Speed == 1)
}

// String renders the selection in the spelling ParseFilePacing accepts.
func (p FilePacing) String() string {
	switch {
	case p.Unpaced:
		return filePacingUnpaced
	case p.Realtime():
		return filePacingRealtime
	default:
		return strconv.FormatFloat(p.Speed, 'g', -1, 64) + "x"
	}
}

const (
	filePacingRealtime = "realtime"
	filePacingUnpaced  = "unpaced"
)

// ParseFilePacing reads a host option spelling: "" or "realtime" (the
// default), "unpaced", or a speed multiplier such as "10x" or "2.5x".
func ParseFilePacing(value string) (FilePacing, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "", filePacingRealtime:
		return FilePacing{}, nil
	case filePacingUnpaced:
		return FilePacing{Unpaced: true}, nil
	}
	number, ok := strings.CutSuffix(value, "x")
	if !ok {
		return FilePacing{}, fmt.Errorf("%w: file input pacing %q: want realtime, unpaced, or a speed such as 10x", ErrInvalidRequest, value)
	}
	speed, err := strconv.ParseFloat(number, 64)
	if err != nil || speed <= 0 {
		return FilePacing{}, fmt.Errorf("%w: file input pacing %q: speed must be a positive number", ErrInvalidRequest, value)
	}
	pacing := FilePacing{Speed: speed}
	return pacing, pacing.Validate()
}
