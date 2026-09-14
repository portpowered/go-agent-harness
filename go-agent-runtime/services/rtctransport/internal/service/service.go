package service

import (
	rtctransport "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtctransport"
	core "github.com/portpowered/go-agent-harness/go-audio/pkg/rtctransport"
)

// Service contains no process state. Endpoint, codec, clock, and lifecycle
// state belongs to each core track returned from this boundary.
type Service struct{}

func New() *Service { return &Service{} }

func (s *Service) NewInboundTrack(source, opus any, config rtctransport.InboundTrackConfig) (rtctransport.InboundTrack, error) {
	return core.NewInboundTrack(source, opus, config)
}

func (s *Service) NewOutboundTrack(config rtctransport.OutboundTrackConfig) (rtctransport.OutboundTrack, error) {
	// Runtime callers historically receive strict 20 ms frames when the
	// duration field is omitted. The shared core keeps zero duration as its
	// source-compatible variable-frame mode for the legacy gateway adapter.
	if config.FrameDuration == 0 {
		config.FrameDuration = rtctransport.DefaultInboundFrameDuration
	}
	return core.NewOutboundTrack(config)
}
