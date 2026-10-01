package realtime

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
)

// DefaultInputSampleRate is the realtime PCM16 input rate both providers use
// when the session config does not set one.
const DefaultInputSampleRate = 24000

// mediaSlot is the session's RTC media endpoint. A session prepares it before
// the read loop starts so immediate provider audio is queued; an RTC caller
// claims it, and a stream-only caller releases it on its first Receive.
type mediaSlot struct {
	mu         sync.Mutex
	media      *sharedaudio.SessionMedia
	claimed    bool
	continuous bool
}

// InitialSessionConfigSent reports that ConnectSession already sent the
// provider-owned session.update before the read loop started.
func (*Session) InitialSessionConfigSent() bool { return true }

// RTCMedia exposes provider-owned PCM media endpoints. Inbound audio remains
// available through Receive while also being framed for the local RTC sink.
func (s *Session) RTCMedia() sharedaudio.MediaEndpoints {
	return s.RTCMediaWithOptions(sharedaudio.MediaSessionOptions{})
}

// RTCMediaWithOptions is RTCMedia with explicit inbound media options. An
// unclaimed prepared endpoint with different continuity is replaced.
func (s *Session) RTCMediaWithOptions(options sharedaudio.MediaSessionOptions) sharedaudio.MediaEndpoints {
	slot := &s.media
	slot.mu.Lock()
	var previous *sharedaudio.SessionMedia
	if slot.media != nil && !slot.claimed && slot.continuous != options.InboundContinuous {
		previous = slot.media
		slot.media = nil
	}
	slot.claimed = true
	if slot.media == nil {
		slot.media = sharedaudio.NewSessionMediaAtRateWithOptions(s.cfg.WriteMediaFrame, s.cfg.OutputSampleRate, options)
		slot.continuous = options.InboundContinuous
	}
	endpoints := slot.media.Endpoints()
	slot.mu.Unlock()
	if previous != nil {
		if err := previous.Close(); err != nil {
			s.SetTerminalError(fmt.Errorf("close replaced %s RTC media: %w", s.cfg.MediaName, err))
		}
	}
	return endpoints
}

// PrepareRTCMedia creates the speculative media endpoint before the read loop
// starts.
func (s *Session) PrepareRTCMedia() {
	slot := &s.media
	slot.mu.Lock()
	if slot.media == nil {
		slot.media = sharedaudio.NewSessionMediaAtRate(s.cfg.WriteMediaFrame, s.cfg.OutputSampleRate)
		slot.continuous = false
	}
	slot.mu.Unlock()
}

func (s *Session) releaseUnclaimedRTCMedia() {
	slot := &s.media
	slot.mu.Lock()
	if slot.claimed || slot.media == nil {
		slot.mu.Unlock()
		return
	}
	media := slot.media
	slot.media = nil
	slot.continuous = false
	slot.mu.Unlock()
	if err := media.Close(); err != nil {
		s.logger.Warn(s.cfg.LogPrefix+": release unclaimed RTC media", logging.Field{Key: "error", Value: err})
	}
}

// CurrentRTCMedia returns the media endpoint, or nil when none is open.
func (s *Session) CurrentRTCMedia() *sharedaudio.SessionMedia {
	s.media.mu.Lock()
	defer s.media.mu.Unlock()
	return s.media.media
}

// LocalPlayback reports provider audio still queued for or audible on the
// local device, which outlives the provider's response.
func (s *Session) LocalPlayback() messages.LocalPlaybackState {
	activity := s.CurrentRTCMedia().PlaybackActivity()
	return messages.LocalPlaybackState{Active: activity.Active, Level: activity.Level}
}

// InterruptLocalPlayback discards audio not yet heard and reports whether any
// was audible.
func (s *Session) InterruptLocalPlayback(ctx context.Context) bool {
	if !s.LocalPlayback().Active {
		return false
	}
	if s.cfg.InterruptPlayback != nil {
		s.cfg.InterruptPlayback(ctx)
	}
	return true
}

// InputAudioSampleRate reports the rate of the PCM16 audio the client sends.
func (s *Session) InputAudioSampleRate() int {
	if s.cfg.InputSampleRate > 0 {
		return s.cfg.InputSampleRate
	}
	return DefaultInputSampleRate
}

// PushInboundPCM decodes PCM16 provider audio onto media's inbound path.
func PushInboundPCM(media *sharedaudio.SessionMedia, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	samples, err := codec.DecodePCM16(data)
	if err != nil {
		return err
	}
	return media.PushInbound(samples)
}

// FailInboundOnError records err on media's inbound path unless the media is
// already closed, and returns err.
func FailInboundOnError(media *sharedaudio.SessionMedia, err error) error {
	if err != nil && !errors.Is(err, sharedaudio.ErrSessionMediaClosed) {
		media.FailInbound(err)
	}
	return err
}
