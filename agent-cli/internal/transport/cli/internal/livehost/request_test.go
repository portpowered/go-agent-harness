package livehost

import (
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
)

func TestAssembleLiveRequestWaitForCloseOverridesFiniteAudioPolicy(t *testing.T) {
	inputs := requestInputs{
		effective: config.Config{},
		provider:  "openai",
		model:     "gpt-realtime",
	}
	for _, test := range []struct {
		name          string
		waitForClose  bool
		wantFinite    bool
		wantResponses int
	}{
		{name: "finite audio", wantFinite: true, wantResponses: 1},
		{name: "provider owns close", waitForClose: true, wantFinite: false, wantResponses: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := serviceSession.Request{
				WaitForClose: test.waitForClose,
				AudioInput:   serviceSession.AudioInput{Present: true},
			}
			got := assembleLiveRequest(request, inputs)
			if got.FinishAfterResponse != test.wantFinite {
				t.Fatalf("FinishAfterResponse = %t, want %t", got.FinishAfterResponse, test.wantFinite)
			}
			if got.ExpectedResponses != test.wantResponses {
				t.Fatalf("ExpectedResponses = %d, want %d", got.ExpectedResponses, test.wantResponses)
			}
		})
	}
}
