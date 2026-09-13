package wire

import (
	sd "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiondiagnostics"
	sf "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfailure"
)

// Invocation carries one failure service beside an existing lifecycle
// service. It lets a host cache the failure state without changing its
// lifecycle interface or importing the private implementation package.
type Invocation struct {
	sd.Service
	Failure sf.Service
}

// NewInvocation assembles one invocation-local failure service beside the
// existing lifecycle service. State and policy remain in the service returned
// by NewService; this package only provides construction and value helpers.
func NewInvocation(lifecycle sd.Service, dependencies sf.Dependencies) *Invocation {
	return &Invocation{Service: lifecycle, Failure: NewService(dependencies)}
}

func Facts(kind sf.Projection, event string, progress sf.Progress) sf.Facts {
	return NewService(sf.Dependencies{}).Projection(kind, event, progress)
}

func OutputState(progress sf.Progress) string {
	return NewService(sf.Dependencies{}).OutputState(progress)
}

func Progress(sessionOpened bool, turnsCompleted int) sf.Progress {
	return sf.Progress{SessionOpened: sessionOpened, TurnsCompleted: turnsCompleted}
}

func OutputStateForProgress(sessionOpened bool, turnsCompleted int) string {
	return OutputState(Progress(sessionOpened, turnsCompleted))
}

func RunFacts(err error) sf.Facts {
	facts := NewService(sf.Dependencies{}).FactsFromSessionRunError(err)
	if facts == nil {
		return sf.Facts{}
	}
	return *facts
}
