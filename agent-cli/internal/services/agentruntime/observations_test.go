package agentruntime

import (
	"testing"

	sessionobservation "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionobservation"
)

func TestDeprecatedObservationAliasesPreservePublicContract(t *testing.T) {
	if SessionRuntimeObservationAudioOutput != sessionobservation.SessionRuntimeObservationAudioOutput || SessionRuntimeObservationAudioPlaybackReceipt != sessionobservation.SessionRuntimeObservationAudioPlaybackReceipt || SessionRuntimeObservationTerminal != sessionobservation.SessionRuntimeObservationTerminal {
		t.Fatal("legacy observation aliases changed vocabulary")
	}
	var observer SessionRuntimeObserver = aliasObserver{}
	var _ sessionobservation.SessionRuntimeObserver = observer
	if (SessionRuntimeObservation{Kind: SessionRuntimeObservationInputCommit}).Kind != sessionobservation.SessionRuntimeObservationInputCommit {
		t.Fatal("legacy observation record is not the public record")
	}
}

type aliasObserver struct{}

func (aliasObserver) ObserveSessionRuntime(SessionRuntimeObservation) {}
