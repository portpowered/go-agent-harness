package roommedia

import (
	"reflect"
	"strings"
	"testing"
)

func TestRoomMediaRoutesPeersAndDiscardsStaleEpoch(t *testing.T) {
	report, err := Run(Request{Mode: "routing-epochs", OutputDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if report.Status != "complete" {
		t.Fatalf("status = %q, want complete", report.Status)
	}
	if got, want := report.PeerOutputs["alice"], []int16{211, 212, 213, 214}; !reflect.DeepEqual(got, want) {
		t.Fatalf("alice peer output = %v, want %v", got, want)
	}
	if got, want := report.PeerOutputs["bob"], []int16{111, 112, 113, 114}; !reflect.DeepEqual(got, want) {
		t.Fatalf("bob peer output = %v, want %v", got, want)
	}
	wantInputs := []InputObservation{
		{Participant: "alice", Epoch: 1, Sequence: 1, Samples: []int16{101, 102}},
		{Participant: "alice", Epoch: 2, Sequence: 2, Samples: []int16{111, 112, 113, 114}, End: true},
		{Participant: "bob", Epoch: 1, Sequence: 1, Samples: []int16{201, 202}},
		{Participant: "bob", Epoch: 2, Sequence: 2, Samples: []int16{211, 212, 213, 214}, End: true},
	}
	if !reflect.DeepEqual(report.SourceInputs, wantInputs) {
		t.Fatalf("source inputs = %+v, want %+v", report.SourceInputs, wantInputs)
	}
	if got, want := report.Playback.Admitted, []int16{322, 324, 326, 328}; !reflect.DeepEqual(got, want) {
		t.Fatalf("listener admission = %v, want %v", got, want)
	}
	if got, want := report.Playback.Observed, []FrameObservation{{Epoch: 1, Samples: []int16{322, 324, 326, 328}}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("listener observed frames = %+v, want %+v", got, want)
	}
	if len(report.RawEvents) == 0 {
		t.Fatal("raw event ledger is empty")
	}
	for _, event := range report.RawEvents {
		if event.AfterTerminal {
			t.Fatalf("raw event occurred after terminal: %+v", event)
		}
	}
	if report.Epochs.StalePendingSamples != 4 || !report.Epochs.EndBeforeTerminal {
		t.Fatalf("epoch evidence = %+v, want stale four samples and ordered terminal", report.Epochs)
	}
	if !report.ReplayRejected || report.Recording.ProviderTrace != "unavailable" {
		t.Fatalf("partial replay evidence = %+v, want rejection with unavailable provider trace", report.Recording)
	}
}

func TestPlaybackBoundarySeparatesAdmissionConsumptionAndUnderflow(t *testing.T) {
	report, err := runPlaybackBoundaryProbe()
	if err != nil {
		t.Fatalf("runPlaybackBoundaryProbe() error = %v", err)
	}
	if report.BeforeRender != 0 {
		t.Fatalf("rendered before callback = %d, want 0", report.BeforeRender)
	}
	if got, want := report.Consumed, []int16{41, 42, 0, 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rendered callback = %v, want %v", got, want)
	}
	if report.DiscardedSamples != 3 || report.UnderflowSamples != 2 || report.ZeroFilledSamples != 2 {
		t.Fatalf("queue accounting = %+v, want discard=3 underflow=2 zero-fill=2", report)
	}
}

func TestLifecycleCancellationAndRepeatedClose(t *testing.T) {
	for _, mode := range []string{"cancel-before-start", "cancel-active"} {
		report, err := Run(Request{Mode: mode})
		if err != nil {
			t.Fatalf("%s: Run() error = %v", mode, err)
		}
		if report.Cancellation == nil || !report.Cancellation.Joined ||
			report.Cancellation.FirstCloseError != "" || report.Cancellation.SecondCloseError != "" ||
			!report.Cancellation.RunReturned || !report.Cancellation.RepeatedCloseOK {
			t.Fatalf("%s: cancellation evidence = %+v", mode, report.Cancellation)
		}
		if mode == "cancel-before-start" && (report.Cancellation.Opened != 0 || report.Cancellation.Started != 0) {
			t.Fatalf("%s: pre-start cancellation admitted/started participants: %+v", mode, report.Cancellation)
		}
		if mode == "cancel-active" && (report.Cancellation.Opened != 2 || report.Cancellation.Started != 2 || report.Cancellation.Waited != 2) {
			t.Fatalf("%s: active cancellation lifecycle = %+v", mode, report.Cancellation)
		}
	}
}

func TestMutationOracleRejectsThroughRoomSubprocessContract(t *testing.T) {
	for _, mutation := range []string{"peer-participant-key", "source-order", "epoch", "pcm", "terminal"} {
		t.Run(mutation, func(t *testing.T) {
			_, err := Run(Request{Mode: "mutations", Mutation: mutation, OutputDir: t.TempDir()})
			if err == nil || !strings.Contains(err.Error(), "literal oracle mismatch") {
				t.Fatalf("mutation error = %v, want literal oracle mismatch", err)
			}
		})
	}
}
