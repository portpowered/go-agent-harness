package agentruntime

import (
	"bytes"
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestObservedSessionCommitExcludesLaterBufferAdmissions(t *testing.T) {
	observer := &recordingSessionRuntimeObserver{}
	recorder := newSessionRuntimeObservationRecorder(observer, nil)
	session := &observedSession{Session: newRoomTestSession(), runtime: recorder}
	first, second := []byte{1, 2, 3, 4}, []byte{5, 6, 7, 8}
	// Both frames can enter the core FIFO before its worker sends the first
	// commit. Evidence must follow actual session sends, not these admissions.
	recorder.audioInput(first)
	recorder.audioInput(second)
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
	recorder := newSessionRuntimeObservationRecorder(observer, nil)
	session := &observedSession{Session: rejectObservedAudioSession{Session: newRoomTestSession()}, runtime: recorder}
	pcm := []byte{1, 2}
	recorder.audioInput(pcm)
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
