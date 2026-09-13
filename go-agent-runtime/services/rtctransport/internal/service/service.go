package service

// Service contains the transport policy implementation. It has no process
// state; all endpoint, codec, clock, and lifecycle state belongs to a track.
type Service struct{}

func New() *Service { return &Service{} }
