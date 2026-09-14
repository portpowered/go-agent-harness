package service

import "github.com/portpowered/go-agent-harness/go-agent-runtime/services/terminaloutcome"

// Service is an inert factory; all mutable state belongs to one reporter.
type Service struct{}

// New constructs the private terminal-outcome service.
func New() *Service { return &Service{} }

func (*Service) NewReporter() terminaloutcome.Reporter { return newReporter() }

func (*Service) HasIndependentFailure(err error, ignored ...error) bool {
	return hasIndependentFailure(err, ignored...)
}

var _ terminaloutcome.Service = (*Service)(nil)
