package audioinput

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// ManagedSource carries a host-acquired source and transport metadata through
// the service boundary. It owns only the resources explicitly transferred by
// the host and closes them at most once.
type ManagedSource struct {
	Source             audio.AudioSource
	Path               string
	SourceSampleRate   int
	ProviderSampleRate int
	Pace               bool
	Clock              clock.Source
	SendAudioInput     func(context.Context, []byte) error
	SendEndOfTurn      func(context.Context) error
	Observer           Observer
	Reader             io.Reader
	CloseOnCancel      bool
	OwnedInput         io.Closer
	once               sync.Once
	err                error
}

// Close releases the source and the optional transferred host resource.
func (s *ManagedSource) Close() error {
	if s == nil {
		return nil
	}
	closed := false
	s.once.Do(func() {
		closed = true
		if s.Source != nil {
			s.err = errors.Join(s.err, s.Source.Close())
		}
		if s.OwnedInput != nil {
			s.err = errors.Join(s.err, s.OwnedInput.Close())
		}
	})
	if !closed {
		return nil
	}
	if s.err != nil {
		return &Error{Kind: KindClose, Path: s.Path, Err: s.err}
	}
	return nil
}

// BindContext forwards the session context to a cancellation-aware reader.
func (s *ManagedSource) BindContext(ctx context.Context) {
	if s == nil {
		return
	}
	if binder, ok := s.Reader.(interface{ BindContext(context.Context) }); ok {
		binder.BindContext(ctx)
	}
}

// Input projects managed transport metadata onto the stream contract.
func (s *ManagedSource) Input(sourceClock clock.Source) Input {
	if s == nil {
		return Input{}
	}
	return Input{Path: s.Path, Source: s.Source, SourceSampleRate: s.SourceSampleRate, ProviderSampleRate: s.ProviderSampleRate, Pace: s.Pace, Clock: sourceClock, SendAudioInput: s.SendAudioInput, SendEndOfTurn: s.SendEndOfTurn, Observer: s.Observer}
}

// BindObserver attaches the host's bounded byte observer to the managed source.
func (s *ManagedSource) BindObserver(sourceClock clock.Source, observe func([]byte)) {
	if s != nil {
		s.Clock = sourceClock
		s.Observer = ObserverFunc(func(observation AudioObservation) { observe(observation.PCM) })
	}
}

// ClockSource supplies the managed source's deterministic pacing clock.
func (s *ManagedSource) ClockSource() clock.Source {
	if s == nil {
		return nil
	}
	return clock.Ensure(s.Clock)
}
