package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const (
	roomFanoutSafetyTimeout = 10 * time.Second
	// The shared room-test options use a short lifetime for tests that exercise
	// ordinary participant termination. This fanout test needs a longer
	// startup allowance so scheduler contention cannot stop the room before
	// its causal frame assertion observes all participants.
	roomFanoutMaxDuration = 30 * time.Second
)

func TestObserveRoomParticipantStream_FansOutBeforeDurableAudioEvidence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	targetMixer, err := room.NewPCM16Mixer(ctx, room.PCM16Format{
		SampleRate:    1000,
		Channels:      1,
		FrameDuration: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new target mixer: %v", err)
	}
	t.Cleanup(func() { _ = targetMixer.Close() })
	if err := targetMixer.AddInput("source"); err != nil {
		t.Fatalf("add source mixer input: %v", err)
	}

	owner := &roomEvidence{}
	deltasPath := filepath.Join(t.TempDir(), "source.deltas.jsonl")
	deltasFile, err := os.OpenFile(deltasPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("create delta evidence: %v", err)
	}
	audioPath := filepath.Join(t.TempDir(), "source.wav")
	audio, err := newSelfPlayWAVRecorder(audioPath, 1000)
	if err != nil {
		_ = deltasFile.Close()
		t.Fatalf("create audio evidence: %v", err)
	}
	participantEvidence := &roomParticipantEvidence{
		owner: owner,
		id:    "source",
		deltas: &selfPlayJSONLWriter{
			path: deltasPath,
			file: deltasFile,
		},
		audio: audio,
	}
	t.Cleanup(func() {
		_ = participantEvidence.deltas.close()
		_ = participantEvidence.audio.close()
	})

	source := &roomParticipantRuntime{
		plan:      &roomParticipantPlan{manifest: room.Participant{ID: "source"}},
		ctx:       ctx,
		lifecycle: &roomParticipantLifecycle{},
	}
	target := &roomParticipantRuntime{
		plan:      &roomParticipantPlan{manifest: room.Participant{ID: "target"}},
		ctx:       ctx,
		mixer:     targetMixer,
		lifecycle: &roomParticipantLifecycle{},
	}
	coordinator := newRoomCoordinator(nil, 0, nil)
	coordinator.addParticipant(source)
	coordinator.addParticipant(target)

	pcm := []byte{0x34, 0x12, 0x78, 0x56}
	order := make([]string, 0, 2)
	opts := RoomRunOptions{
		OnAudioOutput: func(participantID string, got []byte) error {
			if participantID != "source" || !bytes.Equal(got, pcm) {
				t.Errorf("audio output = %q/%v, want source/%v", participantID, got, pcm)
			}
			order = append(order, "output")
			return nil
		},
		onParticipantAudioFanned: func(sourceID, targetID string, got []byte) {
			if sourceID != "source" || targetID != "target" || !bytes.Equal(got, pcm) {
				t.Errorf("fanned audio = %q -> %q/%v, want source -> target/%v", sourceID, targetID, got, pcm)
			}
			info, statErr := deltasFile.Stat()
			if statErr != nil {
				t.Errorf("stat delta evidence before fanout callback: %v", statErr)
			} else if info.Size() != 0 {
				t.Errorf("delta evidence size before fanout = %d, want 0", info.Size())
			}
			order = append(order, "fanout")
		},
	}
	observeRoomParticipantStream(
		coordinator,
		source,
		opts,
		owner,
		participantEvidence,
		RoomParticipantEventSink{},
		messages.StreamMessage{
			Type:       messages.StreamTypeAudioDelta,
			Role:       messages.RoleAssistant,
			ResponseID: "response-1",
			Value:      messages.NewAudioDeltaValue(pcm),
		},
	)

	if got, want := fmt.Sprint(order), "[output fanout]"; got != want {
		t.Fatalf("critical path order = %s, want %s", got, want)
	}
	if info, statErr := deltasFile.Stat(); statErr != nil {
		t.Fatalf("stat delta evidence after fanout: %v", statErr)
	} else if info.Size() == 0 {
		t.Fatal("delta evidence was not written after fanout")
	}
	if got := audio.dataBytes; got != uint64(len(pcm)) {
		t.Fatalf("audio evidence bytes = %d, want %d", got, len(pcm))
	}
}

func TestRunRoom_FansPCMToEveryOtherParticipant(t *testing.T) {
	ids := []string{"a", "b", "c"}
	values := map[string]int16{"a": 1000, "b": 2000, "c": 3000}
	inferencers := make(map[string]*roomTestInferencer, len(ids))
	for _, id := range ids {
		inferencers[id] = &roomTestInferencer{events: []messages.StreamMessage{
			roomTestSessionOpen(id),
			roomTestAudioEvent(values[id], 10),
		}}
	}

	inputFrames := make(chan roomAudioFrame, 128)
	outputIDs := make(chan string, len(ids))
	opts, factoryCalls := newRoomTestRunOptions(ids, inferencers)
	opts.Manifest.Room.MaxDuration = roomFanoutMaxDuration
	audioGate := make(chan struct{})
	var openedMu sync.Mutex
	var opened int
	var audioGateOnce sync.Once
	for _, inferencer := range inferencers {
		inferencer.audioGate = audioGate
	}
	opts.onParticipantSessionOpen = func(string) {
		openedMu.Lock()
		opened++
		allOpened := opened == len(ids)
		openedMu.Unlock()
		if allOpened {
			audioGateOnce.Do(func() { close(audioGate) })
		}
	}
	var observedMu sync.Mutex
	observed := make(map[string][][]byte, len(ids))
	opts.OnAudioInput = func(id string, pcm []byte) error {
		copyPCM := append([]byte(nil), pcm...)
		observedMu.Lock()
		observed[id] = append(observed[id], copyPCM)
		observedMu.Unlock()
		inputFrames <- roomAudioFrame{id: id, pcm: copyPCM}
		return nil
	}
	opts.OnAudioOutput = func(id string, _ []byte) error {
		outputIDs <- id
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outcome := make(chan roomTestRunOutcome, 1)
	go func() {
		result, err := RunRoomWithResult(ctx, io.Discard, opts)
		outcome <- roomTestRunOutcome{result: result, err: err}
	}()

	want := map[string][]byte{
		"a": roomPCM16(5000, 10),
		"b": roomPCM16(4000, 10),
		"c": roomPCM16(3000, 10),
	}
	seen := make(map[string]bool, len(ids))
	deadline := time.NewTimer(roomFanoutSafetyTimeout)
	defer deadline.Stop()
	for len(seen) < len(ids) {
		select {
		case frame := <-inputFrames:
			if expected, ok := want[frame.id]; ok && bytes.Equal(frame.pcm, expected) {
				seen[frame.id] = true
			}
		case <-deadline.C:
			observedMu.Lock()
			observedSummary := make(map[string][]string, len(observed))
			for id, frames := range observed {
				for index, frame := range frames {
					if index == 8 {
						break
					}
					observedSummary[id] = append(observedSummary[id], fmt.Sprintf("%x", frame))
				}
			}
			observedMu.Unlock()
			t.Fatalf("timed out after %s waiting for N-1 mixed frames; seen %v; observed=%v", roomFanoutSafetyTimeout, seen, observedSummary)
		}
	}
	cancel()

	var got roomTestRunOutcome
	select {
	case got = <-outcome:
	case <-time.After(roomFanoutSafetyTimeout):
		t.Fatal("room did not terminate after cancellation")
	}
	if got.err != nil {
		t.Fatalf("room cancellation: %v", got.err)
	}
	if got.result.Reason != RoomTerminationStopped {
		t.Fatalf("room reason = %q, want %q", got.result.Reason, RoomTerminationStopped)
	}
	if len(got.result.Participants) != len(ids) {
		t.Fatalf("participant results = %d, want %d", len(got.result.Participants), len(ids))
	}
	for _, id := range ids {
		if !factoryCalls[id].WaitForClose || factoryCalls[id].APIKey == "" {
			t.Fatalf("factory options for %s = %+v, want live participant configuration", id, factoryCalls[id])
		}
	}
	for range ids {
		select {
		case <-outputIDs:
		case <-time.After(roomFanoutSafetyTimeout):
			t.Fatal("room did not observe every provider audio delta")
		}
	}
}

func TestRunRoom_DeliversPeerPCMToEachProviderSession(t *testing.T) {
	ids := []string{"alice", "bob"}
	values := map[string]int16{"alice": 1100, "bob": 2200}
	releaseAudio := make(chan struct{})
	releaseEnd := make(chan struct{})
	inferencers := make(map[string]*roomTestInferencer, len(ids))
	for _, id := range ids {
		inferencers[id] = &roomTestInferencer{events: []messages.StreamMessage{
			roomTestSessionOpen(id),
			roomTestMessageStart(),
			roomTestAudioEvent(values[id], 10),
			roomTestMessageEnd(),
		}, eventWait: func(index int) <-chan struct{} {
			switch index {
			case 2:
				return releaseAudio
			case 3:
				return releaseEnd
			default:
				return nil
			}
		}}
	}

	opened := make(chan string, len(ids))
	started := make(chan string, len(ids))
	ended := make(chan string, len(ids))
	inputFrames := make(chan roomAudioFrame, 128)
	diagnostics := make(chan struct {
		id     string
		record SessionDiagnosticRecord
	}, 128)
	opts, factoryCalls := newRoomTestRunOptions(ids, inferencers)
	opts.onParticipantSessionOpen = func(id string) { opened <- id }
	opts.onParticipantStream = func(id string, msg messages.StreamMessage) {
		switch msg.Type {
		case messages.StreamTypeMessageStart:
			started <- id
		case messages.StreamTypeMessageEnd:
			ended <- id
		}
	}
	opts.OnAudioInput = func(id string, pcm []byte) error {
		inputFrames <- roomAudioFrame{id: id, pcm: append([]byte(nil), pcm...)}
		return nil
	}
	opts.OnDiagnostic = func(id string, record SessionDiagnosticRecord) {
		diagnostics <- struct {
			id     string
			record SessionDiagnosticRecord
		}{id: id, record: record}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outcome := make(chan roomTestRunOutcome, 1)
	go func() {
		result, err := RunRoomWithResult(ctx, io.Discard, opts)
		outcome <- roomTestRunOutcome{result: result, err: err}
	}()

	seenOpened := make(map[string]struct{}, len(ids))
	for len(seenOpened) < len(ids) {
		select {
		case id := <-opened:
			seenOpened[id] = struct{}{}
		case <-time.After(2 * time.Second):
			t.Fatalf("session-open observations = %v, want %d participants", seenOpened, len(ids))
		}
	}
	seenStarted := make(map[string]struct{}, len(ids))
	for len(seenStarted) < len(ids) {
		select {
		case id := <-started:
			seenStarted[id] = struct{}{}
		case <-time.After(2 * time.Second):
			t.Fatalf("response-start observations = %v, want %d participants", seenStarted, len(ids))
		}
	}
	close(releaseAudio)

	want := map[string][]byte{
		"alice": roomPCM16(values["bob"], 10),
		"bob":   roomPCM16(values["alice"], 10),
	}
	for _, id := range ids {
		factoryOptions, ok := factoryCalls[id]
		if !ok {
			t.Fatalf("%s missing provider factory options", id)
		}
		if binding := factoryOptions.RTCDeviceBinding; binding.Registry != nil || binding.InputDevice != "" || binding.OutputDevice != "" || binding.InputPresent || binding.OutputPresent || binding.BypassSelfHearing {
			t.Fatalf("%s room provider received local-device feedback binding = %+v, want room-owned peer ingress without local policy", id, binding)
		}

		sessions := inferencers[id].sessionsSnapshot()
		if len(sessions) != 1 {
			t.Fatalf("%s sessions = %d, want one", id, len(sessions))
		}
		waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
		sawContentfulAudio := false
		sawCancel := false
		for !sawContentfulAudio {
			msg, ok := sessions[0].nextSent(waitCtx)
			if !ok {
				waitCancel()
				t.Fatalf("%s provider session received no peer audio", id)
			}
			switch msg.Type {
			case messages.StreamTypeResponseCancel:
				if sawCancel {
					waitCancel()
					t.Fatalf("%s provider session received duplicate response cancel", id)
				}
				sawCancel = true
			case messages.StreamTypeAudioDelta:
				value, ok := msg.Value.(*messages.AudioDeltaValue)
				if !ok || value == nil {
					waitCancel()
					t.Fatalf("%s provider-bound value = %T, want *AudioDeltaValue", id, msg.Value)
				}
				if bytes.Equal(value.Content, want[id]) {
					sawContentfulAudio = true
					if !sawCancel {
						waitCancel()
						t.Fatalf("%s peer audio arrived without ordered response cancel", id)
					}
				}
			case messages.StreamTypeTextStart, messages.StreamTypeTextDelta, messages.StreamTypeTextEnd:
				// A scripted session can echo a setup/user text turn while the
				// provider response is being admitted. It is unrelated to the
				// peer-audio contract; keep consuming it so the assertion remains
				// strict about RESPONSE.CANCEL ordering and exact AUDIO.DELTA
				// payloads without racing harmless text wire traffic.
				continue
			default:
				waitCancel()
				t.Fatalf("%s unexpected provider-bound message %s", id, msg.Type)
			}
		}
		waitCancel()
	}

	close(releaseEnd)
	seenEnded := make(map[string]struct{}, len(ids))
	for len(seenEnded) < len(ids) {
		select {
		case id := <-ended:
			seenEnded[id] = struct{}{}
		case <-time.After(2 * time.Second):
			t.Fatalf("response-end observations = %v, want %d participants", seenEnded, len(ids))
		}
	}
	cancel()
	got := <-outcome
	if got.err != nil {
		t.Fatalf("room cancellation: %v", got.err)
	}
	if got.result.Reason != RoomTerminationStopped {
		t.Fatalf("room reason = %q, want %q", got.result.Reason, RoomTerminationStopped)
	}

	inputCounts := make(map[string]int, len(ids))
	for {
		select {
		case frame := <-inputFrames:
			if bytes.Equal(frame.pcm, want[frame.id]) {
				inputCounts[frame.id]++
			}
		default:
			goto inputDrained
		}
	}
inputDrained:
	for _, id := range ids {
		if inputCounts[id] != 1 {
			t.Fatalf("%s contentful mixer frames = %d, want exactly one", id, inputCounts[id])
		}
	}
	metricsByID := make(map[string]SessionDiagnosticRecord, len(ids))
	ingressSummariesByID := make(map[string]SessionDiagnosticRecord, len(ids))
	for {
		select {
		case item := <-diagnostics:
			if item.record.Event == SessionDiagnosticEventMetrics {
				metricsByID[item.id] = item.record
			}
			if item.record.Event == SessionDiagnosticEventRoomAudioIngressSummary {
				ingressSummariesByID[item.id] = item.record
			}
		default:
			goto diagnosticsDrained
		}
	}
diagnosticsDrained:
	for _, id := range ids {
		record, ok := metricsByID[id]
		if !ok {
			t.Fatalf("%s missing terminal metrics diagnostic", id)
		}
		bytesReceived, err := strconv.ParseUint(record.Fields["input_audio_bytes"], 10, 64)
		if err != nil || bytesReceived == 0 {
			t.Fatalf("%s input_audio_bytes = %q, want non-zero", id, record.Fields["input_audio_bytes"])
		}
		summary, ok := ingressSummariesByID[id]
		if !ok {
			t.Fatalf("%s missing room ingress summary", id)
		}
		if summary.Fields[SessionDiagnosticFieldDeliveredBytes] != "20" || summary.Fields[SessionDiagnosticFieldRejectedBytes] != "0" {
			t.Fatalf("%s ingress summary = %v, want 20 delivered and 0 rejected bytes", id, summary.Fields)
		}
		if summary.Fields[SessionDiagnosticFieldContentLoss] != "false" {
			t.Fatalf("%s ingress summary content_loss = %q, want false", id, summary.Fields[SessionDiagnosticFieldContentLoss])
		}
	}
}

func TestRunRoom_ParticipantCloseDoesNotStopViableRoom(t *testing.T) {
	ids := []string{"a", "b", "c"}
	inferencers := map[string]*roomTestInferencer{
		"a": {events: []messages.StreamMessage{
			roomTestSessionOpen("a"),
			roomTestSessionClose("a", "complete"),
		}},
		"b": {events: []messages.StreamMessage{roomTestSessionOpen("b")}},
		"c": {events: []messages.StreamMessage{roomTestSessionOpen("c")}},
	}
	participantEvents := make(chan RoomParticipantResult, len(ids))
	opts, _ := newRoomTestRunOptions(ids, inferencers)
	opts.OnParticipantTerminated = func(result RoomParticipantResult) {
		participantEvents <- result
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outcome := make(chan roomTestRunOutcome, 1)
	go func() {
		result, err := RunRoomWithResult(ctx, io.Discard, opts)
		outcome <- roomTestRunOutcome{result: result, err: err}
	}()

	select {
	case event := <-participantEvents:
		if event.ParticipantID != "a" || event.Reason != ParticipantTerminationEnded {
			t.Fatalf("early participant event = %+v, want a/ended", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("participant a did not terminate independently")
	}
	for _, id := range []string{"b", "c"} {
		if sessions := inferencers[id].sessionsSnapshot(); len(sessions) != 1 {
			t.Fatalf("%s sessions = %d, want 1", id, len(sessions))
		} else if calls := sessions[0].closeCallsSnapshot(); calls != 0 {
			t.Fatalf("%s was closed when a ended; close calls = %d done=%v sent=%d", id, calls, sessions[0].doneSnapshot(), sessions[0].sentCountSnapshot())
		} else if sessions[0].doneSnapshot() {
			t.Fatalf("%s session ended when a terminated", id)
		}
	}
	cancel()

	select {
	case got := <-outcome:
		if got.err != nil {
			t.Fatalf("room cancellation: %v", got.err)
		}
		if got.result.Reason != RoomTerminationStopped {
			t.Fatalf("room reason = %q, want %q", got.result.Reason, RoomTerminationStopped)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("room did not terminate after cancellation")
	}
}

func TestRunRoom_WaitsForMixerWorkBeforeReturning(t *testing.T) {
	ids := []string{"a", "b"}
	inferencers := map[string]*roomTestInferencer{
		"a": {events: []messages.StreamMessage{roomTestSessionOpen("a")}},
		"b": {events: []messages.StreamMessage{roomTestSessionOpen("b")}},
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	opts, _ := newRoomTestRunOptions(ids, inferencers)
	opts.OnAudioInput = func(string, []byte) error {
		startOnce.Do(func() { close(started) })
		<-release
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outcome := make(chan roomTestRunOutcome, 1)
	go func() {
		result, err := RunRoomWithResult(ctx, io.Discard, opts)
		outcome <- roomTestRunOutcome{result: result, err: err}
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("mixer observer did not start")
	}
	cancel()
	select {
	case got := <-outcome:
		if got.err == nil || got.result.Reason != RoomTerminationFailed {
			t.Fatalf("room returned before mixer work completed: result=%+v err=%v", got.result, got.err)
		}
		if !strings.Contains(got.err.Error(), `participant "`) || !strings.Contains(got.err.Error(), "phase mixer") {
			t.Fatalf("mixer cleanup error = %v, want participant/phase diagnostic", got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("room did not return a bounded mixer cleanup diagnostic")
	}
	close(release)
}

func TestRunRoom_BoundsBlockedSessionCloseWithLifecycleDiagnostic(t *testing.T) {
	ids := []string{"a", "b"}
	closeStarted := make(chan struct{})
	closeRelease := make(chan struct{})
	inferencers := map[string]*roomTestInferencer{
		"a": {
			events:       []messages.StreamMessage{roomTestSessionOpen("a")},
			closeStarted: closeStarted,
			closeRelease: closeRelease,
		},
		"b": {events: []messages.StreamMessage{roomTestSessionOpen("b")}},
	}
	opts, _ := newRoomTestRunOptions(ids, inferencers)
	opened := make(chan string, len(ids))
	opts.onParticipantSessionOpen = func(id string) { opened <- id }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outcome := make(chan roomTestRunOutcome, 1)
	go func() {
		result, err := RunRoomWithResult(ctx, io.Discard, opts)
		outcome <- roomTestRunOutcome{result: result, err: err}
	}()

	seenOpened := make(map[string]struct{}, len(ids))
	for len(seenOpened) < len(ids) {
		select {
		case id := <-opened:
			seenOpened[id] = struct{}{}
		case <-time.After(2 * time.Second):
			t.Fatalf("session-open observations = %v, want %d participants", seenOpened, len(ids))
		}
	}
	cancel()
	select {
	case <-closeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("owned session Close did not start")
	}
	select {
	case got := <-outcome:
		if got.err == nil || got.result.Reason != RoomTerminationFailed {
			t.Fatalf("blocked session close returned cleanly: result=%+v err=%v", got.result, got.err)
		}
		if !strings.Contains(got.err.Error(), `participant "a"`) || !strings.Contains(got.err.Error(), "session.close") {
			t.Fatalf("session cleanup error = %v, want participant a/session.close diagnostic", got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("room did not return a bounded session cleanup diagnostic")
	}
	close(closeRelease)
	sessions := inferencers["a"].sessionsSnapshot()
	if len(sessions) != 1 {
		t.Fatalf("blocked session count = %d, want one", len(sessions))
	}
	select {
	case <-sessions[0].Done():
	case <-time.After(2 * time.Second):
		t.Fatal("blocked session did not finish")
	}
	if calls := sessions[0].closeCallsSnapshot(); calls != 1 {
		t.Fatalf("blocked session close calls = %d, want exactly once", calls)
	}
}

func TestRunRoom_BoundsBlockedRoomObserverWithLifecycleDiagnostic(t *testing.T) {
	ids := []string{"a", "b"}
	inferencers := map[string]*roomTestInferencer{
		"a": {events: []messages.StreamMessage{roomTestSessionOpen("a")}},
		"b": {events: []messages.StreamMessage{roomTestSessionOpen("b")}},
	}
	opened := make(chan string, len(ids))
	observerStarted := make(chan struct{})
	observerRelease := make(chan struct{})
	observerDone := make(chan struct{})
	var observerStartOnce sync.Once
	opts, _ := newRoomTestRunOptions(ids, inferencers)
	opts.onParticipantSessionOpen = func(id string) { opened <- id }
	opts.OnRoomTerminated = func(RoomResult) {
		observerStartOnce.Do(func() { close(observerStarted) })
		<-observerRelease
		close(observerDone)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outcome := make(chan roomTestRunOutcome, 1)
	go func() {
		result, err := RunRoomWithResult(ctx, io.Discard, opts)
		outcome <- roomTestRunOutcome{result: result, err: err}
	}()

	seenOpened := make(map[string]struct{}, len(ids))
	openDeadline := time.NewTimer(2 * time.Second)
	defer openDeadline.Stop()
	for len(seenOpened) < len(ids) {
		select {
		case id := <-opened:
			seenOpened[id] = struct{}{}
		case <-openDeadline.C:
			t.Fatalf("session-open observations = %v, want %d participants", seenOpened, len(ids))
		}
	}
	cancel()

	select {
	case <-observerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("room observer did not start")
	}
	select {
	case got := <-outcome:
		if got.err == nil || got.result.Reason != RoomTerminationFailed {
			t.Fatalf("blocked room observer returned cleanly: result=%+v err=%v", got.result, got.err)
		}
		if !strings.Contains(got.err.Error(), "room observer") {
			t.Fatalf("room observer cleanup error = %v, want room observer diagnostic", got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("room did not return a bounded room observer cleanup diagnostic")
	}
	close(observerRelease)
	select {
	case <-observerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked room observer did not finish after release")
	}
}

func TestRunRoom_StartupParticipantFailurePreservesViableParticipants(t *testing.T) {
	ids := []string{"a", "b", "c"}
	secret := "room-secret-value"
	inferencers := map[string]*roomTestInferencer{
		"a": {connectErr: fmt.Errorf("dial failed with %s", "secret-a")},
		"b": {events: []messages.StreamMessage{roomTestSessionOpen("b")}},
		"c": {events: []messages.StreamMessage{roomTestSessionOpen("c")}},
	}
	var mu sync.Mutex
	order := make([]string, 0, len(ids)*2)
	opts, factoryCalls := newRoomTestRunOptions(ids, inferencers)
	baseFactory := opts.SessionFactory
	opts.SessionFactory = func(participant room.Participant, options SessionRunOptions) (messages.SessionInferencer, error) {
		mu.Lock()
		order = append(order, "factory:"+participant.ID)
		mu.Unlock()
		return baseFactory(participant, options)
	}
	for id, inferencer := range inferencers {
		inferencer.onConnect = func() {
			mu.Lock()
			order = append(order, "connect:"+id)
			mu.Unlock()
		}
	}

	ready := make(chan string, len(ids))
	terminated := make(chan RoomParticipantResult, len(ids))
	opts.OnParticipantReady = func(result RoomParticipantReady) { ready <- result.ParticipantID }
	opts.OnParticipantTerminated = func(result RoomParticipantResult) { terminated <- result }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outcome := make(chan roomTestRunOutcome, 1)
	go func() {
		result, err := RunRoomWithResult(ctx, io.Discard, opts)
		outcome <- roomTestRunOutcome{result: result, err: err}
	}()

	readyIDs := make(map[string]struct{}, 2)
	for len(readyIDs) < 2 {
		select {
		case id := <-ready:
			if id == "a" {
				t.Fatal("failed participant was published as ready")
			}
			readyIDs[id] = struct{}{}
		case <-time.After(2 * time.Second):
			t.Fatalf("viable participants were not admitted: %v", readyIDs)
		}
	}
	if len(factoryCalls) != len(ids) {
		t.Fatalf("factory calls = %d, want %d", len(factoryCalls), len(ids))
	}

	firstConnect := len(order)
	for index, item := range order {
		if strings.HasPrefix(item, "connect:") {
			firstConnect = index
			break
		}
	}
	if firstConnect == len(order) {
		t.Fatal("no participant attempted connection")
	}
	for _, item := range order[:firstConnect] {
		if !strings.HasPrefix(item, "factory:") {
			t.Fatalf("startup order = %v, connection preceded configuration", order)
		}
	}
	for _, id := range []string{"b", "c"} {
		sessions := inferencers[id].sessionsSnapshot()
		if len(sessions) != 1 {
			t.Fatalf("viable participant %s sessions = %d, want one live session", id, len(sessions))
		}
		if sessions[0].closeCallsSnapshot() != 0 || sessions[0].doneSnapshot() {
			t.Fatalf("viable participant %s was stopped by sibling startup failure: close_calls=%d done=%v", id, sessions[0].closeCallsSnapshot(), sessions[0].doneSnapshot())
		}
	}

	cancel()
	select {
	case got := <-outcome:
		if got.err != nil {
			t.Fatalf("room after participant startup failure: %v", got.err)
		}
		if got.result.Reason != RoomTerminationStopped {
			t.Fatalf("room reason = %q, want %q", got.result.Reason, RoomTerminationStopped)
		}
		failed := got.result.Participants["a"]
		if failed.Reason != ParticipantTerminationError || failed.Connected || !strings.Contains(failed.Error, `room participant "a"`) {
			t.Fatalf("failed participant result = %+v, want isolated causal error", failed)
		}
		if strings.Contains(got.result.Error, secret) || strings.Contains(failed.Error, secret) {
			t.Fatalf("startup error leaked credential: result=%q participant=%q", got.result.Error, failed.Error)
		}
		for _, id := range []string{"b", "c"} {
			participant := got.result.Participants[id]
			if !participant.Connected || participant.Reason != ParticipantTerminationEnded || participant.Error != "" {
				t.Fatalf("viable participant %s result = %+v, want clean ended result", id, participant)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("room did not terminate after explicit cancellation")
	}
	terminalIDs := make(map[string]int, len(ids))
	for len(terminalIDs) < len(ids) {
		select {
		case result := <-terminated:
			terminalIDs[result.ParticipantID]++
		case <-time.After(2 * time.Second):
			t.Fatalf("participant terminal callbacks = %v, want one per participant", terminalIDs)
		}
	}
	for _, id := range ids {
		if terminalIDs[id] != 1 {
			t.Fatalf("participant %s terminal callbacks = %d, want exactly one", id, terminalIDs[id])
		}
	}
}

func TestRunRoom_SessionConstructionFailurePreservesViableParticipant(t *testing.T) {
	ids := []string{"a", "b"}
	inferencers := map[string]*roomTestInferencer{
		"a": {},
		"b": {events: []messages.StreamMessage{roomTestSessionOpen("b")}},
	}
	opts, _ := newRoomTestRunOptions(ids, inferencers)
	baseFactory := opts.SessionFactory
	opts.SessionFactory = func(participant room.Participant, options SessionRunOptions) (messages.SessionInferencer, error) {
		if participant.ID == "a" {
			return nil, errors.New("participant construction failed")
		}
		return baseFactory(participant, options)
	}
	ready := make(chan string, len(ids))
	terminated := make(chan RoomParticipantResult, len(ids))
	opts.OnParticipantReady = func(result RoomParticipantReady) { ready <- result.ParticipantID }
	opts.OnParticipantTerminated = func(result RoomParticipantResult) { terminated <- result }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outcomeCh := make(chan roomTestRunOutcome, 1)
	go func() {
		result, err := RunRoomWithResult(ctx, io.Discard, opts)
		outcomeCh <- roomTestRunOutcome{result: result, err: err}
	}()

	select {
	case id := <-ready:
		if id != "b" {
			t.Fatalf("ready participant = %q, want viable b", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("viable participant did not become ready")
	}
	session := inferencers["b"].sessionsSnapshot()[0]
	if session.closeCallsSnapshot() != 0 || session.doneSnapshot() {
		t.Fatalf("viable participant was stopped by construction failure: close_calls=%d done=%v", session.closeCallsSnapshot(), session.doneSnapshot())
	}
	cancel()
	select {
	case outcome := <-outcomeCh:
		if outcome.err != nil || outcome.result.Reason != RoomTerminationStopped {
			t.Fatalf("construction failure room result=%+v err=%v, want clean stopped room", outcome.result, outcome.err)
		}
		failed := outcome.result.Participants["a"]
		if failed.Reason != ParticipantTerminationError || !strings.Contains(failed.Error, "participant construction failed") {
			t.Fatalf("failed construction result = %+v, want isolated participant error", failed)
		}
		viable := outcome.result.Participants["b"]
		if !viable.Connected || viable.Reason != ParticipantTerminationEnded || viable.Error != "" {
			t.Fatalf("viable construction result = %+v, want clean ended participant", viable)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("room did not finish after stopping viable participant")
	}
	terminalIDs := make(map[string]int, len(ids))
	for len(terminalIDs) < len(ids) {
		select {
		case result := <-terminated:
			terminalIDs[result.ParticipantID]++
		case <-time.After(2 * time.Second):
			t.Fatalf("terminal callbacks = %v, want one per participant", terminalIDs)
		}
	}
	for _, id := range ids {
		if terminalIDs[id] != 1 {
			t.Fatalf("participant %s terminal callbacks = %d, want exactly one", id, terminalIDs[id])
		}
	}
}

func TestRunRoom_StopsWhenEveryParticipantReachesMaxTurns(t *testing.T) {
	ids := []string{"a", "b", "c"}
	inferencers := make(map[string]*roomTestInferencer, len(ids))
	for _, id := range ids {
		events := []messages.StreamMessage{roomTestSessionOpen(id)}
		events = append(events, roomTestResponse("turn one")...)
		events = append(events, roomTestResponse("turn two")...)
		inferencers[id] = &roomTestInferencer{events: events}
	}
	opts, _ := newRoomTestRunOptions(ids, inferencers)
	opts.Manifest.Room.MaxTurns = 2
	result, err := RunRoomWithResult(context.Background(), io.Discard, opts)
	if err != nil {
		t.Fatalf("max-turn room: %v", err)
	}
	if result.Reason != RoomTerminationMaxTurnsReached {
		t.Fatalf("room reason = %q, want %q", result.Reason, RoomTerminationMaxTurnsReached)
	}
	for _, id := range ids {
		participant := result.Participants[id]
		if participant.TurnsCompleted < 2 {
			t.Fatalf("participant %s turns = %d, want at least 2", id, participant.TurnsCompleted)
		}
	}
}

func TestRunRoom_StopsAtMaxDuration(t *testing.T) {
	ids := []string{"a", "b", "c"}
	inferencers := make(map[string]*roomTestInferencer, len(ids))
	for _, id := range ids {
		inferencers[id] = &roomTestInferencer{events: []messages.StreamMessage{roomTestSessionOpen(id)}}
	}
	opts, _ := newRoomTestRunOptions(ids, inferencers)
	opts.Manifest.Room.MaxDuration = 100 * time.Millisecond
	result, err := RunRoomWithResult(context.Background(), io.Discard, opts)
	if err != nil {
		t.Fatalf("duration-bounded room: %v", err)
	}
	if result.Reason != RoomTerminationMaxDurationReached {
		t.Fatalf("room reason = %q, want %q", result.Reason, RoomTerminationMaxDurationReached)
	}
}

func TestRunRoom_CompletesWhenEveryParticipantTerminates(t *testing.T) {
	ids := []string{"a", "b", "c"}
	inferencers := make(map[string]*roomTestInferencer, len(ids))
	for _, id := range ids {
		inferencers[id] = &roomTestInferencer{events: []messages.StreamMessage{
			roomTestSessionOpen(id),
			roomTestSessionClose(id, "complete"),
		}}
	}
	result, err := RunRoomWithResult(context.Background(), io.Discard, func() RoomRunOptions {
		opts, _ := newRoomTestRunOptions(ids, inferencers)
		return opts
	}())
	if err != nil {
		t.Fatalf("room with no viable participants: %v", err)
	}
	if result.Reason != RoomTerminationStopped {
		t.Fatalf("room reason = %q, want %q", result.Reason, RoomTerminationStopped)
	}
	for _, id := range ids {
		if participant := result.Participants[id]; participant.Reason != ParticipantTerminationEnded {
			t.Fatalf("participant %s reason = %q, want %q", id, participant.Reason, ParticipantTerminationEnded)
		}
	}
}

func TestRunRoom_ClassifiesTransportEndAsParticipantDisconnect(t *testing.T) {
	ids := []string{"a", "b", "c"}
	inferencers := map[string]*roomTestInferencer{
		"a": {events: []messages.StreamMessage{roomTestSessionOpen("a")}},
		"b": {events: []messages.StreamMessage{roomTestSessionOpen("b")}},
		"c": {events: []messages.StreamMessage{roomTestSessionOpen("c")}},
	}
	participantEvents := make(map[string]chan RoomParticipantResult, len(ids))
	for _, id := range ids {
		participantEvents[id] = make(chan RoomParticipantResult, 1)
	}
	opts, _ := newRoomTestRunOptions(ids, inferencers)
	sessionOpened := make(map[string]chan struct{}, len(ids))
	for _, id := range ids {
		sessionOpened[id] = make(chan struct{}, 1)
	}
	opts.onParticipantSessionOpen = func(id string) {
		select {
		case sessionOpened[id] <- struct{}{}:
		default:
		}
	}
	opts.OnParticipantTerminated = func(result RoomParticipantResult) {
		participantEvents[result.ParticipantID] <- result
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outcome := make(chan roomTestRunOutcome, 1)
	go func() {
		result, err := RunRoomWithResult(ctx, io.Discard, opts)
		outcome <- roomTestRunOutcome{result: result, err: err}
	}()

	select {
	case <-sessionOpened["a"]:
	case <-time.After(2 * time.Second):
		t.Fatal("target session did not reach observed admission")
	}
	for _, id := range []string{"b", "c"} {
		select {
		case <-sessionOpened[id]:
		case <-time.After(2 * time.Second):
			t.Fatalf("participant %q did not reach observed admission", id)
		}
	}
	sessions := inferencers["a"].sessionsSnapshot()
	if len(sessions) != 1 {
		t.Fatalf("target sessions = %d, want 1", len(sessions))
	}
	sessions[0].end()
	select {
	case event := <-participantEvents["a"]:
		if event.ParticipantID != "a" || event.Reason != ParticipantTerminationDisconnected {
			t.Fatalf("transport-ended event = %+v, want a/disconnected", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("transport-ended participant did not terminate")
	}
	cancel()
	select {
	case got := <-outcome:
		if got.err != nil {
			t.Fatalf("room cancellation: %v", got.err)
		}
		if got.result.Reason != RoomTerminationStopped {
			t.Fatalf("room reason = %q, want %q", got.result.Reason, RoomTerminationStopped)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("room did not terminate after cancellation")
	}
	for _, id := range []string{"b", "c"} {
		select {
		case event := <-participantEvents[id]:
			if event.ParticipantID != id || event.Reason != ParticipantTerminationEnded {
				t.Fatalf("sibling %q terminal event = %+v, want identity-preserving ended", id, event)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("sibling %q terminal callback unresolved", id)
		}
	}
}

type roomTestRunOutcome struct {
	result RoomResult
	err    error
}

type roomAudioFrame struct {
	id  string
	pcm []byte
}

type roomTestInferencer struct {
	connectErr   error
	events       []messages.StreamMessage
	eventWait    func(index int) <-chan struct{}
	audioGate    <-chan struct{}
	disconnect   bool
	terminalErr  error
	onConnect    func()
	closeStarted chan struct{}
	closeRelease <-chan struct{}

	mu       sync.Mutex
	sessions []*roomTestSession
}

func (i *roomTestInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if i.onConnect != nil {
		i.onConnect()
	}
	if i.connectErr != nil {
		return nil, i.connectErr
	}
	session := newRoomTestSession()
	session.closeStarted = i.closeStarted
	session.closeRelease = i.closeRelease
	session.terminalErr = i.terminalErr
	i.mu.Lock()
	i.sessions = append(i.sessions, session)
	i.mu.Unlock()
	events := append([]messages.StreamMessage(nil), i.events...)
	go func() {
		waitedForAudio := false
		for index, event := range events {
			if i.eventWait != nil {
				if wait := i.eventWait(index); wait != nil {
					select {
					case <-wait:
					case <-ctx.Done():
						return
					}
				}
			}
			if !waitedForAudio && event.Type == messages.StreamTypeAudioDelta && i.audioGate != nil {
				select {
				case <-i.audioGate:
				case <-ctx.Done():
					return
				}
				waitedForAudio = true
			}
			if !session.receive.Write(ctx, event) {
				return
			}
		}
		if i.disconnect {
			session.end()
		}
	}()
	return session, nil
}

func (i *roomTestInferencer) sessionsSnapshot() []*roomTestSession {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]*roomTestSession(nil), i.sessions...)
}

type roomTestSession struct {
	receive    *messages.TypedBuffer[messages.StreamMessage]
	done       chan struct{}
	sentNotify chan struct{}

	mu             sync.Mutex
	closeCalls     int
	terminalErr    error
	sent           []messages.StreamMessage
	sentRead       int
	once           sync.Once
	closeStartOnce sync.Once
	closeStarted   chan struct{}
	closeRelease   <-chan struct{}
}

func newRoomTestSession() *roomTestSession {
	return &roomTestSession{
		receive:    messages.NewTypedBuffer[messages.StreamMessage](64),
		done:       make(chan struct{}),
		sentNotify: make(chan struct{}, 1),
	}
}

func (s *roomTestSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	select {
	case <-s.done:
		return false
	case <-ctx.Done():
		return false
	default:
	}
	s.mu.Lock()
	s.sent = append(s.sent, msg)
	s.mu.Unlock()
	select {
	case s.sentNotify <- struct{}{}:
	default:
	}
	return true
}

func (s *roomTestSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *roomTestSession) Done() <-chan struct{} {
	return s.done
}

func (s *roomTestSession) TerminalError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminalErr
}

func (s *roomTestSession) Close() error {
	s.closeStartOnce.Do(func() {
		if s.closeStarted != nil {
			close(s.closeStarted)
		}
	})
	if s.closeRelease != nil {
		<-s.closeRelease
	}
	s.mu.Lock()
	s.closeCalls++
	s.mu.Unlock()
	s.end()
	return nil
}

func (s *roomTestSession) end() {
	s.once.Do(func() { close(s.done) })
}

func (s *roomTestSession) closeCallsSnapshot() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeCalls
}

func (s *roomTestSession) doneSnapshot() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *roomTestSession) sentCountSnapshot() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

func (s *roomTestSession) sentTypeCountSnapshot(want messages.StreamMessageType) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, message := range s.sent {
		if message.Type == want {
			count++
		}
	}
	return count
}

func (s *roomTestSession) nextSent(ctx context.Context) (messages.StreamMessage, bool) {
	if s == nil {
		return messages.StreamMessage{}, false
	}
	for {
		s.mu.Lock()
		if s.sentRead < len(s.sent) {
			msg := s.sent[s.sentRead]
			s.sentRead++
			s.mu.Unlock()
			return msg, true
		}
		s.mu.Unlock()
		select {
		case <-s.sentNotify:
		case <-ctx.Done():
			return messages.StreamMessage{}, false
		}
	}
}

func newRoomTestRunOptions(ids []string, inferencers map[string]*roomTestInferencer) (RoomRunOptions, map[string]SessionRunOptions) {
	credentials := make(map[string]string, len(ids))
	for _, id := range ids {
		credentials["ROOM_"+strings.ToUpper(id)+"_KEY"] = "secret-" + id
	}
	opts := RoomRunOptions{
		Manifest: room.Manifest{
			SchemaVersion: room.SchemaVersion,
			Room:          room.Room{MaxDuration: 5 * time.Second},
			Participants:  make([]room.Participant, 0, len(ids)),
		},
		CredentialLookup: func(name string) (string, bool) {
			value, ok := credentials[name]
			return value, ok
		},
		MixerConfig: room.PCM16MixerConfig{
			Format:            room.PCM16Format{SampleRate: 1000, Channels: 1, FrameDuration: 10 * time.Millisecond},
			InputQueueFrames:  4,
			OutputQueueFrames: 8,
		},
	}
	for index, id := range ids {
		participant := room.Participant{
			ID:           id,
			SystemPrompt: "room test participant " + id,
			Provider:     "test-provider",
			Model:        "test-model",
			APIKeyEnv:    "ROOM_" + strings.ToUpper(id) + "_KEY",
			Tools:        []string{},
		}
		if index == 0 {
			// Designate the first participant as the room's opener so this
			// shared fixture satisfies the manifest's "someone must speak
			// first" requirement without changing any other test behavior.
			participant.OpeningPrompt = "Start the room, " + id + "."
		}
		opts.Manifest.Participants = append(opts.Manifest.Participants, participant)
	}
	factoryCalls := make(map[string]SessionRunOptions, len(ids))
	var mu sync.Mutex
	opts.SessionFactory = func(participant room.Participant, options SessionRunOptions) (messages.SessionInferencer, error) {
		mu.Lock()
		factoryCalls[participant.ID] = options
		mu.Unlock()
		inferencer, ok := inferencers[participant.ID]
		if !ok {
			return nil, fmt.Errorf("missing test inferencer for %s", participant.ID)
		}
		return inferencer, nil
	}
	return opts, factoryCalls
}

func roomTestSessionOpen(id string) messages.StreamMessage {
	return messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue(id, "room-test"),
	}
}

func roomTestSessionClose(id, reason string) messages.StreamMessage {
	return messages.StreamMessage{
		Type:  messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValue(id, reason),
	}
}

func roomTestAudioEvent(value int16, samples int) messages.StreamMessage {
	return messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewAudioDeltaValue(roomPCM16(value, samples)),
	}
}

func roomTestMessageStart() messages.StreamMessage {
	return messages.StreamMessage{
		Type:  messages.StreamTypeMessageStart,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageStartValue(),
	}
}

func roomTestMessageEnd() messages.StreamMessage {
	return messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	}
}

func roomTestResponse(text string) []messages.StreamMessage {
	return []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(text)},
		roomTestMessageEnd(),
	}
}

func roomPCM16(value int16, samples int) []byte {
	pcm := make([]byte, samples*2)
	for index := 0; index < samples; index++ {
		pcm[index*2] = byte(value)
		pcm[index*2+1] = byte(value >> 8)
	}
	return pcm
}
