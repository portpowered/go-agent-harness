package livehost

import (
	"errors"
	"fmt"
	"io"
	"testing"

	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

func TestDurationExitTranslationPreservesIndependentFailures(t *testing.T) {
	recordingFailure := errors.New("recording publication failed")
	duration := runtimeSession.ErrLiveDurationExceeded
	for _, test := range []struct {
		name string
		err  error
		want error
	}{
		{name: "success"},
		{name: "duration", err: duration},
		{name: "wrapped duration", err: fmt.Errorf("session: %w", duration)},
		{name: "independent failure", err: recordingFailure, want: recordingFailure},
		{name: "joined failure", err: errors.Join(duration, recordingFailure), want: recordingFailure},
		{name: "wrapped joined failure", err: fmt.Errorf("session: %w", errors.Join(duration, recordingFailure)), want: recordingFailure},
		{name: "nested joined failure", err: errors.Join(fmt.Errorf("session: %w", errors.Join(duration, recordingFailure)), duration), want: recordingFailure},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := suppressExpectedDuration(test.err)
			if test.want == nil {
				if got != nil {
					t.Fatalf("expected normal process exit, got %v", got)
				}
				return
			}
			if !errors.Is(got, test.want) {
				t.Fatalf("process exit lost independent failure: got %v, want %v", got, test.want)
			}
		})
	}
}

func TestDurationExitTranslationPreservesTypedScheduleEvidence(t *testing.T) {
	schedule := &runtimeSession.LiveScheduledAudioIncompleteError{Completed: 1, Dispatched: 2, Scheduled: 3}
	for _, cause := range []error{schedule, errors.Join(runtimeSession.ErrLiveDurationExceeded, schedule)} {
		got := suppressExpectedDuration(cause)
		var retained *runtimeSession.LiveScheduledAudioIncompleteError
		if !errors.As(got, &retained) || retained != schedule {
			t.Fatalf("process exit lost typed schedule evidence: %v", got)
		}
	}
}

type capturingFileMediaService struct {
	requests []runtimeDevices.FileMediaRequest
}

func (s *capturingFileMediaService) OpenFileMedia(request runtimeDevices.FileMediaRequest) (runtimeDevices.FileMediaHandle, error) {
	s.requests = append(s.requests, request)
	return nil, nil
}

// TestOpenFileMediaCarriesTheRequestedInputPacing proves the host maps the
// session's file-input pacing onto the runtime file media request unchanged,
// so the zero value keeps real-time delivery.
func TestOpenFileMediaCarriesTheRequestedInputPacing(t *testing.T) {
	for _, pacing := range []runtimeDevices.FilePacing{{}, {Unpaced: true}, {Speed: 20}} {
		service := &capturingFileMediaService{}
		request := serviceSession.Request{AudioTurns: []string{"turn.wav"}, AudioInputPacing: pacing}
		if _, err := openFileMedia(request, io.Discard, runtimeSession.LiveRequest{}, Dependencies{FileMediaService: service}, nil); err != nil {
			t.Fatalf("open file media with pacing %+v: %v", pacing, err)
		}
		if len(service.requests) != 1 || service.requests[0].Pacing != pacing {
			t.Fatalf("file media requests = %+v, want one carrying pacing %+v", service.requests, pacing)
		}
	}
}
