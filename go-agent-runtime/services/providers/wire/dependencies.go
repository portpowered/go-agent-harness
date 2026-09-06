package wire

import (
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	"net/http"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/logging"
)

// Dependencies supplies host-owned infrastructure at the composition boundary.
// Provider service methods consume normalized provider requests instead.
type Dependencies struct {
	Recording  recording.Service
	HTTPClient *http.Client
	Logger     logging.Logger
	Clock      clock.TimerSource
}
