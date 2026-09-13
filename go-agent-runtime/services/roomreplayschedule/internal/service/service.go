package service

import (
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplayschedule"
)

// Service is the private deterministic room replay implementation.
type Service struct{}

var _ roomreplayschedule.Service = (*Service)(nil)

// New constructs a replay scheduling service without opening any files.
func New() *Service { return &Service{} }
