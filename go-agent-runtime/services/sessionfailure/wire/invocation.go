package wire

import (
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sd "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiondiagnostics"
	sf "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfailure"
)

// Invocation carries one failure service beside an existing lifecycle
// service. It lets a host cache the failure state without changing its
// lifecycle interface or importing the private implementation package.
type Invocation struct {
	sd.Service
	Failure  sf.Service
	publish  func(sf.Observation) bool
	rollback func()
}

func NewInvocation(lifecycle sd.Service, dependencies sf.Dependencies, publish func(sf.Observation) bool, rollback func()) *Invocation {
	dependencies.Publish = nil
	return &Invocation{Service: lifecycle, Failure: NewService(dependencies), publish: publish, rollback: rollback}
}

// Accept delegates first acceptance to the private service, then forwards the
// immutable accepted observation to the host. The service remains authoritative
// when the host rejects notification and is cleared before the invocation can
// accept a later observation.
func (i *Invocation) Accept(facts sf.Facts, err error) bool {
	if i == nil || i.Failure == nil || !i.Failure.Accept(facts, err) {
		return false
	}
	observation := i.Failure.Snapshot()
	if observation != nil && (i.publish == nil || i.publish(*observation)) {
		return true
	}
	i.Failure.Clear()
	if i.rollback != nil {
		i.rollback()
	}
	return false
}

func (i *Invocation) AcceptError(value *messages.ErrorValue) bool {
	if i == nil || i.Failure == nil {
		return false
	}
	facts, err := i.Failure.NormalizeErrorValue(value)
	return i.Accept(facts, err)
}

func (i *Invocation) AcceptClose(value *messages.SessionCloseValue, progress sf.Progress) bool {
	return i != nil && i.Failure != nil && i.Accept(i.Failure.NormalizeClose(value, progress), nil)
}

func (i *Invocation) Clear() {
	if i == nil {
		return
	}
	i.Failure.Clear()
	if i.rollback != nil {
		i.rollback()
	}
}

var projectionKinds = [...]sf.Projection{sf.ProjectionUnresolvedTool, sf.ProjectionImageContinuation, sf.ProjectionToolContinuation, sf.ProjectionScheduledAudio}

func Facts(kind int, event string, progress sf.Progress) sf.Facts {
	return NewService(sf.Dependencies{}).Projection(projectionKinds[kind], event, progress)
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
