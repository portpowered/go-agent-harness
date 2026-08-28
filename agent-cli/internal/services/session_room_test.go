package services

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

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
	opts.OnAudioInput = func(id string, pcm []byte) error {
		inputFrames <- roomAudioFrame{id: id, pcm: append([]byte(nil), pcm...)}
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
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for len(seen) < len(ids) {
		select {
		case frame := <-inputFrames:
			if expected, ok := want[frame.id]; ok && bytes.Equal(frame.pcm, expected) {
				seen[frame.id] = true
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for N-1 mixed frames; seen %v", seen)
		}
	}
	cancel()

	var got roomTestRunOutcome
	select {
	case got = <-outcome:
	case <-time.After(2 * time.Second):
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
		case <-time.After(time.Second):
			t.Fatal("room did not observe every provider audio delta")
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

func TestRunRoom_StartupFailureAbortsAllParticipants(t *testing.T) {
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

	result, err := RunRoomWithResult(context.Background(), io.Discard, opts)
	if err == nil {
		t.Fatal("startup failure returned nil error")
	}
	if result.Reason != RoomTerminationFailed {
		t.Fatalf("room reason = %q, want %q", result.Reason, RoomTerminationFailed)
	}
	if strings.Contains(result.Error, secret) || strings.Contains(err.Error(), secret) {
		t.Fatalf("startup error leaked credential: result=%q err=%q", result.Error, err)
	}
	if !strings.Contains(result.Error, `room participant "a"`) {
		t.Fatalf("startup error = %q, want participant identity", result.Error)
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
		if len(sessions) != 1 || sessions[0].closeCallsSnapshot() == 0 {
			t.Fatalf("viable participant %s was not aborted: sessions=%d", id, len(sessions))
		}
	}
}

func TestRunRoom_StopsWhenEveryParticipantReachesMaxTurns(t *testing.T) {
	ids := []string{"a", "b", "c"}
	inferencers := make(map[string]*roomTestInferencer, len(ids))
	for _, id := range ids {
		inferencers[id] = &roomTestInferencer{events: []messages.StreamMessage{
			roomTestSessionOpen(id),
			roomTestMessageEnd(),
			roomTestMessageEnd(),
		}}
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

func TestRunRoom_FailsWhenEveryParticipantTerminates(t *testing.T) {
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
	if err == nil {
		t.Fatal("room with no viable participants returned nil error")
	}
	if result.Reason != RoomTerminationFailed {
		t.Fatalf("room reason = %q, want %q", result.Reason, RoomTerminationFailed)
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
	connectErr error
	events     []messages.StreamMessage
	disconnect bool
	onConnect  func()

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
	i.mu.Lock()
	i.sessions = append(i.sessions, session)
	i.mu.Unlock()
	events := append([]messages.StreamMessage(nil), i.events...)
	go func() {
		for _, event := range events {
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
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}

	mu         sync.Mutex
	closeCalls int
	sent       []messages.StreamMessage
	once       sync.Once
}

func newRoomTestSession() *roomTestSession {
	return &roomTestSession{
		receive: messages.NewTypedBuffer[messages.StreamMessage](64),
		done:    make(chan struct{}),
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
	return true
}

func (s *roomTestSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *roomTestSession) Done() <-chan struct{} {
	return s.done
}

func (s *roomTestSession) Close() error {
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
	for _, id := range ids {
		opts.Manifest.Participants = append(opts.Manifest.Participants, room.Participant{
			ID:           id,
			SystemPrompt: "room test participant " + id,
			Provider:     "test-provider",
			Model:        "test-model",
			APIKeyEnv:    "ROOM_" + strings.ToUpper(id) + "_KEY",
			Tools:        []string{},
		})
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

func roomTestMessageEnd() messages.StreamMessage {
	return messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
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
