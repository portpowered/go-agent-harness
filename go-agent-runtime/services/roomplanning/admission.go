package roomplanning

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
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

type service struct{}

func newAdmissionService() *service { return &service{} }

// Await runs the admission barrier for the private service composition root.
// It is exported as a narrow function so the generated Wire package can keep
// the public Service implementation small without exposing coordinator state.
func Await(ctx context.Context, options AwaitOptions) error {
	return newAdmissionService().Await(ctx, options)
}

func (s *service) Await(ctx context.Context, options AwaitOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if options.Coordinator == nil {
		return errors.New("room planning admission coordinator is unavailable")
	}
	if options.Cleanup == nil {
		return errors.New("room planning cleanup waiter is unavailable")
	}
	if options.ParticipantError == nil {
		options.ParticipantError = func(_ string, err error) error { return err }
	}
	if options.LifecycleLabel == nil {
		options.LifecycleLabel = defaultLifecycleLabel
	}
	if options.LifecycleError == nil {
		options.LifecycleError = defaultLifecycleError
	}
	if options.AdmissionTimeout <= 0 {
		options.AdmissionTimeout = DefaultAdmissionTimeout
	}
	if options.CleanupTimeout <= 0 {
		options.CleanupTimeout = DefaultCleanupTimeout
	}
	byTracker := make(map[any]AdmissionParticipant, len(options.Participants))
	for _, participant := range options.Participants {
		if !nilAdmissionValue(participant.Tracker) && participant.StartupErr == nil {
			byTracker[participant.Tracker] = participant
		}
	}
	remaining := len(byTracker)
	seen := make(map[any]struct{}, remaining)
	ctxDone := ctx.Done()
	timerDone := options.Timer
	admissionDone := timerFor(options.TimerFactory, options.AdmissionTimeout)
	roomDone := options.Coordinator.Done()
	for remaining > 0 {
		select {
		case outcome, ok := <-options.Outcomes:
			if !ok {
				options.Outcomes = nil
				continue
			}
			participant, ok := byTracker[outcome.Tracker]
			if !ok {
				continue
			}
			if _, duplicate := seen[outcome.Tracker]; duplicate {
				continue
			}
			seen[outcome.Tracker] = struct{}{}
			remaining--
			if participant.MarkConnected != nil {
				participant.MarkConnected(outcome.Err)
			}
			if outcome.Err != nil {
				cause := fmt.Errorf("connect live session: %w", outcome.Err)
				options.Coordinator.FailParticipant(participant.ID, options.ParticipantError(participant.ID, cause))
			}
		case <-ctxDone:
			options.Coordinator.Stop(TerminationStopped)
			ctxDone = nil
		case <-timerDone:
			options.Coordinator.Stop(TerminationMaxDurationReached)
			timerDone = nil
		case <-admissionDone:
			outstanding := make([]string, 0, remaining)
			for tracker, participant := range byTracker {
				if _, done := seen[tracker]; done {
					continue
				}
				outstanding = append(outstanding, options.LifecycleLabel(participant.ID, "connect"))
			}
			options.Coordinator.Fail(options.LifecycleError(outstanding...))
			admissionDone = nil
			options.Cleanup.Start()
		case <-roomDone:
			roomDone = nil
			options.Cleanup.Start()
		case <-options.Cleanup.Done():
			return options.LifecycleError(admissionOutstanding(options, byTracker, seen)...)
		}
	}
	if options.Coordinator.IsStopping() {
		return nil
	}
	readinessDone := timerFor(options.TimerFactory, options.AdmissionTimeout)
	for {
		allReady := true
		for _, participant := range options.Participants {
			if participant.ID == "" || !options.Coordinator.IsActive(participant.ID) {
				continue
			}
			ready, err := participantReady(options.Coordinator, participant, options.ParticipantError)
			if err != nil {
				options.Coordinator.FailParticipant(participant.ID, err)
				continue
			}
			if !ready {
				allReady = false
			}
		}
		if allReady {
			return nil
		}
		select {
		case <-options.Coordinator.Done():
			return options.Coordinator.RoomError()
		case <-ctx.Done():
			options.Coordinator.Stop(TerminationStopped)
			return nil
		case <-options.Timer:
			options.Coordinator.Stop(TerminationMaxDurationReached)
			return nil
		case <-readinessDone:
			outstanding := make([]string, 0, len(options.Participants))
			for _, participant := range options.Participants {
				if participant.ID == "" || !options.Coordinator.IsActive(participant.ID) {
					continue
				}
				snapshot := LifecycleSnapshot{}
				if participant.Snapshot != nil {
					snapshot = participant.Snapshot()
				}
				if participant.Kind == rooms.ParticipantKindHuman {
					if !snapshot.DeviceReady {
						options.Coordinator.FailParticipant(participant.ID, options.ParticipantError(participant.ID, errors.New("human participant devices were not ready")))
					}
					continue
				}
				if !snapshot.Opened {
					outstanding = append(outstanding, participant.ID)
				}
			}
			for _, participantID := range outstanding {
				options.Coordinator.FailParticipant(participantID, options.ParticipantError(participantID, errors.New("session did not become ready before admission deadline")))
			}
			if options.Coordinator.IsStopping() {
				return nil
			}
		case <-options.Coordinator.Progress():
		}
	}
}

func participantReady(coordinator AdmissionCoordinator, participant AdmissionParticipant, failure func(string, error) error) (bool, error) {
	snapshot := LifecycleSnapshot{}
	if participant.Snapshot != nil {
		snapshot = participant.Snapshot()
	}
	if participant.Kind == rooms.ParticipantKindHuman {
		if snapshot.DeviceReady {
			return true, nil
		}
		if snapshot.RunFinished || coordinator.IsStopping() {
			return false, failure(participant.ID, errors.New("human participant devices were not ready"))
		}
		return false, nil
	}
	if snapshot.Opened {
		return true, nil
	}
	if snapshot.TransportEnded && !snapshot.RunFinished {
		return false, nil
	}
	if snapshot.Closed || snapshot.TransportEnded || snapshot.RunFinished {
		if snapshot.TerminalObserved && snapshot.TerminalErr != nil {
			return false, failure(participant.ID, snapshot.TerminalErr)
		}
		return false, failure(participant.ID, errors.New("session ended before SESSION.OPEN"))
	}
	return false, nil
}

func admissionOutstanding(options AwaitOptions, participants map[any]AdmissionParticipant, seen map[any]struct{}) []string {
	outstanding := make([]string, 0, len(participants))
	for tracker, participant := range participants {
		if _, done := seen[tracker]; !done {
			outstanding = append(outstanding, options.LifecycleLabel(participant.ID, "connect"))
		}
	}
	for _, participant := range options.Participants {
		if participant.OutstandingWork != nil {
			outstanding = append(outstanding, participant.OutstandingWork()...)
		}
	}
	return outstanding
}

func timerFor(factory func(time.Duration) <-chan time.Time, duration time.Duration) <-chan time.Time {
	if factory != nil {
		return factory(duration)
	}
	timer := time.NewTimer(duration)
	return timer.C
}

func nilAdmissionValue(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func defaultLifecycleLabel(participantID, phase string) string {
	if participantID == "" {
		return phase
	}
	return fmt.Sprintf("participant %q phase %s", participantID, phase)
}

func defaultLifecycleError(outstanding ...string) error {
	seen := make(map[string]struct{}, len(outstanding))
	ordered := make([]string, 0, len(outstanding))
	for _, item := range outstanding {
		if strings.TrimSpace(item) == "" {
			continue
		}
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		ordered = append(ordered, item)
	}
	if len(ordered) == 0 {
		return nil
	}
	sort.Strings(ordered)
	return fmt.Errorf("room lifecycle work did not complete: %s", strings.Join(ordered, "; "))
}
