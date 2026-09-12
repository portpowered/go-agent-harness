package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplayschedule"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestBuildOrdersTiedSegmentsAndRetainsPartialTail(t *testing.T) {
	root := t.TempDir()
	alphaCapture := filepath.Join(root, "alpha.session.json")
	betaCapture := filepath.Join(root, "beta.session.json")
	writeCapture(t, alphaCapture, 2)
	writeCapture(t, betaCapture, 2)
	alphaPCM := []byte{1, 0, 2, 0, 3, 0}
	betaPCM := []byte{9, 0, 8, 0, 7, 0}
	alphaPath := filepath.Join(root, "alpha.pcm")
	betaPath := filepath.Join(root, "beta.pcm")
	writeBytes(t, alphaPath, alphaPCM)
	writeBytes(t, betaPath, betaPCM)

	schedule := buildSchedule(t, roomreplayschedule.BuildRequest{
		SourceFormat: sourceFormat(100, 1),
		TargetFormat: targetTestFormat(100, 1),
		Participants: []roomreplayschedule.Participant{
			{ID: "alpha", CapturePath: alphaCapture, SentPCMPath: alphaPath},
			{ID: "beta", CapturePath: betaCapture, SentPCMPath: betaPath},
		},
		Timeline: []roomreplayschedule.TimelineEvent{
			{Sequence: 20, Type: "speech_start", ParticipantID: "alpha"},
			{Sequence: 21, OffsetMS: 20, Type: "speech_end", ParticipantID: "alpha"},
			{Sequence: 10, Type: "speech_start", ParticipantID: "beta"},
			{Sequence: 11, OffsetMS: 20, Type: "speech_end", ParticipantID: "beta"},
		},
		TargetIDs: []string{"alpha", "beta"},
	})

	if len(schedule.frames) != 2 {
		t.Fatalf("scheduled frames = %d, want 2", len(schedule.frames))
	}
	if got := contributionSources(schedule.frames[0]); !reflect.DeepEqual(got, []string{"beta", "alpha"}) {
		t.Fatalf("frame 0 sources = %v, want beta then alpha", got)
	}
	if got := contributionBytes(schedule.frames[1]); !reflect.DeepEqual(got, [][]byte{{7, 0, 0, 0}, {3, 0, 0, 0}}) {
		t.Fatalf("frame 1 bytes = %x, want partial tails", got)
	}
}

func TestBuildConvertsPCM16AndPreservesTextOnlyNoSchedule(t *testing.T) {
	root := t.TempDir()
	capturePath := filepath.Join(root, "target.session.json")
	writeCapture(t, capturePath, 1)
	pcmPath := filepath.Join(root, "target.pcm")
	input := []byte{0xE8, 0x03, 0xB8, 0x0B, 0xD0, 0x07, 0xA0, 0x0F}
	writeBytes(t, pcmPath, input)

	result, err := New().Build(context.Background(), roomreplayschedule.BuildRequest{
		SourceFormat: roomreplayschedule.SourcePCM16Format{SampleRate: 50, Channels: 2, SampleWidthBits: 16},
		TargetFormat: targetTestFormat(100, 1),
		Participants: []roomreplayschedule.Participant{{ID: "target", CapturePath: capturePath, SentPCMPath: pcmPath}},
		TargetIDs:    []string{"target"},
	})
	if err != nil {
		t.Fatalf("Build converted PCM: %v", err)
	}
	schedule, ok := result.(*schedule)
	if !ok {
		t.Fatalf("Build returned %T, want *schedule", result)
	}
	want := [][]byte{{0xD0, 0x07, 0xC4, 0x09}}
	if got := contributionBytes(schedule.frames[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("converted frame = %x, want %x", got, want)
	}

	textCapture := filepath.Join(root, "text.session.json")
	writeCapture(t, textCapture, 0)
	result, err = New().Build(context.Background(), roomreplayschedule.BuildRequest{
		Participants: []roomreplayschedule.Participant{{ID: "text", CapturePath: textCapture}},
		TargetIDs:    []string{"text"},
	})
	if err != nil {
		t.Fatalf("Build text-only capture: %v", err)
	}
	if result != nil {
		t.Fatalf("text-only schedule = %#v, want nil", result)
	}

	copyInput := []byte{1, 0, 2, 0}
	copyOutput, err := normalizePCM(copyInput, sourceFormat(100, 1), targetTestFormat(100, 1))
	if err != nil {
		t.Fatalf("normalize same-format PCM: %v", err)
	}
	copyOutput[0] = 99
	if copyInput[0] == 99 {
		t.Fatal("same-format normalization aliases the source PCM")
	}
}

func TestBuildRejectsMalformedAdmissionInputs(t *testing.T) {
	root := t.TempDir()
	capturePath := filepath.Join(root, "target.session.json")
	writeCapture(t, capturePath, 1)
	pcmPath := filepath.Join(root, "target.pcm")
	writeBytes(t, pcmPath, []byte{1, 0, 2, 0})
	base := roomreplayschedule.BuildRequest{
		SourceFormat: sourceFormat(100, 1),
		TargetFormat: targetTestFormat(100, 1),
		Participants: []roomreplayschedule.Participant{{ID: "target", CapturePath: capturePath, SentPCMPath: pcmPath}},
		TargetIDs:    []string{"target"},
	}
	tests := []struct {
		name string
		edit func(*roomreplayschedule.BuildRequest)
		want error
	}{
		{name: "invalid target format", edit: func(request *roomreplayschedule.BuildRequest) { request.TargetFormat.SampleRate = 0 }, want: roomreplayschedule.ErrInvalidFormat},
		{name: "invalid source width", edit: func(request *roomreplayschedule.BuildRequest) { request.SourceFormat.SampleWidthBits = 8 }, want: roomreplayschedule.ErrInvalidPCM},
		{name: "unaligned source", edit: func(request *roomreplayschedule.BuildRequest) {
			request.SourceFormat.Channels = 2
			request.Participants[0].SentPCMPath = filepath.Join(root, "unaligned.pcm")
			writeBytes(t, request.Participants[0].SentPCMPath, []byte{1, 0})
		}, want: roomreplayschedule.ErrInvalidPCM},
		{name: "missing participant", edit: func(request *roomreplayschedule.BuildRequest) { request.TargetIDs = []string{"other"} }, want: roomreplayschedule.ErrParticipantMissing},
		{name: "missing capture", edit: func(request *roomreplayschedule.BuildRequest) {
			request.Participants[0].CapturePath = filepath.Join(root, "missing")
		}, want: roomreplayschedule.ErrCaptureUnavailable},
		{name: "missing sent PCM", edit: func(request *roomreplayschedule.BuildRequest) { request.Participants[0].SentPCMPath = "" }, want: roomreplayschedule.ErrSentPCMUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := cloneBuildRequest(base)
			test.edit(&request)
			_, err := New().Build(context.Background(), request)
			if !errors.Is(err, test.want) {
				t.Fatalf("Build error = %v, want errors.Is(%v)", err, test.want)
			}
		})
	}
}

func TestRunUsesAcknowledgementBarrierAndPreservesCancellation(t *testing.T) {
	root := t.TempDir()
	alphaCapture := filepath.Join(root, "alpha.session.json")
	betaCapture := filepath.Join(root, "beta.session.json")
	writeCapture(t, alphaCapture, 2)
	writeCapture(t, betaCapture, 2)
	alphaPath := filepath.Join(root, "alpha.pcm")
	betaPath := filepath.Join(root, "beta.pcm")
	writeBytes(t, alphaPath, []byte{1, 0, 2, 0, 3, 0, 4, 0})
	writeBytes(t, betaPath, []byte{9, 0, 8, 0, 7, 0, 6, 0})
	schedule := buildSchedule(t, roomreplayschedule.BuildRequest{
		SourceFormat: sourceFormat(100, 1), TargetFormat: targetTestFormat(100, 1),
		Participants: []roomreplayschedule.Participant{
			{ID: "alpha", CapturePath: alphaCapture, SentPCMPath: alphaPath},
			{ID: "beta", CapturePath: betaCapture, SentPCMPath: betaPath},
		}, TargetIDs: []string{"alpha", "beta"},
	})

	var mu sync.Mutex
	advances := map[string]int{}
	contributions := make([][2]string, 0, 4)
	ackRelease := make(chan struct{})
	firstAck := true
	target := func(id string) roomreplayschedule.Target {
		return roomreplayschedule.Target{
			ID:     id,
			Active: func() bool { return true },
			Release: func(ctx context.Context, sourceID string, _ []byte) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				mu.Lock()
				contributions = append(contributions, [2]string{sourceID, id})
				mu.Unlock()
				return nil
			},
			Advance: func(context.Context) error {
				mu.Lock()
				advances[id]++
				mu.Unlock()
				return nil
			},
			AwaitAcknowledgement: func(ctx context.Context) error {
				if id != "alpha" {
					return nil
				}
				mu.Lock()
				block := firstAck
				firstAck = false
				mu.Unlock()
				if !block {
					return nil
				}
				select {
				case <-ackRelease:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			},
		}
	}

	runDone := make(chan error, 1)
	go func() {
		runDone <- schedule.Run(context.Background(), roomreplayschedule.RunRequest{Targets: []roomreplayschedule.Target{target("alpha"), target("beta")}})
	}()
	select {
	case <-time.After(50 * time.Millisecond):
		mu.Lock()
		if advances["alpha"] != 1 {
			mu.Unlock()
			t.Fatalf("alpha advances before acknowledgement = %d, want 1", advances["alpha"])
		}
		mu.Unlock()
	case err := <-runDone:
		t.Fatalf("Run returned before acknowledgement: %v", err)
	}
	close(ackRelease)
	if err := <-runDone; err != nil {
		t.Fatalf("Run: %v", err)
	}
	mu.Lock()
	gotAdvances := map[string]int{"alpha": advances["alpha"], "beta": advances["beta"]}
	gotContributions := append([][2]string(nil), contributions...)
	mu.Unlock()
	if !reflect.DeepEqual(gotAdvances, map[string]int{"alpha": 2, "beta": 2}) {
		t.Fatalf("advances = %v, want two acknowledged frames", gotAdvances)
	}
	if !reflect.DeepEqual(gotContributions[:2], [][2]string{{"alpha", "beta"}, {"beta", "alpha"}}) {
		t.Fatalf("first contribution order = %v", gotContributions[:2])
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var cancelOnce sync.Once
	err := schedule.Run(cancelCtx, roomreplayschedule.RunRequest{
		Targets: []roomreplayschedule.Target{target("alpha"), target("beta")},
		OnContribution: func(roomreplayschedule.Contribution) {
			cancelOnce.Do(cancel)
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v, want context.Canceled", err)
	}
}

func TestRunRejectsMissingUncontrolledAndInactiveTargets(t *testing.T) {
	schedule := &schedule{targetIDs: []string{"target"}, frames: []scheduledFrame{{}}}
	ctx := context.Background()
	if err := schedule.Run(ctx, roomreplayschedule.RunRequest{}); !errors.Is(err, roomreplayschedule.ErrTargetMissing) {
		t.Fatalf("missing target error = %v", err)
	}
	if err := schedule.Run(ctx, roomreplayschedule.RunRequest{Targets: []roomreplayschedule.Target{{ID: "target"}}}); !errors.Is(err, roomreplayschedule.ErrTargetUncontrolled) {
		t.Fatalf("uncontrolled target error = %v", err)
	}
	inactive := roomreplayschedule.Target{
		ID: "target", Active: func() bool { return false },
		Release:              func(context.Context, string, []byte) error { return nil },
		Advance:              func(context.Context) error { return nil },
		AwaitAcknowledgement: func(context.Context) error { return nil },
	}
	if err := schedule.Run(ctx, roomreplayschedule.RunRequest{Targets: []roomreplayschedule.Target{inactive}}); !errors.Is(err, roomreplayschedule.ErrTargetInactive) {
		t.Fatalf("inactive target error = %v", err)
	}
	if err := schedule.Run(ctx, roomreplayschedule.RunRequest{
		Targets:    []roomreplayschedule.Target{inactive},
		IsStopping: func() bool { return true },
	}); err != nil {
		t.Fatalf("stopping inactive target = %v, want clean completion", err)
	}
}

func TestRunPreservesDeadlineIdentity(t *testing.T) {
	schedule := &schedule{targetIDs: []string{"target"}, frames: []scheduledFrame{{}}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := schedule.Run(ctx, roomreplayschedule.RunRequest{Targets: []roomreplayschedule.Target{{
		ID: "target", Active: func() bool { return true },
		Release: func(context.Context, string, []byte) error { return nil },
		Advance: func(context.Context) error { return nil },
		AwaitAcknowledgement: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error = %v, want context.DeadlineExceeded", err)
	}
}

func buildSchedule(t *testing.T, request roomreplayschedule.BuildRequest) *schedule {
	t.Helper()
	result, err := New().Build(context.Background(), request)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if result == nil {
		t.Fatal("Build returned nil schedule")
	}
	schedule, ok := result.(*schedule)
	if !ok {
		t.Fatalf("Build returned %T, want *schedule", result)
	}
	return schedule
}

func sourceFormat(rate, channels int) roomreplayschedule.SourcePCM16Format {
	return roomreplayschedule.SourcePCM16Format{SampleRate: rate, Channels: channels, SampleWidthBits: 16}
}

func targetTestFormat(rate, channels int) roomreplayschedule.PCM16Format {
	return roomreplayschedule.PCM16Format{SampleRate: rate, Channels: channels, FrameDuration: 20 * time.Millisecond}
}

func contributionSources(frame scheduledFrame) []string {
	result := make([]string, 0, len(frame.contributions))
	for _, contribution := range frame.contributions {
		result = append(result, contribution.sourceID)
	}
	return result
}

func contributionBytes(frame scheduledFrame) [][]byte {
	result := make([][]byte, 0, len(frame.contributions))
	for _, contribution := range frame.contributions {
		result = append(result, append([]byte(nil), contribution.pcm...))
	}
	return result
}

func writeCapture(t *testing.T, path string, appendCount int) {
	t.Helper()
	records := make([]gwtesting.CapturedSessionEvent, 0, appendCount)
	for sequence := 1; sequence <= appendCount; sequence++ {
		records = append(records, gwtesting.CapturedSessionEvent{
			Sequence: sequence, Direction: gwtesting.DirectionClientToServer,
			Type: "input_audio_buffer.append", PayloadType: gwtesting.SessionPayloadTypeWebSocketMessage,
			Payload: json.RawMessage(`{"type":"input_audio_buffer.append"}`),
		})
	}
	data, err := json.MarshalIndent(gwtesting.SessionCapture{
		Version: gwtesting.SessionCaptureVersion,
		Session: gwtesting.SessionMetadata{StartedAtUTC: time.Now().UTC().Format(time.RFC3339Nano)},
		Records: records,
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal capture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write capture: %v", err)
	}
}

func writeBytes(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write bytes: %v", err)
	}
}

func cloneBuildRequest(request roomreplayschedule.BuildRequest) roomreplayschedule.BuildRequest {
	clone := request
	clone.Participants = append([]roomreplayschedule.Participant(nil), request.Participants...)
	clone.TargetIDs = append([]string(nil), request.TargetIDs...)
	clone.Timeline = append([]roomreplayschedule.TimelineEvent(nil), request.Timeline...)
	return clone
}
