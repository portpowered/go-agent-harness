package agentruntime

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// writeSyntheticRecordingTranscript is retained for the unowned sight
// integration test. The recording implementation itself lives in the runtime
// package; this helper only exercises the CLI adapter's compatibility seam.
func writeSyntheticRecordingTranscript(t *testing.T, recording *sessionDirectoryRecording, client, agent string) {
	t.Helper()
	recording.observe(messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Role:  messages.RoleUser,
		Value: messages.NewTextDeltaValue(client),
	}, true)
	recording.observe(messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewTextDeltaValue(agent),
	}, false)
	recording.observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant}, false)
	if recording == nil || recording.session == nil {
		t.Fatal("recording session was not opened")
	}
}
