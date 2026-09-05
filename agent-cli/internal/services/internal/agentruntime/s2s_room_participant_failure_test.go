package agentruntime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestRunRoom_ParticipantFailurePublishesEventAndPreservesSurvivor(t *testing.T) {
	const secret = "secret-a"
	ids := []string{"a", "b"}
	inferencers := map[string]*roomTestInferencer{
		"a": {events: []messages.StreamMessage{roomTestSessionOpen("a")}},
		"b": {events: []messages.StreamMessage{roomTestSessionOpen("b")}},
	}
	broker, err := NewRoomEventBroker(ids)
	if err != nil {
		t.Fatalf("NewRoomEventBroker: %v", err)
	}
	defer broker.Close()
	server := httptest.NewServer(broker)
	defer server.Close()
	response, err := http.Get(server.URL + "/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /events status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	events := make(chan map[string]json.RawMessage, 32)
	streamErrors := make(chan error, 1)
	scanner := bufio.NewScanner(response.Body)
	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var payload map[string]json.RawMessage
			if decodeErr := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload); decodeErr != nil {
				streamErrors <- decodeErr
				return
			}
			events <- payload
		}
		streamErrors <- scanner.Err()
	}()
	nextEvent := func() map[string]json.RawMessage {
		t.Helper()
		select {
		case payload := <-events:
			return payload
		case streamErr := <-streamErrors:
			if streamErr == nil {
				t.Fatal("room event stream ended before terminal event")
			}
			t.Fatalf("room event stream: %v", streamErr)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for room event")
		}
		return nil
	}

	outputDir := t.TempDir()
	opened := map[string]chan struct{}{"a": make(chan struct{}, 1), "b": make(chan struct{}, 1)}
	output := make(chan string, 1)
	opts, _ := newRoomTestRunOptions(ids, inferencers)
	opts.OutputDir = outputDir
	opts.Stream = broker
	opts.OnAudioOutput = func(participantID string, _ []byte) error {
		if participantID == "b" {
			select {
			case output <- participantID:
			default:
			}
		}
		return nil
	}
	opts.onParticipantSessionOpen = func(participantID string) {
		if signal, ok := opened[participantID]; ok {
			signal <- struct{}{}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outcome := make(chan roomTestRunOutcome, 1)
	go func() {
		result, runErr := RunRoomWithResult(ctx, io.Discard, opts)
		outcome <- roomTestRunOutcome{result: result, err: runErr}
	}()

	for _, id := range ids {
		select {
		case <-opened[id]:
		case <-time.After(2 * time.Second):
			t.Fatalf("participant %q did not become live", id)
		}
	}
	aSession := inferencers["a"].sessionsSnapshot()[0]
	bSession := inferencers["b"].sessionsSnapshot()[0]
	aSentBeforeFailure := aSession.sentCountSnapshot()

	// The stream is a combined room stream, so B can observe A's typed failure
	// event while A is already absent from the coordinator active set.
	aSession.fail(errors.New("transport failed with " + secret))
	participantFailed := map[string]json.RawMessage(nil)
	failureEventCount := 0
	for participantFailed == nil {
		payload := nextEvent()
		if roomSSEString(t, payload, "type") != RoomStreamEventTypeRoom {
			continue
		}
		switch roomSSEString(t, payload, "event") {
		case RoomStreamEventParticipantFailed:
			failureEventCount++
			participantFailed = payload
		case RoomStreamEventRunTerminated:
			t.Fatalf("room terminated while survivor was live: %v", payload)
		}
	}
	if failureEventCount != 1 || roomSSEString(t, participantFailed, "participant_id") != "a" {
		t.Fatalf("participant failure event = %v, count=%d; want exactly one event for a", participantFailed, failureEventCount)
	}
	failureReason := roomSSEString(t, participantFailed, "reason")
	if strings.TrimSpace(failureReason) == "" || !strings.Contains(failureReason, "transport failed") {
		t.Fatalf("participant failure reason = %q, want sanitized transport cause", failureReason)
	}
	if strings.Contains(failureReason, secret) {
		t.Fatalf("participant failure reason leaked credential: %q", failureReason)
	}
	if bSession.doneSnapshot() || bSession.closeCallsSnapshot() != 0 {
		t.Fatalf("survivor session after A failure = done=%v close_calls=%d", bSession.doneSnapshot(), bSession.closeCallsSnapshot())
	}

	// B remains able to produce output after A's retirement. There is no active
	// target left for that output, so A must not receive a new provider message.
	bSession.publish(roomTestAudioEvent(2400, 10))
	select {
	case <-output:
	case <-time.After(2 * time.Second):
		t.Fatal("survivor output was not processed after sibling failure")
	}
	if got := aSession.sentCountSnapshot(); got != aSentBeforeFailure {
		t.Fatalf("failed participant received post-retirement output: sent count %d, want %d", got, aSentBeforeFailure)
	}
	if bSession.doneSnapshot() || bSession.closeCallsSnapshot() != 0 {
		t.Fatalf("survivor session stopped after survivor output = done=%v close_calls=%d", bSession.doneSnapshot(), bSession.closeCallsSnapshot())
	}

	cancel()
	terminalEventCount := 0
	for {
		payload := nextEvent()
		if roomSSEString(t, payload, "type") != RoomStreamEventTypeRoom {
			continue
		}
		switch roomSSEString(t, payload, "event") {
		case RoomStreamEventParticipantFailed:
			failureEventCount++
		case RoomStreamEventRunTerminated:
			terminalEventCount++
		}
		if terminalEventCount > 0 {
			break
		}
	}
	if failureEventCount != 1 || terminalEventCount != 1 {
		t.Fatalf("room lifecycle event counts: participant_failed=%d run_terminated=%d", failureEventCount, terminalEventCount)
	}
	select {
	case got := <-outcome:
		if got.err != nil {
			t.Fatalf("room after participant failure and explicit stop: %v", got.err)
		}
		if got.result.Reason != RoomTerminationStopped {
			t.Fatalf("room reason = %q, want stopped", got.result.Reason)
		}
		if failed := got.result.Participants["a"]; failed.Reason != ParticipantTerminationError || strings.Contains(failed.Error, secret) {
			t.Fatalf("failed participant result = %+v, want sanitized error", failed)
		}
		if survivor := got.result.Participants["b"]; survivor.Reason != ParticipantTerminationEnded || survivor.Error != "" {
			t.Fatalf("survivor result = %+v, want clean ended result", survivor)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("room did not finish after explicit stop")
	}

	timelineLines := readRoomEvidenceJSONLLines(t, filepath.Join(outputDir, RoomEvidenceTimelinePath))
	timelineFailureCount := 0
	for _, line := range timelineLines {
		var entry roomTimelineEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("decode room timeline entry: %v", err)
		}
		if entry.Event != RoomStreamEventParticipantFailed {
			continue
		}
		timelineFailureCount++
		if entry.Participant != "a" || entry.Fields["reason"] != failureReason {
			t.Fatalf("timeline participant failure = %+v, want participant a/reason %q", entry, failureReason)
		}
	}
	if timelineFailureCount != 1 {
		t.Fatalf("timeline participant_failed count = %d, want exactly one", timelineFailureCount)
	}
	if _, err := os.Stat(filepath.Join(outputDir, RoomEvidenceManifestPath)); err != nil {
		t.Fatalf("room evidence manifest: %v", err)
	}
}

func TestRunRoom_ParticipantFailureDrainsSurvivorOutput(t *testing.T) {
	const (
		failedParticipant   = "a"
		survivorParticipant = "b"
		survivorTranscript  = "survivor output remains authoritative"
	)
	survivorPCM := roomPCM16(3200, 10)
	inferencers := map[string]*roomTestInferencer{
		failedParticipant:   {events: []messages.StreamMessage{roomTestSessionOpen(failedParticipant)}},
		survivorParticipant: {events: []messages.StreamMessage{roomTestSessionOpen(survivorParticipant)}},
	}

	outputDir := filepath.Join(t.TempDir(), "room-run")
	opens := make(chan string, 2)
	audioRenderStarted := make(chan struct{})
	releaseAudioRender := make(chan struct{})
	renderedAudio := make(chan []byte, 1)
	survivorText := make(chan string, 1)
	terminated := make(chan RoomParticipantResult, 2)
	var audioRenderOnce sync.Once
	var releaseAudioOnce sync.Once
	releaseAudio := func() { releaseAudioOnce.Do(func() { close(releaseAudioRender) }) }
	t.Cleanup(releaseAudio)

	opts, _ := newRoomTestRunOptions([]string{failedParticipant, survivorParticipant}, inferencers)
	opts.OutputDir = outputDir
	opts.onParticipantSessionOpen = func(participantID string) { opens <- participantID }
	opts.OnParticipantTerminated = func(result RoomParticipantResult) { terminated <- result }
	opts.OnAudioOutput = func(participantID string, pcm []byte) error {
		if participantID != survivorParticipant {
			return nil
		}
		audioRenderOnce.Do(func() { close(audioRenderStarted) })
		<-releaseAudioRender
		renderedAudio <- append([]byte(nil), pcm...)
		return nil
	}
	opts.onParticipantStream = func(participantID string, msg messages.StreamMessage) {
		if participantID != survivorParticipant || msg.Type != messages.StreamTypeTextDelta {
			return
		}
		value, ok := msg.Value.(*messages.TextDeltaValue)
		if ok && value != nil {
			survivorText <- value.Content
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outcome := make(chan roomTestRunOutcome, 1)
	go func() {
		result, err := RunRoomWithResult(ctx, io.Discard, opts)
		outcome <- roomTestRunOutcome{result: result, err: err}
	}()

	for range []string{failedParticipant, survivorParticipant} {
		select {
		case <-opens:
		case <-time.After(2 * time.Second):
			t.Fatal("both room participants did not become live")
		}
	}
	aSession := inferencers[failedParticipant].sessionsSnapshot()[0]
	bSession := inferencers[survivorParticipant].sessionsSnapshot()[0]

	// B's provider output is accepted by the room observer before A fails, but
	// its render callback is held so the transcript behind that output remains
	// pending at the failure boundary. A room-local failure must not cancel B's
	// session or route A's error through B's done-error callback.
	survivorEvents := append([]messages.StreamMessage{
		{
			Type:  messages.StreamTypeAudioDelta,
			Role:  messages.RoleAssistant,
			Value: messages.NewAudioDeltaValue(survivorPCM),
		},
	}, roomTestResponse(survivorTranscript)...)
	bSession.publish(survivorEvents...)
	select {
	case <-audioRenderStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("survivor audio did not reach the room render boundary")
	}

	aSession.fail(errors.New("survivor drain transport failure"))
	var failedResult RoomParticipantResult
	for failedResult.ParticipantID != failedParticipant {
		select {
		case result := <-terminated:
			if result.ParticipantID == failedParticipant {
				failedResult = result
			}
		case <-time.After(2 * time.Second):
			t.Fatal("failed participant did not finalize")
		}
	}
	if bSession.doneSnapshot() || bSession.closeCallsSnapshot() != 0 {
		t.Fatalf("survivor session after sibling failure = done=%v close_calls=%d", bSession.doneSnapshot(), bSession.closeCallsSnapshot())
	}
	select {
	case <-renderedAudio:
		t.Fatal("survivor audio rendered before the failure boundary was released")
	default:
	}

	releaseAudio()
	select {
	case got := <-renderedAudio:
		if !bytes.Equal(got, survivorPCM) {
			t.Fatalf("rendered survivor audio = %v, want %v", got, survivorPCM)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("survivor audio was not rendered after sibling failure")
	}
	select {
	case got := <-survivorText:
		if got != survivorTranscript {
			t.Fatalf("survivor transcript = %q, want %q", got, survivorTranscript)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("survivor transcript was not delivered after sibling failure")
	}

	cancel()
	var got roomTestRunOutcome
	select {
	case got = <-outcome:
	case <-time.After(2 * time.Second):
		t.Fatal("room did not finish after explicit stop")
	}
	if got.err != nil {
		t.Fatalf("room after isolated failure and stop: %v", got.err)
	}
	if got.result.Reason != RoomTerminationStopped {
		t.Fatalf("room reason = %q, want %q", got.result.Reason, RoomTerminationStopped)
	}
	if failedResult.Reason != ParticipantTerminationDisconnected && failedResult.Reason != ParticipantTerminationError {
		t.Fatalf("failed participant result = %+v, want disconnected or error", failedResult)
	}
	survivorResult := got.result.Participants[survivorParticipant]
	if survivorResult.Reason != ParticipantTerminationEnded || survivorResult.Error != "" {
		t.Fatalf("survivor result = %+v, want clean ended result", survivorResult)
	}

	deltaData := readRoomEvidenceFile(t, filepath.Join(outputDir, "agent-"+survivorParticipant+".deltas.jsonl"))
	if !bytes.Contains(deltaData, []byte(survivorTranscript)) {
		t.Fatalf("survivor transcript evidence omitted %q: %s", survivorTranscript, deltaData)
	}
}

func (s *roomTestSession) fail(err error) {
	s.mu.Lock()
	s.terminalErr = err
	s.mu.Unlock()
	s.end()
}

func (s *roomTestSession) publish(events ...messages.StreamMessage) {
	for _, event := range events {
		if !s.receive.Write(context.Background(), event) {
			panic("room test session could not publish event")
		}
	}
}
