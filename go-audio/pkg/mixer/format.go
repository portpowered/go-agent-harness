package mixer

import (
	"fmt"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// Format is the negotiated provider-side PCM cadence. It is deliberately
// explicit: a room must not infer a device format or silently resample here.
type Format struct {
	SampleRate    int
	Channels      int
	FrameDuration time.Duration
}

func (f Format) FrameSamples() (int, error) {
	if f.SampleRate <= 0 || f.Channels != audio.Channels || f.FrameDuration <= 0 {
		return 0, fmt.Errorf("%w: want positive sample rate, mono channels, and positive frame duration", ErrInvalidFormat)
	}
	total := int64(f.SampleRate) * int64(f.FrameDuration)
	if total <= 0 || total%int64(time.Second) != 0 {
		return 0, fmt.Errorf("%w: frame duration %s is not sample aligned at %d Hz", ErrInvalidFormat, f.FrameDuration, f.SampleRate)
	}
	samples := total / int64(time.Second) * int64(f.Channels)
	if samples <= 0 || samples > int64(int(^uint(0)>>1)) {
		return 0, fmt.Errorf("%w: frame sample count is out of range", ErrInvalidFormat)
	}
	return int(samples), nil
}

type Config struct {
	Format Format
	// StreamID identifies the mix timeline in diagnostics and evidence. It is
	// never copied from an input source because a mix has its own lineage.
	StreamID          string
	InputQueueFrames  int
	OutputQueueFrames int
}

const (
	defaultInputQueueFrames  = 16
	defaultOutputQueueFrames = 8
	defaultSampleRate        = 24000
	defaultFrameDuration     = 30 * time.Millisecond
)

// DefaultFormat is the provider session cadence used by the existing
// realtime media adapters. Device workers may negotiate a different local
// format and convert at their own boundary.
func DefaultFormat() Format {
	return Format{SampleRate: defaultSampleRate, Channels: 1, FrameDuration: defaultFrameDuration}
}

func (c Config) normalize() (Config, int, error) {
	samples, err := c.Format.FrameSamples()
	if err != nil {
		return Config{}, 0, err
	}
	if c.InputQueueFrames == 0 {
		c.InputQueueFrames = defaultInputQueueFrames
	}
	if c.OutputQueueFrames == 0 {
		c.OutputQueueFrames = defaultOutputQueueFrames
	}
	if strings.TrimSpace(c.StreamID) == "" {
		c.StreamID = "mix"
	}
	if c.InputQueueFrames < 1 || c.OutputQueueFrames < 1 {
		return Config{}, 0, fmt.Errorf("%w: queue capacities must be positive", ErrInvalidFormat)
	}
	return c, samples, nil
}
