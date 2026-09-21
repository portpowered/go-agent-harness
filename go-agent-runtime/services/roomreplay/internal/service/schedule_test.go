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

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const roomReplayScheduleAlphaID = "alpha"

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

	schedule := buildSchedule(t, roomreplay.BuildRequest{
		SourceFormat: sourceFormat(100, 1),
		TargetFormat: targetTestFormat(100, 1),
		Participants: []roomreplay.Participant{
			{ID: roomReplayScheduleAlphaID, CapturePath: alphaCapture, SentPCMPath: alphaPath},
			{ID: roomReplayAdditionalParticipantID, CapturePath: betaCapture, SentPCMPath: betaPath},
		},
		Timeline: []roomreplay.TimelineEvent{
			{Sequence: 20, Type: "speech_start", ParticipantID: roomReplayScheduleAlphaID},
			{Sequence: 21, OffsetMS: 20, Type: "speech_end", ParticipantID: roomReplayScheduleAlphaID},
			{Sequence: 10, Type: "speech_start", ParticipantID: roomReplayAdditionalParticipantID},
			{Sequence: 11, OffsetMS: 20, Type: "speech_end", ParticipantID: roomReplayAdditionalParticipantID},
		},
		TargetIDs: []string{roomReplayScheduleAlphaID, roomReplayAdditionalParticipantID},
	})
	contributions := make([]roomreplay.Contribution, 0, 4)
	target := func(id string) roomreplay.Target {
		return roomreplay.Target{
			ID:                   id,
			Active:               func() bool { return true },
			Release:              func(context.Context, string, []byte) error { return nil },
			Advance:              func(context.Context) error { return nil },
			AwaitAcknowledgement: func(context.Context) error { return nil },
		}
	}
	if err := schedule.Run(context.Background(), roomreplay.RunRequest{
		Targets: []roomreplay.Target{target(roomReplayScheduleAlphaID), target(roomReplayAdditionalParticipantID)},
		OnContribution: func(contribution roomreplay.Contribution) {
			contributions = append(contributions, contribution)
		},
	}); err != nil {
		t.Fatalf("Schedule.Run: %v", err)
	}
	if len(contributions) != 4 {
		t.Fatalf("public contributions = %d, want two sources across two frames", len(contributions))
	}
	gotSources := []string{contributions[0].SourceID, contributions[1].SourceID}
	if !reflect.DeepEqual(gotSources, []string{roomReplayAdditionalParticipantID, roomReplayScheduleAlphaID}) {
		t.Fatalf("frame 0 sources = %v, want beta then alpha", gotSources)
	}
	gotTail := [][]byte{contributions[2].PCM, contributions[3].PCM}
	if !reflect.DeepEqual(gotTail, [][]byte{{7, 0, 0, 0}, {3, 0, 0, 0}}) {
		t.Fatalf("frame 1 PCM = %x, want partial tails", gotTail)
	}
}

func TestBuildConvertsPCM16AndPreservesTextOnlyNoSchedule(t *testing.T) {
	root := t.TempDir()
	capturePath := filepath.Join(root, "target.session.json")
	writeCapture(t, capturePath, 1)
	pcmPath := filepath.Join(root, "target.pcm")
	input := []byte{0xE8, 0x03, 0xB8, 0x0B, 0xD0, 0x07, 0xA0, 0x0F}
	writeBytes(t, pcmPath, input)

	peerCapturePath := filepath.Join(root, "peer.session.json")
	writeCapture(t, peerCapturePath, 1)
	peerPCMPath := filepath.Join(root, "peer.pcm")
	writeBytes(t, peerPCMPath, []byte{1, 0, 2, 0})
	result, err := roomReplayServiceForTest().Build(context.Background(), roomreplay.BuildRequest{
		SourceFormat: roomreplay.SourcePCM16Format{SampleRate: 50, Channels: 2, SampleWidthBits: 16},
		TargetFormat: targetTestFormat(100, 1),
		Participants: []roomreplay.Participant{
			{ID: "target", CapturePath: capturePath, SentPCMPath: pcmPath},
			{ID: "peer", CapturePath: peerCapturePath, SentPCMPath: peerPCMPath},
		},
		TargetIDs: []string{"target", "peer"},
	})
	if err != nil {
		t.Fatalf("Build converted PCM: %v", err)
	}
	var released [][]byte
	target := func(id string) roomreplay.Target {
		return roomreplay.Target{
			ID: id, Active: func() bool { return true },
			Release: func(_ context.Context, sourceID string, pcm []byte) error {
				if id == "peer" && sourceID == "target" {
					released = append(released, append([]byte(nil), pcm...))
				}
				return nil
			},
			Advance:              func(context.Context) error { return nil },
			AwaitAcknowledgement: func(context.Context) error { return nil },
		}
	}
	if err := result.Run(context.Background(), roomreplay.RunRequest{Targets: []roomreplay.Target{target("target"), target("peer")}}); err != nil {
		t.Fatalf("Schedule.Run converted PCM: %v", err)
	}
	want := [][]byte{{0xD0, 0x07, 0xC4, 0x09}}
	if len(released) == 0 || !reflect.DeepEqual(released[0], want[0]) {
		t.Fatalf("first released converted PCM = %x, want %x", released, want[0])
	}

	textCapture := filepath.Join(root, "text.session.json")
	writeCapture(t, textCapture, 0)
	result, err = roomReplayServiceForTest().Build(context.Background(), roomreplay.BuildRequest{
		Participants: []roomreplay.Participant{{ID: "text", CapturePath: textCapture}},
		TargetIDs:    []string{"text"},
	})
	if err != nil {
		t.Fatalf("Build text-only capture: %v", err)
	}
	if result != nil {
		t.Fatalf("text-only schedule = %#v, want nil", result)
	}

}

func TestBuildRejectsMalformedAdmissionInputs(t *testing.T) {
	root := t.TempDir()
	capturePath := filepath.Join(root, "target.session.json")
	writeCapture(t, capturePath, 1)
	pcmPath := filepath.Join(root, "target.pcm")
	writeBytes(t, pcmPath, []byte{1, 0, 2, 0})
	base := roomreplay.BuildRequest{
		SourceFormat: sourceFormat(100, 1),
		TargetFormat: targetTestFormat(100, 1),
		Participants: []roomreplay.Participant{{ID: "target", CapturePath: capturePath, SentPCMPath: pcmPath}},
		TargetIDs:    []string{"target"},
	}
	tests := []struct {
		name string
		edit func(*roomreplay.BuildRequest)
		want error
	}{
		{name: "invalid target format", edit: func(request *roomreplay.BuildRequest) { request.TargetFormat.SampleRate = 0 }, want: roomreplay.ErrInvalidFormat},
		{name: "invalid source width", edit: func(request *roomreplay.BuildRequest) { request.SourceFormat.SampleWidthBits = 8 }, want: roomreplay.ErrInvalidPCM},
		{name: "unaligned source", edit: func(request *roomreplay.BuildRequest) {
			request.SourceFormat.Channels = 2
			request.Participants[0].SentPCMPath = filepath.Join(root, "unaligned.pcm")
			writeBytes(t, request.Participants[0].SentPCMPath, []byte{1, 0})
		}, want: roomreplay.ErrInvalidPCM},
		{name: "missing participant", edit: func(request *roomreplay.BuildRequest) { request.TargetIDs = []string{"other"} }, want: roomreplay.ErrParticipantMissing},
		{name: "missing capture", edit: func(request *roomreplay.BuildRequest) {
			request.Participants[0].CapturePath = filepath.Join(root, "missing")
		}, want: roomreplay.ErrCaptureUnavailable},
		{name: "missing sent PCM", edit: func(request *roomreplay.BuildRequest) { request.Participants[0].SentPCMPath = "" }, want: roomreplay.ErrSentPCMUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := cloneBuildRequest(base)
			test.edit(&request)
			_, err := roomReplayServiceForTest().Build(context.Background(), request)
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
	schedule := buildSchedule(t, roomreplay.BuildRequest{
		SourceFormat: sourceFormat(100, 1), TargetFormat: targetTestFormat(100, 1),
		Participants: []roomreplay.Participant{
			{ID: roomReplayScheduleAlphaID, CapturePath: alphaCapture, SentPCMPath: alphaPath},
			{ID: roomReplayAdditionalParticipantID, CapturePath: betaCapture, SentPCMPath: betaPath},
		}, TargetIDs: []string{roomReplayScheduleAlphaID, roomReplayAdditionalParticipantID},
	})

	var mu sync.Mutex
	advances := map[string]int{}
	contributions := make([][2]string, 0, 4)
	ackRelease := make(chan struct{})
	firstAck := true
	target := func(id string) roomreplay.Target {
		return roomreplay.Target{
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
				if id != roomReplayScheduleAlphaID {
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
		runDone <- schedule.Run(context.Background(), roomreplay.RunRequest{Targets: []roomreplay.Target{target(roomReplayScheduleAlphaID), target(roomReplayAdditionalParticipantID)}})
	}()
	select {
	case <-time.After(50 * time.Millisecond):
		mu.Lock()
		if advances[roomReplayScheduleAlphaID] != 1 {
			mu.Unlock()
			t.Fatalf("alpha advances before acknowledgement = %d, want 1", advances[roomReplayScheduleAlphaID])
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
	gotAdvances := map[string]int{roomReplayScheduleAlphaID: advances[roomReplayScheduleAlphaID], roomReplayAdditionalParticipantID: advances[roomReplayAdditionalParticipantID]}
	gotContributions := append([][2]string(nil), contributions...)
	mu.Unlock()
	if !reflect.DeepEqual(gotAdvances, map[string]int{roomReplayScheduleAlphaID: 2, roomReplayAdditionalParticipantID: 2}) {
		t.Fatalf("advances = %v, want two acknowledged frames", gotAdvances)
	}
	if !reflect.DeepEqual(gotContributions[:2], [][2]string{{roomReplayScheduleAlphaID, roomReplayAdditionalParticipantID}, {roomReplayAdditionalParticipantID, roomReplayScheduleAlphaID}}) {
		t.Fatalf("first contribution order = %v", gotContributions[:2])
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var cancelOnce sync.Once
	err := schedule.Run(cancelCtx, roomreplay.RunRequest{
		Targets: []roomreplay.Target{target(roomReplayScheduleAlphaID), target(roomReplayAdditionalParticipantID)},
		OnContribution: func(roomreplay.Contribution) {
			cancelOnce.Do(cancel)
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v, want context.Canceled", err)
	}
}

func TestRunRejectsMissingUncontrolledAndInactiveTargets(t *testing.T) {
	root := t.TempDir()
	capturePath := filepath.Join(root, "target.session.json")
	writeCapture(t, capturePath, 1)
	pcmPath := filepath.Join(root, "target.pcm")
	writeBytes(t, pcmPath, []byte{1, 0, 2, 0})
	schedule := buildSchedule(t, roomreplay.BuildRequest{
		SourceFormat: sourceFormat(100, 1), TargetFormat: targetTestFormat(100, 1),
		Participants: []roomreplay.Participant{{ID: "target", CapturePath: capturePath, SentPCMPath: pcmPath}},
		TargetIDs:    []string{"target"},
	})
	ctx := context.Background()
	if err := schedule.Run(ctx, roomreplay.RunRequest{}); !errors.Is(err, roomreplay.ErrTargetMissing) {
		t.Fatalf("missing target error = %v", err)
	}
	if err := schedule.Run(ctx, roomreplay.RunRequest{Targets: []roomreplay.Target{{ID: "target"}}}); !errors.Is(err, roomreplay.ErrTargetUncontrolled) {
		t.Fatalf("uncontrolled target error = %v", err)
	}
	inactive := roomreplay.Target{
		ID: "target", Active: func() bool { return false },
		Release:              func(context.Context, string, []byte) error { return nil },
		Advance:              func(context.Context) error { return nil },
		AwaitAcknowledgement: func(context.Context) error { return nil },
	}
	if err := schedule.Run(ctx, roomreplay.RunRequest{Targets: []roomreplay.Target{inactive}}); !errors.Is(err, roomreplay.ErrTargetInactive) {
		t.Fatalf("inactive target error = %v", err)
	}
	if err := schedule.Run(ctx, roomreplay.RunRequest{
		Targets:    []roomreplay.Target{inactive},
		IsStopping: func() bool { return true },
	}); err != nil {
		t.Fatalf("stopping inactive target = %v, want clean completion", err)
	}
}

func TestRunRechecksTargetActivityBeforeEachContribution(t *testing.T) {
	root := t.TempDir()
	participants := make([]roomreplay.Participant, 0, 3)
	for _, id := range []string{roomReplayScheduleAlphaID, roomReplayAdditionalParticipantID, "gamma"} {
		capturePath := filepath.Join(root, id+".session.json")
		writeCapture(t, capturePath, 1)
		pcmPath := filepath.Join(root, id+".pcm")
		writeBytes(t, pcmPath, []byte{1, 0})
		participants = append(participants, roomreplay.Participant{ID: id, CapturePath: capturePath, SentPCMPath: pcmPath})
	}
	schedule := buildSchedule(t, roomreplay.BuildRequest{
		SourceFormat: sourceFormat(100, 1), TargetFormat: targetTestFormat(100, 1),
		Participants: participants,
		TargetIDs:    []string{roomReplayScheduleAlphaID, roomReplayAdditionalParticipantID, "gamma"},
	})

	active := map[string]bool{roomReplayScheduleAlphaID: true, roomReplayAdditionalParticipantID: true, "gamma": true}
	var betaReleases int
	target := func(id string) roomreplay.Target {
		return roomreplay.Target{
			ID:     id,
			Active: func() bool { return active[id] },
			Release: func(_ context.Context, _ string, _ []byte) error {
				if id == roomReplayAdditionalParticipantID {
					betaReleases++
					if betaReleases == 1 {
						active[id] = false
					}
				}
				return nil
			},
			Advance:              func(context.Context) error { return nil },
			AwaitAcknowledgement: func(context.Context) error { return nil },
		}
	}

	err := schedule.Run(context.Background(), roomreplay.RunRequest{
		Targets: []roomreplay.Target{target(roomReplayScheduleAlphaID), target(roomReplayAdditionalParticipantID), target("gamma")},
	})
	if !errors.Is(err, roomreplay.ErrTargetInactive) {
		t.Fatalf("mid-frame inactive target error = %v, want errors.Is(%v)", err, roomreplay.ErrTargetInactive)
	}
	if betaReleases != 1 {
		t.Fatalf("inactive target releases = %d, want one", betaReleases)
	}
}

func TestRunPreservesDeadlineIdentity(t *testing.T) {
	root := t.TempDir()
	capturePath := filepath.Join(root, "target.session.json")
	writeCapture(t, capturePath, 1)
	pcmPath := filepath.Join(root, "target.pcm")
	writeBytes(t, pcmPath, []byte{1, 0, 2, 0})
	schedule := buildSchedule(t, roomreplay.BuildRequest{
		SourceFormat: sourceFormat(100, 1), TargetFormat: targetTestFormat(100, 1),
		Participants: []roomreplay.Participant{{ID: "target", CapturePath: capturePath, SentPCMPath: pcmPath}},
		TargetIDs:    []string{"target"},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := schedule.Run(ctx, roomreplay.RunRequest{Targets: []roomreplay.Target{{
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

func buildSchedule(t *testing.T, request roomreplay.BuildRequest) roomreplay.Schedule {
	t.Helper()
	result, err := roomReplayServiceForTest().Build(context.Background(), request)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if result == nil {
		t.Fatal("Build returned nil schedule")
	}
	return result
}

func sourceFormat(rate, channels int) roomreplay.SourcePCM16Format {
	return roomreplay.SourcePCM16Format{SampleRate: rate, Channels: channels, SampleWidthBits: 16}
}

func targetTestFormat(rate, channels int) roomreplay.PCM16Format {
	return roomreplay.PCM16Format{SampleRate: rate, Channels: channels, FrameDuration: 20 * time.Millisecond}
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

func cloneBuildRequest(request roomreplay.BuildRequest) roomreplay.BuildRequest {
	clone := request
	clone.Participants = append([]roomreplay.Participant(nil), request.Participants...)
	clone.TargetIDs = append([]string(nil), request.TargetIDs...)
	clone.Timeline = append([]roomreplay.TimelineEvent(nil), request.Timeline...)
	return clone
}
