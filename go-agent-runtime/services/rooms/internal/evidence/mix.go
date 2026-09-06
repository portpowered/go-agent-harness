package evidence

import (
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/mixer"
)

// mixAccumulator is an evidence adapter around go-audio's canonical bounded
// PCM accumulator. Room evidence owns when a span is observed; go-audio owns
// all sample alignment, overlap, clipping, and serialization semantics.
type mixAccumulator struct {
	inner *mixer.PCMAccumulator
}

func newMixAccumulator(format rooms.AudioFormat, maxDuration time.Duration) (*mixAccumulator, error) {
	inner, err := mixer.NewPCMAccumulator(format, maxDuration)
	if err != nil {
		return nil, fmt.Errorf("create room mix accumulator: %w", err)
	}
	return &mixAccumulator{inner: inner}, nil
}

// addSource maps a participant-local cursor onto the room timeline. Provider
// cursors commonly restart at a response or interruption boundary, so using
// StartSample as a room-global position would overlay later responses at the
// beginning of the mix. Each stream/epoch is anchored at its first observed
// arrival and subsequent frames follow its local cursor from that anchor.
func (m *mixAccumulator) addSource(participantID string, offset time.Duration, frame audio.PCMFrame, hasStartSample bool, samples []int16) error {
	if m == nil {
		return nil
	}
	return m.inner.AddSource(mixer.SourceKey{SourceID: participantID, StreamID: frame.StreamID, Epoch: frame.Epoch}, offset, frame.StartSample, hasStartSample, frame.EndOfResponse, samples)
}

func (m *mixAccumulator) finalize(span time.Duration, path string) error {
	if m == nil {
		return nil
	}
	return m.inner.Finalize(span, path)
}
