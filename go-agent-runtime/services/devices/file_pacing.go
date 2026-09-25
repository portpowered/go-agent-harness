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
	// releases ten seconds of audio per scheduler second. A non-zero speed
	// must lie in [MinFilePacingSpeed, MaxFilePacingSpeed]; use Unpaced for
	// no pacing at all.
	Speed float64
}

// The accepted speed range keeps accelerated scheduler arithmetic well inside
// time.Duration: a slower speed would stretch waits past any useful bound and
// a faster one is indistinguishable from Unpaced.
const (
	MinFilePacingSpeed = 0.01
	MaxFilePacingSpeed = 1000
)

// Validate reports an unusable pacing selection. Speed is checked even when
// Unpaced is set so a malformed request never passes silently.
func (p FilePacing) Validate() error {
	if p.Speed == 0 {
		return nil
	}
	if math.IsNaN(p.Speed) || p.Speed < MinFilePacingSpeed || p.Speed > MaxFilePacingSpeed {
		return fmt.Errorf("%w: file input pacing speed %v must be between %vx and %vx (or use unpaced)", ErrInvalidRequest, p.Speed, MinFilePacingSpeed, MaxFilePacingSpeed)
	}
	return nil
}

// Realtime reports whether the selection is the production real-time cadence.
func (p FilePacing) Realtime() bool {
	return !p.Unpaced && (p.Speed == 0 || p.Speed == 1)
}

// String renders the selection in the spelling UnmarshalText accepts.
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

// MarshalText renders the selection in the spelling UnmarshalText accepts.
func (p FilePacing) MarshalText() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return []byte(p.String()), nil
}

// UnmarshalText reads a host option spelling: "" or "realtime" (the
// default), "unpaced", or a speed multiplier such as "10x" or "2.5x". It
// implements encoding.TextUnmarshaler, so a host can bind a command-line
// flag with flag.TextVar.
func (p *FilePacing) UnmarshalText(text []byte) error {
	value := strings.ToLower(strings.TrimSpace(string(text)))
	switch value {
	case "", filePacingRealtime:
		*p = FilePacing{}
		return nil
	case filePacingUnpaced:
		*p = FilePacing{Unpaced: true}
		return nil
	}
	number, ok := strings.CutSuffix(value, "x")
	if !ok {
		return fmt.Errorf("%w: file input pacing %q: want realtime, unpaced, or a speed such as 10x", ErrInvalidRequest, value)
	}
	speed, err := strconv.ParseFloat(number, 64)
	if err != nil || speed <= 0 {
		return fmt.Errorf("%w: file input pacing %q: speed must be a positive number", ErrInvalidRequest, value)
	}
	pacing := FilePacing{Speed: speed}
	if err := pacing.Validate(); err != nil {
		return err
	}
	*p = pacing
	return nil
}
