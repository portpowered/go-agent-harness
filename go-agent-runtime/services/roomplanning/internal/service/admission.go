package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomplanning"
)

type admissionState struct {
	ctx           context.Context
	options       roomplanning.AwaitOptions
	byTracker     map[any]roomplanning.AdmissionParticipant
	seen          map[any]struct{}
	remaining     int
	ctxDone       <-chan struct{}
	timerDone     <-chan time.Time
	admissionDone <-chan time.Time
	roomDone      <-chan struct{}
}

func awaitAdmission(ctx context.Context, options roomplanning.AwaitOptions) error {
	state, err := newAdmissionState(ctx, options)
	if err != nil {
		return err
	}
	if err := state.awaitConnections(); err != nil {
		return err
	}
	if state.options.Coordinator.IsStopping() {
		return nil
	}
	return state.awaitReadiness()
}

func newAdmissionState(ctx context.Context, options roomplanning.AwaitOptions) (*admissionState, error) {
	if options.Coordinator == nil {
		return nil, errors.New("room planning admission coordinator is unavailable")
	}
	if options.Cleanup == nil {
		return nil, errors.New("room planning cleanup waiter is unavailable")
	}
	applyAdmissionDefaults(&options)
	byTracker := admissionTrackers(options.Participants)
	return &admissionState{
		ctx: ctx, options: options, byTracker: byTracker,
		seen: make(map[any]struct{}, len(byTracker)), remaining: len(byTracker),
		ctxDone: ctx.Done(), timerDone: options.Timer,
		admissionDone: timerFor(options.TimerFactory, options.AdmissionTimeout),
		roomDone:      options.Coordinator.Done(),
	}, nil
}

func applyAdmissionDefaults(options *roomplanning.AwaitOptions) {
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
		options.AdmissionTimeout = roomplanning.DefaultAdmissionTimeout
	}
	if options.CleanupTimeout <= 0 {
		options.CleanupTimeout = roomplanning.DefaultCleanupTimeout
	}
}

func admissionTrackers(participants []roomplanning.AdmissionParticipant) map[any]roomplanning.AdmissionParticipant {
	byTracker := make(map[any]roomplanning.AdmissionParticipant, len(participants))
	for _, participant := range participants {
		if !nilAdmissionValue(participant.Tracker) && participant.StartupErr == nil {
			byTracker[participant.Tracker] = participant
		}
	}
	return byTracker
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
	case reflect.Invalid, reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128, reflect.Array, reflect.String,
		reflect.Struct, reflect.UnsafePointer:
		return false
	}
	return false
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
