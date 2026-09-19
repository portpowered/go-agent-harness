package agentruntime

import (
	"context"
	"testing"

	runtimeSession "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sd "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiondiagnostics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
)

var ErrSessionUnresolvedToolResults = sessionterminal.ErrUnresolvedToolResults
var ErrSessionImageContinuationIncomplete = runtimeSession.ErrLiveImageContinuationIncomplete
var ErrSessionToolContinuationIncomplete = runtimeSession.ErrLiveToolContinuationIncomplete
var ErrSessionAudioResponseIncomplete = runtimeSession.ErrLiveAudioResponseIncomplete

type SessionUnresolvedToolResultsError = sessionterminal.UnresolvedToolResultsError
type SessionImageContinuationError = runtimeSession.LiveImageContinuationError
type SessionToolContinuationError = runtimeSession.LiveToolContinuationError

func ensureTestLifecycleScheduled(t *testing.T, observer *sessionProgressObserver, count int) {
	t.Helper()
	if _, err := observer.ensureLifecycle().Apply(context.Background(), sd.Event{Kind: sd.EventEnsureScheduled, Count: count}); err != nil {
		t.Fatalf("ensure scheduled lifecycle slots: %v", err)
	}
}

func lifecycleSnapshotForTest(observer *sessionProgressObserver) sd.Snapshot {
	if observer == nil || observer.lifecycle == nil {
		return sd.Snapshot{}
	}
	return observer.lifecycle.Snapshot()
}

func completeTestScheduledLifecycles(t *testing.T, observer *sessionProgressObserver, count int) {
	t.Helper()
	ensureTestLifecycleScheduled(t, observer, count)
	for index := 0; index < count; index++ {
		id := "test-scheduled-response-" + string(rune('a'+index))
		if !observer.bindScheduledResponseID(index, id) {
			t.Fatalf("bind scheduled lifecycle %d", index)
		}
		observer.noteScheduledResponseDisposition(id, scheduledAudioResponseCompleted)
	}
}
