package service

import (
	"errors"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiooutput"
)

// Service is the stateless audio-output factory assembled by Wire.
type Service struct{}

func New() *Service { return &Service{} }

func (*Service) Open(config audiooutput.Config) (audiooutput.Output, error) {
	return newOutput(config)
}

func (*Service) Wrap(inner messages.SessionInferencer, output audiooutput.Output, options audiooutput.SessionOptions) audiooutput.SessionInferencer {
	return newInferencer(inner, output, options)
}

func validateSessionDependencies(inner messages.SessionInferencer, output audiooutput.Output) error {
	if inner == nil {
		return errors.New("audio output session inferencer is nil")
	}
	if output == nil {
		return errors.New("audio output session output is nil")
	}
	return nil
}

var _ audiooutput.Service = (*Service)(nil)
