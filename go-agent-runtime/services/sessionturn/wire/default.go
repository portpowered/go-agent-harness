//go:build !wireinject

package wire

import "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"

func NewDefaultService() sessionturn.Service {
	return NewService(Dependencies{})
}
