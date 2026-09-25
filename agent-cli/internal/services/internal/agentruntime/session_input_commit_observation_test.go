package agentruntime

import (
	"bytes"
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sessiontracewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/wire"
)

func TestObservedSessionCommitExcludesLaterBufferAdmissions(t *testing.T) {
	observer := &recordingSessionRuntimeObserver{}
	recorder := sessiontracewire.NewRuntimeRecorder(observer, nil)
	session := &observedSession{Session: newCommitTestSession(), runtime: recorder}
	first, second := []byte{1, 2, 3, 4}, []byte{5, 6, 7, 8}
	// Both frames can enter the core FIFO before its worker sends the first
	// commit. Evidence must follow actual session sends, not these admissions.
	recorder.AudioInput(first)
	recorder.AudioInput(second)
	for _, pcm := range [][]byte{first, second} {
		if !session.Send(context.Background(), messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue(pcm)}) {
			t.Fatal("audio send failed")
		}
		if !session.Send(context.Background(), messages.StreamMessage{Type: messages.StreamTypeMessageEnd}) {
			t.Fatal("commit send failed")
		}
	}
	var commits []SessionRuntimeObservation
	for _, event := range observer.observations {
		if event.Kind == SessionRuntimeObservationInputCommit {
			commits = append(commits, event)
		}
	}
	if len(commits) != 2 {
		t.Fatalf("commits = %d, want 2", len(commits))
	}
	for i, want := range [][]byte{first, second} {
		if commits[i].InputCommit != i+1 || !bytes.Equal(commits[i].Payload, want) {
			t.Fatalf("commit %d = ordinal %d payload %v, want %v", i+1, commits[i].InputCommit, commits[i].Payload, want)
		}
	}
}

type rejectObservedAudioSession struct{ messages.Session }

func (s rejectObservedAudioSession) Send(_ context.Context, msg messages.StreamMessage) bool {
	return msg.Type != messages.StreamTypeAudioDelta
}

func TestObservedSessionCommitExcludesRejectedAudio(t *testing.T) {
	observer := &recordingSessionRuntimeObserver{}
	recorder := sessiontracewire.NewRuntimeRecorder(observer, nil)
	session := &observedSession{Session: rejectObservedAudioSession{Session: newCommitTestSession()}, runtime: recorder}
	pcm := []byte{1, 2}
	recorder.AudioInput(pcm)
	if session.Send(context.Background(), messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue(pcm)}) {
		t.Fatal("rejected audio reported success")
	}
	if !session.Send(context.Background(), messages.StreamMessage{Type: messages.StreamTypeMessageEnd}) {
		t.Fatal("commit rejected")
	}
	for _, event := range observer.observations {
		if event.Kind == SessionRuntimeObservationInputCommit {
			if len(event.Payload) != 0 {
				t.Fatalf("rejected audio included in commit: %v", event.Payload)
			}
			return
		}
	}
	t.Fatal("commit evidence missing")
}

// commitTestSession accepts every send; the observed-session wrapper under
// test owns commit evidence, not this transport stand-in.
type commitTestSession struct {
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
}

func newCommitTestSession() *commitTestSession {
	return &commitTestSession{receive: messages.NewTypedBuffer[messages.StreamMessage](1), done: make(chan struct{})}
}

func (*commitTestSession) Send(context.Context, messages.StreamMessage) bool { return true }

func (s *commitTestSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *commitTestSession) Done() <-chan struct{} { return s.done }

func (*commitTestSession) TerminalError() error { return nil }

func (*commitTestSession) Close() error { return nil }
