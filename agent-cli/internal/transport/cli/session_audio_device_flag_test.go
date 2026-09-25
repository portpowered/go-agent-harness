package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
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
			root := newTestRootCommandWithProbeFleetCommand(NewProbeFleetCommand(nil, nil, newReplayRuntimeServiceForTest()), inferencer)
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

// TestSessionAudioInPacingFlagReachesTheSessionRequest proves the hidden
// test-harness option parses every documented spelling, rejects an invalid
// one at flag parsing, and is carried onto the session request the live host
// maps onto the runtime file media request.
func TestSessionAudioInPacingFlagReachesTheSessionRequest(t *testing.T) {
	for _, testCase := range []struct {
		args []string
		want runtimeDevices.FilePacing
	}{
		{args: nil, want: runtimeDevices.FilePacing{}},
		{args: []string{"--audio-in-pacing=realtime"}, want: runtimeDevices.FilePacing{}},
		{args: []string{"--audio-in-pacing=unpaced"}, want: runtimeDevices.FilePacing{Unpaced: true}},
		{args: []string{"--audio-in-pacing", "20x"}, want: runtimeDevices.FilePacing{Speed: 20}},
	} {
		command := NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil)
		cmd := command.Generate()
		if err := cmd.ParseFlags(testCase.args); err != nil {
			t.Fatalf("parse %v: %v", testCase.args, err)
		}
		request, err := command.buildSessionRequest(cmd, nil, sessionCommandRunState{}, SessionTransportWebSocket, false, false, false, nil, nil)
		if err != nil {
			t.Fatalf("build request for %v: %v", testCase.args, err)
		}
		if request.AudioInputPacing != testCase.want {
			t.Fatalf("%v request pacing = %+v, want %+v", testCase.args, request.AudioInputPacing, testCase.want)
		}
	}

	cmd := NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil).Generate()
	err := cmd.ParseFlags([]string{"--audio-in-pacing=fast"})
	const want = `invalid argument "fast" for "--audio-in-pacing" flag: invalid device media request: file input pacing "fast": want realtime, unpaced, or a speed such as 10x`
	if err == nil || err.Error() != want {
		t.Fatalf("--audio-in-pacing=fast error = %v, want %q", err, want)
	}
	assertSessionFlagHidden(t, cmd, sessionAudioInPacingFlag)
}

func assertSessionFlagHidden(t *testing.T, cmd *cobra.Command, name string) {
	t.Helper()
	flag := cmd.Flags().Lookup(name)
	if flag == nil || !flag.Hidden {
		t.Fatalf("--%s = %+v, want a registered hidden flag", name, flag)
	}
}
