package agentruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audioiowire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	recordingwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording/wire"
	runtimereplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	replaywire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func newTestReplayService() runtimereplay.Service { return replaywire.NewService() }

func newTestRecordingService() recording.Service { return recordingwire.NewService(clock.Real{}) }

func newTestProviderCaptureService() recording.ProviderCaptureService {
	return recordingwire.NewProviderCaptureService(clock.Real{})
}

func withTestRecordingServices(options SessionRunOptions) SessionRunOptions {
	options.RecordingService = newTestRecordingService()
	options.ProviderCaptureService = newTestProviderCaptureService()
	if options.AudioService == nil {
		options.AudioService = audioiowire.NewService()
	}
	return options
}

// sessionDirectoryRecording is test support for the preserved browser
// behavior test. Its observations and artifact persistence go through the
// public recording service contract.
type sessionDirectoryRecording struct {
	destination string
	browser     *sessionBrowserRecording
	live        session.LiveRecorder
}

func newSessionDirectoryRecording(destination string, plan sessionRuntimePlan, opts SessionRunOptions) *sessionDirectoryRecording {
	plan.clockSource = clock.Ensure(plan.clockSource)
	service := recordingwire.NewService(plan.clockSource)
	live, err := service.OpenLiveEvidence(recording.LiveEvidenceOptions{
		Destination: destination, Provider: plan.provider, Model: plan.model,
		OutputAudioRate: plan.outputAudioSampleRate, ClockBase: plan.clockSource.Now(),
		Credentials: []string{opts.APIKey},
	})
	if err != nil {
		panic(err)
	}
	return &sessionDirectoryRecording{destination: destination, browser: newSessionBrowserRecording(opts, plan), live: live}
}

func (r *sessionDirectoryRecording) Finalize() error {
	if r == nil {
		return nil
	}
	if r.browser != nil {
		r.browser.stop()
		artifact, err := r.browser.artifact()
		if err != nil {
			return errors.Join(err, r.live.Finalize(context.Background(), err))
		}
		if artifact != nil {
			browserRecorder, ok := r.live.(recording.BrowserArtifactRecorder)
			if !ok {
				return errors.Join(errors.New("recording service cannot persist browser artifacts"), r.live.Finalize(context.Background(), nil))
			}
			if err := browserRecorder.RecordBrowserArtifact(context.Background(), artifact); err != nil {
				return errors.Join(err, r.live.Finalize(context.Background(), err))
			}
		}
	}
	if err := r.live.RecordEvent(context.Background(), session.LiveEvent{
		Timestamp: time.Unix(0, 0).UTC(),
		Kind:      string(session.LiveEventTerminal),
		Terminal: messages.NewSessionCloseValueWithTerminal(
			"fixture", "completed", "completed", messages.TerminalReasonProviderAuthoredCompletion,
			messages.TerminalProvenanceProvider, messages.TerminalOutputComplete,
		),
	}); err != nil {
		return errors.Join(err, r.live.Finalize(context.Background(), err))
	}
	return r.live.Finalize(context.Background(), nil)
}

func writeSyntheticRecordingTranscript(t *testing.T, recorder *sessionDirectoryRecording, client, agent string) {
	t.Helper()
	base := time.Unix(0, 0).UTC()
	if err := recorder.live.RecordMessage(t.Context(), session.LiveRecord{
		Direction: session.LiveRecordClient, Timestamp: base,
		Message: messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleUser, Value: messages.NewTextDeltaValue(client)},
	}); err != nil {
		t.Fatalf("record synthetic client transcript: %v", err)
	}
	if err := recorder.live.RecordMessage(t.Context(), session.LiveRecord{
		Direction: session.LiveRecordAgent, Timestamp: base.Add(time.Nanosecond),
		Message: messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(agent)},
	}); err != nil {
		t.Fatalf("record synthetic agent transcript: %v", err)
	}
}
