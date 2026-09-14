package agentruntime

import (
	"context"
	"errors"
	"os"
	"sync"

	runtimesession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	sharedclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func classifySessionAudioOpenError(path string, err error) error {
	kind := SessionAudioInputUnreadable
	switch {
	case errors.Is(err, audio.ErrUnsupportedFormat):
		kind = SessionAudioInputFormat
	case errors.Is(err, os.ErrNotExist):
		kind = SessionAudioInputMissing
	case errors.Is(err, audio.ErrNilStream):
		kind = SessionAudioInputUnreadable
	}
	return &SessionAudioInputError{Kind: kind, Path: path, Err: err}
}

type sessionAudioSource struct {
	source       audio.AudioSource
	path         string
	sourceRate   int
	providerRate int
	reader       *sessionAudioReader
	ownedInput   *os.File
	// paced marks file-backed finite sources whose frames must be delivered
	// at the encoded real-time rate. Synthetic test sources injected through
	// the SessionAudioInput.Source seam are never paced so tests control
	// their own timing.
	paced       bool
	send        func(context.Context, []byte) error
	endOfTurn   func(context.Context) error
	runtime     *sessionRuntimeObservationRecorder
	clock       sharedclock.Source
	recordAudio func(runtimesession.LiveAudioRecord)
	recordRate  int
	once        sync.Once
	err         error
}

func (s *sessionAudioSource) bindProviderRate(rate int) {
	if s != nil {
		s.providerRate = rate
	}
}

func (s *sessionAudioSource) bindContext(ctx context.Context) {
	if s.reader != nil {
		s.reader.bindContext(ctx)
	}
}

func (s *sessionAudioSource) bindRuntime(runtime *sessionRuntimeObservationRecorder, source sharedclock.Source) {
	if s != nil {
		s.runtime = runtime
		s.clock = source
	}
}

func (s *sessionAudioSource) bindLiveAudio(record func(runtimesession.LiveAudioRecord), rate int) {
	if s != nil {
		s.recordAudio = record
		s.recordRate = rate
	}
}

func (s *sessionAudioSource) Close() error {
	s.once.Do(func() {
		s.err = s.source.Close()
		if s.ownedInput != nil {
			s.err = errors.Join(s.err, s.reader.Close())
		}
	})
	if s.err == nil {
		return nil
	}
	return &SessionAudioInputError{Kind: SessionAudioInputClose, Path: s.path, Err: s.err}
}
