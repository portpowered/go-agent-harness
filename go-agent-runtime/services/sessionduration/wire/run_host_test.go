package wire

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

func queuedPublicLoop(ctx context.Context, t *testing.T, queued ...messages.StreamMessage) *publicLoop {
	t.Helper()
	loop := &publicLoop{deltas: messages.NewTypedBuffer[messages.StreamMessage](len(queued) + 1)}
	for _, msg := range queued {
		if !loop.deltas.Write(ctx, msg) {
			t.Fatal("test message was not queued")
		}
	}
	return loop
}

func publicLoopFactory(loop sessionduration.Loop) sessionduration.LoopFactory {
	return func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
		return loop, nil
	}
}

func TestPublicRunDispatchesHostClaimedRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loop := queuedPublicLoop(context.Background(), t, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, ResponseID: "resp-1", Value: &messages.MessageEndValue{Status: "failed"}})
	var claimed string
	dispatched := make(chan messages.StreamMessage, 1)
	result := make(chan error, 1)
	go func() {
		result <- NewService().Run(sessionduration.RunRequest{
			Context: ctx, Inferencer: publicInferencer{session: newPublicSession()}, LoopFactory: publicLoopFactory(loop),
			RetryClaim: func(responseID string, _ *messages.MessageEndValue) (time.Duration, bool) {
				claimed = responseID
				return 0, true
			},
			RetryDispatched: func(msg messages.StreamMessage) { dispatched <- msg; cancel() },
		})
	}()
	select {
	case msg := <-dispatched:
		if msg.Type != messages.StreamTypeResponseCreate {
			t.Fatalf("retry dispatch = %s, want response.create", msg.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("host-claimed retry was not dispatched")
	}
	if err := waitPublicRun(t, result); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() = %v, want caller cancellation", err)
	}
	if claimed != "resp-1" || loop.sentEvents != 1 {
		t.Fatalf("retry claim = %q sent=%d, want resp-1 and one dispatch", claimed, loop.sentEvents)
	}
}

func TestPublicRunFansInHostErrorAndDoneSources(t *testing.T) {
	failure := errors.New("device pump failed")
	errorsSource := make(chan error, 1)
	errorsSource <- failure
	err := NewService().Run(sessionduration.RunRequest{
		Context: context.Background(), Inferencer: publicInferencer{session: newPublicSession()}, LoopFactory: publicLoopFactory(queuedPublicLoop(context.Background(), t)),
		ExternalErrors:       make(chan error),
		ExternalErrorSources: func() []<-chan error { return []<-chan error{nil, errorsSource} },
	})
	if !errors.Is(err, failure) {
		t.Fatalf("Run() = %v, want host source failure", err)
	}
	doneErr := errors.New("provider completed")
	providerDone := make(chan struct{})
	close(providerDone)
	err = NewService().Run(sessionduration.RunRequest{
		Context: context.Background(), Inferencer: publicInferencer{session: newPublicSession()}, LoopFactory: publicLoopFactory(queuedPublicLoop(context.Background(), t)),
		Done:        make(chan struct{}),
		DoneSources: func() []<-chan struct{} { return []<-chan struct{}{providerDone} },
		DoneError:   func() error { return doneErr },
	})
	if !errors.Is(err, doneErr) {
		t.Fatalf("Run() = %v, want host completion error", err)
	}
}

func TestPublicRunBoundsSessionUpdatedAcknowledgement(t *testing.T) {
	timeoutErr := errors.New("configuration was not acknowledged")
	open := messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("session", "model")}
	scheduler := &publicManualScheduler{created: make(chan *publicManualTimer, 2)}
	result := make(chan error, 1)
	go func() {
		result <- NewService().Run(sessionduration.RunRequest{
			Context: context.Background(), Inferencer: publicInferencer{session: newPublicSession()}, Clock: scheduler,
			LoopFactory:    publicLoopFactory(queuedPublicLoop(context.Background(), t, open)),
			SessionUpdated: sessionduration.SessionUpdatedWait{Timeout: time.Second, Pending: func() bool { return true }, Ready: func() bool { return false }, TimeoutError: timeoutErr},
			DrainPolicy:    sessionduration.DrainPolicy{WallSafety: time.Millisecond},
		})
	}()
	receivePublicTimer(t, scheduler).Fire()
	if err := waitPublicRun(t, result); !errors.Is(err, timeoutErr) {
		t.Fatalf("Run() = %v, want acknowledgement timeout", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	scheduler = &publicManualScheduler{created: make(chan *publicManualTimer, 2)}
	go func() {
		result <- NewService().Run(sessionduration.RunRequest{
			Context: ctx, Inferencer: publicInferencer{session: newPublicSession()}, Clock: scheduler,
			LoopFactory: publicLoopFactory(queuedPublicLoop(ctx, t, open)),
			Handle: func(context.Context, sessionduration.Loop, sessionduration.Controller, messages.StreamMessage) (sessionduration.MessageResult, error) {
				return sessionduration.MessageResult{}, nil
			},
			SessionUpdated: sessionduration.SessionUpdatedWait{Timeout: time.Second, Ready: func() bool { return true }, TimeoutError: timeoutErr},
			DrainPolicy:    sessionduration.DrainPolicy{WallSafety: time.Millisecond},
		})
	}()
	receivePublicTimer(t, scheduler)
	cancel()
	if err := waitPublicRun(t, result); !errors.Is(err, context.Canceled) || errors.Is(err, timeoutErr) {
		t.Fatalf("Run() = %v, want acknowledged configuration and caller cancellation", err)
	}
}

func TestPublicRunSessionUpdatedRequiresScheduler(t *testing.T) {
	open := messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("session", "model")}
	for name, clock := range map[string]sessionduration.TimerScheduler{"missing": nil, "nil timer": nilTimerScheduler{}} {
		t.Run(name, func(t *testing.T) {
			err := NewService().Run(sessionduration.RunRequest{
				Context: context.Background(), Inferencer: publicInferencer{session: newPublicSession()}, Clock: clock,
				LoopFactory:    publicLoopFactory(queuedPublicLoop(context.Background(), t, open)),
				SessionUpdated: sessionduration.SessionUpdatedWait{Timeout: time.Second},
				DrainPolicy:    sessionduration.DrainPolicy{Clock: &publicManualScheduler{created: make(chan *publicManualTimer, 1)}, WallSafety: time.Millisecond},
			})
			if !errors.Is(err, sessionduration.ErrSchedulerUnavailable) {
				t.Fatalf("Run() = %v, want unavailable scheduler", err)
			}
		})
	}
}

type nilTimerScheduler struct{}

func (nilTimerScheduler) NewTimer(time.Duration) sessionduration.Timer { return nil }

// closingPublicLoop models an agent loop that closes the provider session it
// connected when the run context ends.
type closingPublicLoop struct {
	*publicLoop
	session messages.Session
}

func (l *closingPublicLoop) Run(ctx context.Context) error {
	<-ctx.Done()
	return l.session.Close()
}

func TestPublicRunAwaitsAdmissionCloseAndReportsCompletion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	closeErr := errors.New("provider close failed")
	completionErr := errors.New("completion publication failed")
	session := newPublicSession()
	var observed sessionduration.Result
	var observedErr error
	result := make(chan error, 1)
	go func() {
		result <- NewService().Run(sessionduration.RunRequest{
			Context: ctx, Inferencer: closingInferencer{session: session, closeErr: closeErr}, AwaitAdmissionClose: true,
			LoopFactory: func(ctx context.Context, inferencer sessionduration.AdmissionInferencer, _ sessionduration.Controller) (sessionduration.Loop, error) {
				connected, err := inferencer.ConnectSession(ctx)
				if err != nil {
					return nil, err
				}
				cancel()
				return &closingPublicLoop{publicLoop: queuedPublicLoop(ctx, t), session: connected}, nil
			},
			Completion: func(result sessionduration.Result, err error) error {
				observed, observedErr = result, err
				return completionErr
			},
		})
	}()
	err := waitPublicRun(t, result)
	if !errors.Is(err, closeErr) || !errors.Is(err, completionErr) || !errors.Is(observedErr, closeErr) {
		t.Fatalf("Run() = %v (completion saw %v), want admitted close failure and completion error", err, observedErr)
	}
	if observed.Expired || observed.OutputState != messages.TerminalOutputNone {
		t.Fatalf("completion result = %+v, want unexpired run without output", observed)
	}
}

type closingInferencer struct {
	session  *publicSession
	closeErr error
}

func (i closingInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return closingSession{publicSession: i.session, err: i.closeErr}, nil
}

type closingSession struct {
	*publicSession
	err error
}

func (s closingSession) Close() error {
	_ = s.publicSession.Close() //nolint:errcheck // The public session close cannot fail.
	return s.err
}
