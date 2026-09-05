package rtc

import sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"

import (
	"context"
	"io"
	"sync"

	"github.com/pion/webrtc/v4"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

type pionInbound struct {
	frames          chan sharedaudio.PCMFrame
	visuals         chan pionVisualFrame
	done            chan struct{}
	once            sync.Once
	close           func() error
	mu              sync.Mutex
	audioSeen       bool
	videoSeen       bool
	videoNegotiated bool
	videoMediaType  string
	videoReady      chan struct{}
	videoReadyOnce  sync.Once
	source          string
}

type pionVisualFrame struct {
	mediaType string
	bytes     []byte
}

func newPionInbound(closeFn func() error, source ...string) *pionInbound {
	identity := ""
	if len(source) > 0 {
		identity = source[0]
	}
	return &pionInbound{
		frames:     make(chan sharedaudio.PCMFrame, 8),
		visuals:    make(chan pionVisualFrame, 8),
		done:       make(chan struct{}),
		close:      closeFn,
		videoReady: make(chan struct{}),
		source:     identity,
	}
}

func (m *pionInbound) setVideoNegotiated(negotiated bool) {
	m.mu.Lock()
	m.videoNegotiated = negotiated
	m.mu.Unlock()
}

func (m *pionInbound) attach(track *webrtc.TrackRemote) {
	m.attachAudio(track)
}

func (m *pionInbound) attachAudio(track *webrtc.TrackRemote) {
	if track == nil {
		return
	}
	m.mu.Lock()
	if m.audioSeen {
		m.mu.Unlock()
		return
	}
	m.audioSeen = true
	m.mu.Unlock()
	go func() {
		for {
			packet, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			samples := codec.DecodeRTPAudioPayload(track.Codec().MimeType, packet.Payload)
			if len(samples) == 0 {
				continue
			}
			select {
			case m.frames <- sharedaudio.PCMFrame{Samples: samples}:
			case <-m.done:
				return
			}
		}
	}()
}

func (m *pionInbound) attachVideo(track *webrtc.TrackRemote) {
	if track == nil {
		return
	}
	mediaType := track.Codec().MimeType
	m.mu.Lock()
	if m.videoSeen {
		m.mu.Unlock()
		return
	}
	m.videoSeen = true
	m.videoMediaType = mediaType
	m.videoReadyOnce.Do(func() { close(m.videoReady) })
	m.mu.Unlock()
	go func() {
		for {
			packet, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			if packet == nil || len(packet.Payload) == 0 {
				continue
			}
			payload := append([]byte(nil), packet.Payload...)
			select {
			case m.visuals <- pionVisualFrame{mediaType: mediaType, bytes: payload}:
			case <-m.done:
				return
			}
		}
	}()
}

func (m *pionInbound) Look(ctx context.Context) (VisualObservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	negotiated, attached, mediaType, ready := m.videoNegotiated, m.videoSeen, m.videoMediaType, m.videoReady
	m.mu.Unlock()
	if !negotiated && !attached {
		return VisualObservation{Source: m.source, Status: VisualObservationUnavailable, Reason: VisualObservationReasonNoVideoTrack}, nil
	}
	if err := callerContextError(ctx); err != nil {
		return VisualObservation{}, err
	}
	lookCtx, cancel := context.WithTimeout(ctx, DefaultVisualObservationTimeout)
	defer cancel()
	if !attached {
		select {
		case <-ready:
			m.mu.Lock()
			mediaType = m.videoMediaType
			m.mu.Unlock()
		case <-m.done:
			if err := ctx.Err(); err != nil {
				return VisualObservation{}, err
			}
			return VisualObservation{Source: m.source, Status: VisualObservationUnavailable, Reason: VisualObservationReasonNoVideoTrack}, nil
		case <-lookCtx.Done():
			if err := ctx.Err(); err != nil {
				return VisualObservation{}, err
			}
			return VisualObservation{Source: m.source, Status: VisualObservationUnavailable, Reason: VisualObservationReasonNoVideoTrack}, nil
		}
	}
	for {
		select {
		case frame := <-m.visuals:
			if len(frame.bytes) == 0 {
				continue
			}
			if mediaType == "" {
				m.mu.Lock()
				mediaType = m.videoMediaType
				m.mu.Unlock()
			}
			return VisualObservation{Source: m.source, Status: VisualObservationAvailable, MediaType: mediaType, Bytes: append([]byte(nil), frame.bytes...)}, nil
		case <-m.done:
			if err := ctx.Err(); err != nil {
				return VisualObservation{}, err
			}
			return VisualObservation{Source: m.source, Status: VisualObservationUnavailable, Reason: VisualObservationReasonNoVideoTrack}, nil
		case <-lookCtx.Done():
			if err := ctx.Err(); err != nil {
				return VisualObservation{}, err
			}
			return VisualObservation{Source: m.source, Status: VisualObservationUnavailable, Reason: VisualObservationReasonNoVideoTrack}, nil
		}
	}
}

func (m *pionInbound) ReadFrame(ctx context.Context) (sharedaudio.PCMFrame, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case frame := <-m.frames:
		return frame, nil
	case <-m.done:
		return sharedaudio.PCMFrame{}, io.EOF
	case <-ctx.Done():
		return sharedaudio.PCMFrame{}, ctx.Err()
	}
}
func (m *pionInbound) Close() error {
	m.once.Do(func() {
		close(m.done)
		if m.close != nil {
			_ = m.close()
		}
	})
	return nil
}
