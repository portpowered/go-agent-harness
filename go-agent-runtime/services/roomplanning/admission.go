package roomplanning

import (
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

type TerminationReason string

const (
	TerminationStopped            TerminationReason = "stopped"
	TerminationMaxDurationReached TerminationReason = "max_duration_reached"
)

type LifecycleSnapshot struct {
	DeviceReady      bool
	Opened           bool
	Closed           bool
	TransportEnded   bool
	RunFinished      bool
	TerminalErr      error
	TerminalObserved bool
}

type AdmissionParticipant struct {
	ID              string
	Kind            rooms.ParticipantKind
	Tracker         any
	StartupErr      error
	MarkConnected   func(error)
	Snapshot        func() LifecycleSnapshot
	OutstandingWork func() []string
}

type ConnectionOutcome struct {
	Tracker any
	Err     error
}

type AdmissionCoordinator interface {
	Done() <-chan struct{}
	Progress() <-chan struct{}
	IsActive(string) bool
	IsStopping() bool
	Stop(TerminationReason)
	FailParticipant(string, error)
	Fail(error)
	RoomError() error
}

type AdmissionCleanup interface {
	Start()
	Done() <-chan time.Time
}

type AwaitOptions struct {
	Participants []AdmissionParticipant
	Outcomes     <-chan ConnectionOutcome
	Timer        <-chan time.Time
	Coordinator  AdmissionCoordinator
	Cleanup      AdmissionCleanup

	AdmissionTimeout time.Duration
	CleanupTimeout   time.Duration
	TimerFactory     func(time.Duration) <-chan time.Time
	ParticipantError func(participantID string, err error) error
	LifecycleLabel   func(participantID, phase string) string
	LifecycleError   func(...string) error
}
