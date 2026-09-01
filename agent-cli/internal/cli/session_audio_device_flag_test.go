package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestSessionAudioOutputDeviceFlagErrors(t *testing.T) {
	tests := []struct {
		name             string
		args             []string
		wantError        string
		wantSuggestion   bool
		rejectSuggestion bool
	}{
		{
			name:           "transposed flag suggests canonical spelling",
			args:           []string{"session", "--audio-device-out", "virtual:output"},
			wantError:      "unknown flag: --audio-device-out (did you mean --audio-out-device?)",
			wantSuggestion: true,
		},
		{
			name:      "canonical flag remains accepted",
			args:      []string{"session", "--audio-out-device", "virtual:output", "--transport", "webrtc", "--signaling", "wss://example.test"},
			wantError: (&SessionWebRTCUnavailableError{}).Error(),
		},
		{
			name:             "unrelated flag keeps normal error",
			args:             []string{"session", "--unknown-session-flag", "value"},
			wantError:        "unknown flag: --unknown-session-flag",
			rejectSuggestion: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inferencer := &flagErrorSessionInferencer{}
			root := newTestRootCommandWithProbeFleetCommand(NewProbeFleetCommand(), inferencer)
			var stdout, stderr bytes.Buffer
			root.SetOut(&stdout)
			root.SetErr(&stderr)
			root.SetArgs(tt.args)

			err := root.ExecuteContext(context.Background())
			if err == nil || err.Error() != tt.wantError {
				t.Fatalf("error = %v, want %q", err, tt.wantError)
			}
			if inferencer.connects.Load() != 0 {
				t.Fatalf("session connects = %d, want no session startup", inferencer.connects.Load())
			}
			if tt.wantSuggestion && !strings.Contains(err.Error(), "--audio-out-device") {
				t.Fatalf("transposed flag error = %q, want canonical flag suggestion", err)
			}
			if tt.rejectSuggestion && strings.Contains(err.Error(), "--audio-out-device") {
				t.Fatalf("unrelated flag error = %q, must not suggest audio output device", err)
			}
		})
	}
}

type flagErrorSessionInferencer struct {
	connects atomic.Int32
}

func (i *flagErrorSessionInferencer) ConnectSession(context.Context) (messages.Session, error) {
	i.connects.Add(1)
	return nil, errors.New("unexpected session startup")
}

var _ messages.SessionInferencer = (*flagErrorSessionInferencer)(nil)
