package agentruntime

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

const hermeticRoomLatencyTestTimeout = 3 * time.Second

type hermeticLatencyInput struct {
	participantID string
	pcm           []byte
}

type hermeticLatencyResponseStart struct {
	participantID string
	responseID    string
	tick          uint64
}

type hermeticLatencyAudio struct {
	participantID string
	responseID    string
	pcm           []byte
	tick          uint64
}

type hermeticLatencyFanout struct {
	sourceID string
	targetID string
	pcm      []byte
}

// TestRunRoomWithResult_HermeticTurnToTurnLatency exercises the composed room
// path with two scripted providers. Every response is released by the test
// after the shared deterministic clock has advanced by exactly 600 ms, while
// the two mixer cadences are advanced only after real provider-to-peer fanout.
// The four scripted responses create three measurable peer transitions.
func TestRunRoomWithResult_HermeticTurnToTurnLatency(t *testing.T) {
	const (
		speakerID  = "speaker"
		listenerID = "listener"
	)

	pcmFixture := hermeticLatencyPCMFixture()
	clock := platformclock.NewDeterministic(time.Unix(1700000000, 0).UTC(), time.Millisecond)
	provider := newHermeticLatencyProvider([]string{speakerID, listenerID})
	cadenceReady := make(chan *hermeticLatencyCadence, 2)
	manifest := room.Manifest{
		SchemaVersion: room.SchemaVersion,
		Room:          room.Room{MaxTurns: 2, MaxDuration: hermeticRoomLatencyTestTimeout},
		Participants: []room.Participant{
			{ID: speakerID, SystemPrompt: "speaker system", Provider: "scripted", Model: "scripted-model", APIKeyEnv: "ROOM_SPEAKER_KEY", Tools: []string{}},
			{ID: listenerID, SystemPrompt: "listener system", Provider: "scripted", Model: "scripted-model", APIKeyEnv: "ROOM_LISTENER_KEY", Tools: []string{}},
		},
	}
	outputDir := filepath.Join(t.TempDir(), "room-latency")
	inputEvents := provider.inputEvents
	responseStarts := make(chan hermeticLatencyResponseStart, 8)
	audioEvents := make(chan hermeticLatencyAudio, 8)
	fanouts := make(chan hermeticLatencyFanout, 8)
	opened := make(chan string, len(manifest.Participants))
	var audioOrderMu sync.Mutex
	audioOrder := make([]string, 0, 4)

	roomCtx, cancel := context.WithTimeout(context.Background(), hermeticRoomLatencyTestTimeout)
	defer cancel()
	opts := RoomRunOptions{
		Manifest:  manifest,
		Clock:     clock,
		OutputDir: outputDir,
		MixerConfig: room.PCM16MixerConfig{
			Format:            room.PCM16Format{SampleRate: 1000, Channels: 1, FrameDuration: 20 * time.Millisecond},
			InputQueueFrames:  8,
			OutputQueueFrames: 8,
			CadenceFactory: func(time.Duration) room.PCM16Cadence {
				cadence := newHermeticLatencyCadence()
				cadenceReady <- cadence
				return cadence
			},
		},
		CredentialLookup: func(name string) (string, bool) {
			switch name {
			case "ROOM_SPEAKER_KEY", "ROOM_LISTENER_KEY":
				return "hermetic-room-key", true
			default:
				return "", false
			}
		},
		SessionInferencers: map[string]messages.SessionInferencer{
			speakerID:  provider.inferencer(speakerID),
			listenerID: provider.inferencer(listenerID),
		},
		onParticipantSessionOpen: func(participantID string) {
			opened <- participantID
		},
		onParticipantStream: func(participantID string, msg messages.StreamMessage) {
			if msg.Type != messages.StreamTypeMessageStart {
				return
			}
			responseStarts <- hermeticLatencyResponseStart{
				participantID: participantID,
				responseID:    msg.ResponseID,
				tick:          clock.Tick(),
			}
		},
		OnAudioOutput: func(participantID string, pcm []byte) error {
			audioOrderMu.Lock()
			audioOrder = append(audioOrder, participantID)
			audioOrderMu.Unlock()
			audioEvents <- hermeticLatencyAudio{
				participantID: participantID,
				responseID:    provider.currentResponseID(participantID),
				pcm:           append([]byte(nil), pcm...),
				tick:          clock.Tick(),
			}
			return nil
		},
		onParticipantAudioFanned: func(sourceID, targetID string, pcm []byte) {
			fanouts <- hermeticLatencyFanout{sourceID: sourceID, targetID: targetID, pcm: append([]byte(nil), pcm...)}
		},
	}

	runDone := make(chan roomTestRunOutcome, 1)
	go func() {
		result, err := RunRoomWithResult(roomCtx, io.Discard, opts)
		runDone <- roomTestRunOutcome{result: result, err: err}
	}()

	// Mixers are constructed in manifest order by the room composition root.
	var speakerCadence, listenerCadence *hermeticLatencyCadence
	select {
	case speakerCadence = <-cadenceReady:
	case <-time.After(hermeticRoomLatencyTestTimeout):
		t.Fatal("speaker mixer cadence was not created")
	}
	select {
	case listenerCadence = <-cadenceReady:
	case <-time.After(hermeticRoomLatencyTestTimeout):
		t.Fatal("listener mixer cadence was not created")
	}
	if speakerCadence == nil || listenerCadence == nil {
		t.Fatal("room did not create both deterministic mixer cadences")
	}
	for range manifest.Participants {
		select {
		case <-opened:
		case <-time.After(hermeticRoomLatencyTestTimeout):
			t.Fatal("room sessions did not reach SESSION.OPEN")
		}
	}

	// The opening response seeds the first peer sample. It is also scripted as
	// one ordinary non-empty commit so every response has the same provider
	// commit/create accounting contract.
	openingID, err := provider.startOpening(speakerID, pcmFixture)
	if err != nil {
		t.Fatalf("start opening response: %v", err)
	}
	start := waitHermeticLatencyResponseStart(t, responseStarts, speakerID, openingID)
	clock.AdvanceTo(start.tick + 600)
	if err := provider.releaseResponse(speakerID, openingID, pcmFixture); err != nil {
		t.Fatalf("release opening response: %v", err)
	}
	waitHermeticLatencyAudio(t, audioEvents, speakerID, openingID, pcmFixture, start.tick+600)
	waitHermeticLatencyFanout(t, fanouts, speakerID, listenerID, pcmFixture)

	// listener turn 1: listener hears speaker turn 1, then responds.
	listenerCadence.Advance()
	waitHermeticLatencyInput(t, inputEvents, listenerID, pcmFixture)
	clock.AdvanceTo(clock.Tick() + 60)
	if err := provider.stopSpeech(listenerID); err != nil {
		t.Fatalf("stop listener speech turn 1: %v", err)
	}
	listenerTurnOne := waitHermeticLatencyResponseStart(t, responseStarts, listenerID, "response-listener-01")
	clock.AdvanceTo(listenerTurnOne.tick + 600)
	if err := provider.releaseResponse(listenerID, listenerTurnOne.responseID, pcmFixture); err != nil {
		t.Fatalf("release listener turn 1: %v", err)
	}
	waitHermeticLatencyAudio(t, audioEvents, listenerID, listenerTurnOne.responseID, pcmFixture, listenerTurnOne.tick+600)
	waitHermeticLatencyFanout(t, fanouts, listenerID, speakerID, pcmFixture)

	// speaker turn 1: the speaker's second provider response is turn 2 for
	// room max-turn accounting, and the first measurable transition for it.
	speakerCadence.Advance()
	waitHermeticLatencyInput(t, inputEvents, speakerID, pcmFixture)
	clock.AdvanceTo(clock.Tick() + 60)
	if err := provider.stopSpeech(speakerID); err != nil {
		t.Fatalf("stop speaker speech turn 1: %v", err)
	}
	speakerTurnTwo := waitHermeticLatencyResponseStart(t, responseStarts, speakerID, "response-speaker-02")
	clock.AdvanceTo(speakerTurnTwo.tick + 600)
	if err := provider.releaseResponse(speakerID, speakerTurnTwo.responseID, pcmFixture); err != nil {
		t.Fatalf("release speaker turn 2: %v", err)
	}
	waitHermeticLatencyAudio(t, audioEvents, speakerID, speakerTurnTwo.responseID, pcmFixture, speakerTurnTwo.tick+600)
	waitHermeticLatencyFanout(t, fanouts, speakerID, listenerID, pcmFixture)

	// listener turn 2: this final response gives the artifact its third
	// transition and satisfies max turns for both participants.
	listenerCadence.Advance()
	waitHermeticLatencyInput(t, inputEvents, listenerID, pcmFixture)
	clock.AdvanceTo(clock.Tick() + 60)
	if err := provider.stopSpeech(listenerID); err != nil {
		t.Fatalf("stop listener speech turn 2: %v", err)
	}
	listenerTurnTwo := waitHermeticLatencyResponseStart(t, responseStarts, listenerID, "response-listener-02")
	clock.AdvanceTo(listenerTurnTwo.tick + 600)
	if err := provider.releaseResponse(listenerID, listenerTurnTwo.responseID, pcmFixture); err != nil {
		t.Fatalf("release listener turn 2: %v", err)
	}
	waitHermeticLatencyAudio(t, audioEvents, listenerID, listenerTurnTwo.responseID, pcmFixture, listenerTurnTwo.tick+600)
	waitHermeticLatencyFanout(t, fanouts, listenerID, speakerID, pcmFixture)

	var outcome roomTestRunOutcome
	select {
	case outcome = <-runDone:
	case <-time.After(hermeticRoomLatencyTestTimeout):
		t.Fatal("room did not terminate after the final admitted response")
	}
	if outcome.err != nil {
		t.Fatalf("hermetic latency room: %v", outcome.err)
	}
	if outcome.result.Reason != RoomTerminationMaxTurnsReached {
		t.Fatalf("room termination reason = %q, want %q", outcome.result.Reason, RoomTerminationMaxTurnsReached)
	}
	for _, participantID := range []string{speakerID, listenerID} {
		participantResult, ok := outcome.result.Participants[participantID]
		if !ok {
			t.Fatalf("room result missing participant %q", participantID)
		}
		if participantResult.TurnsCompleted != 2 {
			t.Fatalf("participant %q turns = %d, want 2", participantID, participantResult.TurnsCompleted)
		}
	}

	provider.assertScriptedTurns(t, speakerID, 2, [][]byte{pcmFixture, pcmFixture})
	provider.assertScriptedTurns(t, listenerID, 2, [][]byte{pcmFixture, pcmFixture})
	provider.assertNoOutboundResponseControls(t)

	audioOrderMu.Lock()
	gotAudioOrder := append([]string(nil), audioOrder...)
	audioOrderMu.Unlock()
	wantAudioOrder := []string{speakerID, listenerID, speakerID, listenerID}
	if fmt.Sprint(gotAudioOrder) != fmt.Sprint(wantAudioOrder) {
		t.Fatalf("audio output order = %v, want causal order %v", gotAudioOrder, wantAudioOrder)
	}
	select {
	case extra := <-audioEvents:
		t.Fatalf("unexpected duplicate audio frame after four responses: %+v", extra)
	default:
	}
	provider.assertInputFrames(t, map[string]int{speakerID: 1, listenerID: 2}, pcmFixture)

	report, err := ReadRoomLatencyReport(outputDir)
	if err != nil {
		t.Fatalf("read finalized room latency report: %v", err)
	}
	if report.EligibleCount != 3 || report.ExcludedCount != 1 {
		t.Fatalf("latency report counts = eligible %d excluded %d, want 3/1; transitions=%+v exclusions=%+v", report.EligibleCount, report.ExcludedCount, report.Transitions, report.Exclusions)
	}
	if len(report.Exclusions) != 1 || report.Exclusions[0].Reason != RoomLatencyReasonUncorrelatedLandmarks || report.Exclusions[0].EventCount != 1 {
		t.Fatalf("latency exclusion = %+v, want one terminal uncorrelated event", report.Exclusions)
	}
	wantTransitions := map[string]string{
		"listener-turn-000001": "response-listener-01",
		"speaker-turn-000001":  "response-speaker-02",
		"listener-turn-000002": "response-listener-02",
	}
	if len(report.Transitions) != len(wantTransitions) {
		t.Fatalf("latency transitions = %d, want %d: %+v", len(report.Transitions), len(wantTransitions), report.Transitions)
	}
	for _, transition := range report.Transitions {
		wantResponseID, ok := wantTransitions[transition.TransitionID]
		if !ok {
			t.Fatalf("unexpected latency transition = %+v", transition)
		}
		if !transition.Eligible || transition.ResponseID != wantResponseID {
			t.Fatalf("transition %q = %+v, want eligible response %q", transition.TransitionID, transition, wantResponseID)
		}
		if transition.ProviderMS < 599 || transition.ProviderMS > 601 {
			t.Fatalf("transition %q provider bucket = %d ms, want 600 +/- one clock tick", transition.TransitionID, transition.ProviderMS)
		}
		if transition.HarnessOwnedMS > 400 {
			t.Fatalf("transition %q harness-owned latency = %d ms, want <=400", transition.TransitionID, transition.HarnessOwnedMS)
		}
		if difference := transition.DirectGapMS - transition.FourBucketSumMS; difference > 21 || difference < -21 {
			t.Fatalf("transition %q direct/four-bucket gap = %d/%d ms, want <=20 ms plus rounding", transition.TransitionID, transition.DirectGapMS, transition.FourBucketSumMS)
		}
		if transition.TotalMS != transition.DirectGapMS {
			t.Fatalf("transition %q total = %d ms, want direct gap %d", transition.TransitionID, transition.TotalMS, transition.DirectGapMS)
		}
	}

	bundle, err := ReadRoomLatencyBundle(filepath.Join(outputDir, RoomLatencyArtifactPath))
	if err != nil {
		t.Fatalf("read finalized latency bundle: %v", err)
	}
	if len(bundle.Events) == 0 {
		t.Fatal("finalized latency bundle has no events")
	}
	derived, err := AnalyzeRoomLatencyBundle(bundle)
	if err != nil {
		t.Fatalf("reanalyze finalized latency bundle: %v", err)
	}
	if derived.EligibleCount != report.EligibleCount || derived.Summary != report.Summary {
		t.Fatalf("report is not reproducible from finalized bundle: read=%+v derived=%+v", report.Summary, derived.Summary)
	}
}

func hermeticLatencyPCMFixture() []byte {
	samples := []int16{0, 1, -1, 0, 2, -2, 0, 3, -3, 0, 4, -4, 0, 5, -5, 0, 1600, -1600, 0, 0}
	pcm := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(pcm[index*2:], uint16(sample))
	}
	return pcm
}

func waitHermeticLatencyResponseStart(t *testing.T, starts <-chan hermeticLatencyResponseStart, participantID, responseID string) hermeticLatencyResponseStart {
	t.Helper()
	select {
	case start := <-starts:
		if start.participantID != participantID || start.responseID != responseID {
			t.Fatalf("response start = %+v, want %s/%s", start, participantID, responseID)
		}
		return start
	case <-time.After(hermeticRoomLatencyTestTimeout):
		t.Fatalf("timed out waiting for response start %s/%s", participantID, responseID)
		return hermeticLatencyResponseStart{}
	}
}

func waitHermeticLatencyInput(t *testing.T, inputs <-chan hermeticLatencyInput, participantID string, wantPCM []byte) {
	t.Helper()
	select {
	case input := <-inputs:
		if input.participantID != participantID {
			t.Fatalf("input participant = %q, want %q", input.participantID, participantID)
		}
		if !bytes.Equal(input.pcm, wantPCM) {
			t.Fatalf("input PCM for %s = %v, want exact fixture %v", participantID, input.pcm, wantPCM)
		}
	case <-time.After(hermeticRoomLatencyTestTimeout):
		t.Fatalf("timed out waiting for non-empty input to %s", participantID)
	}
}

func waitHermeticLatencyAudio(t *testing.T, audio <-chan hermeticLatencyAudio, participantID, responseID string, wantPCM []byte, wantTick uint64) {
	t.Helper()
	select {
	case event := <-audio:
		if event.participantID != participantID || event.responseID != responseID {
			t.Fatalf("audio event = %+v, want %s/%s", event, participantID, responseID)
		}
		if event.tick != wantTick {
			t.Fatalf("first audio for %s/%s arrived at tick %d, want response.create tick + 600 (%d)", participantID, responseID, event.tick, wantTick)
		}
		if !bytes.Equal(event.pcm, wantPCM) {
			t.Fatalf("audio PCM for %s = %v, want exact fixture %v", responseID, event.pcm, wantPCM)
		}
	case <-time.After(hermeticRoomLatencyTestTimeout):
		t.Fatalf("timed out waiting for first audio for %s/%s", participantID, responseID)
	}
}

func waitHermeticLatencyFanout(t *testing.T, fanouts <-chan hermeticLatencyFanout, sourceID, targetID string, wantPCM []byte) {
	t.Helper()
	select {
	case fanout := <-fanouts:
		if fanout.sourceID != sourceID || fanout.targetID != targetID {
			t.Fatalf("fanout = %+v, want %s -> %s", fanout, sourceID, targetID)
		}
		if !bytes.Equal(fanout.pcm, wantPCM) {
			t.Fatalf("fanout PCM = %v, want exact fixture %v", fanout.pcm, wantPCM)
		}
	case <-time.After(hermeticRoomLatencyTestTimeout):
		t.Fatalf("timed out waiting for fanout %s -> %s", sourceID, targetID)
	}
}

type hermeticLatencyCadence struct {
	ticks   chan time.Time
	stopped chan struct{}
	once    sync.Once
}

func newHermeticLatencyCadence() *hermeticLatencyCadence {
	return &hermeticLatencyCadence{
		ticks:   make(chan time.Time, 2),
		stopped: make(chan struct{}),
	}
}

func (c *hermeticLatencyCadence) C() <-chan time.Time { return c.ticks }

func (c *hermeticLatencyCadence) Stop() {
	c.once.Do(func() { close(c.stopped) })
}

func (c *hermeticLatencyCadence) Advance() {
	select {
	case c.ticks <- time.Time{}:
	case <-c.stopped:
	}
}

type hermeticLatencyProvider struct {
	inputEvents  chan hermeticLatencyInput
	participants map[string]*hermeticLatencyParticipant
}

type hermeticLatencyParticipant struct {
	id      string
	session *hermeticLatencySession

	mu              sync.Mutex
	pendingInput    []byte
	responseActive  bool
	responseID      string
	responseCreates []string
	commits         [][]byte
	audioInputs     [][]byte
	outbound        []messages.StreamMessage
}

type hermeticLatencyInferencer struct {
	provider    *hermeticLatencyProvider
	participant string
}

type hermeticLatencySession struct {
	provider    *hermeticLatencyProvider
	participant *hermeticLatencyParticipant
	receive     *messages.TypedBuffer[messages.StreamMessage]
	done        chan struct{}
	once        sync.Once
}

func newHermeticLatencyProvider(participantIDs []string) *hermeticLatencyProvider {
	provider := &hermeticLatencyProvider{
		inputEvents:  make(chan hermeticLatencyInput, 8),
		participants: make(map[string]*hermeticLatencyParticipant, len(participantIDs)),
	}
	for _, participantID := range participantIDs {
		provider.participants[participantID] = &hermeticLatencyParticipant{id: participantID}
	}
	return provider
}

func (p *hermeticLatencyProvider) inferencer(participantID string) messages.SessionInferencer {
	return &hermeticLatencyInferencer{provider: p, participant: participantID}
}

func (i *hermeticLatencyInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if i == nil || i.provider == nil {
		return nil, errors.New("hermetic provider is nil")
	}
	participant := i.provider.participants[i.participant]
	if participant == nil {
		return nil, fmt.Errorf("unknown hermetic participant %q", i.participant)
	}
	session := &hermeticLatencySession{
		provider:    i.provider,
		participant: participant,
		receive:     messages.NewTypedBuffer[messages.StreamMessage](64),
		done:        make(chan struct{}),
	}
	participant.mu.Lock()
	if participant.session != nil {
		participant.mu.Unlock()
		return nil, fmt.Errorf("hermetic participant %q connected twice", i.participant)
	}
	participant.session = session
	participant.mu.Unlock()
	for _, event := range []messages.StreamMessage{
		{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("session-"+i.participant, "hermetic")},
		{Type: messages.StreamTypeSessionCreated, Value: messages.NewSessionCreatedValue("session-"+i.participant, "scripted-model")},
	} {
		if !session.enqueue(ctx, event) {
			return nil, fmt.Errorf("enqueue %s for %s", event.Type, i.participant)
		}
	}
	return session, nil
}

func (s *hermeticLatencySession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	select {
	case <-s.done:
		return false
	case <-ctx.Done():
		return false
	default:
	}
	s.participant.mu.Lock()
	s.participant.outbound = append(s.participant.outbound, msg)
	s.participant.mu.Unlock()
	if msg.Type == messages.StreamTypeAudioDelta {
		value, ok := msg.Value.(*messages.AudioDeltaValue)
		if ok && value != nil {
			s.participant.mu.Lock()
			s.participant.audioInputs = append(s.participant.audioInputs, append([]byte(nil), value.Content...))
			s.participant.mu.Unlock()
			s.provider.acceptAudio(s.participant.id, value.Content)
		}
	}
	return true
}

func (s *hermeticLatencySession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *hermeticLatencySession) Done() <-chan struct{} { return s.done }

func (s *hermeticLatencySession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

func (s *hermeticLatencySession) enqueue(ctx context.Context, msg messages.StreamMessage) bool {
	return s.receive.Write(ctx, msg)
}

func (p *hermeticLatencyProvider) acceptAudio(participantID string, pcm []byte) {
	if len(pcm) == 0 || hermeticLatencyPCMIsSilent(pcm) {
		return
	}
	participant := p.participants[participantID]
	if participant == nil {
		return
	}
	participant.mu.Lock()
	if len(participant.pendingInput) > 0 || participant.responseActive {
		participant.mu.Unlock()
		return
	}
	participant.pendingInput = append([]byte(nil), pcm...)
	participant.mu.Unlock()
	p.inputEvents <- hermeticLatencyInput{participantID: participantID, pcm: append([]byte(nil), pcm...)}
}

func hermeticLatencyPCMIsSilent(pcm []byte) bool {
	for _, value := range pcm {
		if value != 0 {
			return false
		}
	}
	return true
}

func (p *hermeticLatencyProvider) startOpening(participantID string, pcm []byte) (string, error) {
	return p.startResponse(participantID, pcm)
}

func (p *hermeticLatencyProvider) stopSpeech(participantID string) error {
	participant := p.participants[participantID]
	if participant == nil {
		return fmt.Errorf("unknown hermetic participant %q", participantID)
	}
	participant.mu.Lock()
	pcm := append([]byte(nil), participant.pendingInput...)
	participant.pendingInput = nil
	participant.mu.Unlock()
	if len(pcm) == 0 {
		return fmt.Errorf("participant %q has no pending non-empty input", participantID)
	}
	if !participant.session.enqueue(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeVADSpeechStopped,
		Value: messages.NewVADSpeechStoppedValue(),
	}) {
		return fmt.Errorf("enqueue speech stop for %s", participantID)
	}
	return p.startResponseAfterSpeechStop(participantID, pcm)
}

func (p *hermeticLatencyProvider) startResponseAfterSpeechStop(participantID string, pcm []byte) (err error) {
	participant := p.participants[participantID]
	participant.mu.Lock()
	participant.commits = append(participant.commits, append([]byte(nil), pcm...))
	participant.mu.Unlock()
	_, err = p.enqueueResponseBoundaries(participantID)
	return err
}

func (p *hermeticLatencyProvider) startResponse(participantID string, pcm []byte) (string, error) {
	participant := p.participants[participantID]
	if participant == nil {
		return "", fmt.Errorf("unknown hermetic participant %q", participantID)
	}
	participant.mu.Lock()
	participant.commits = append(participant.commits, append([]byte(nil), pcm...))
	participant.mu.Unlock()
	return p.enqueueResponseBoundaries(participantID)
}

func (p *hermeticLatencyProvider) enqueueResponseBoundaries(participantID string) (string, error) {
	participant := p.participants[participantID]
	participant.mu.Lock()
	responseID := fmt.Sprintf("response-%s-%02d", participantID, len(participant.responseCreates)+1)
	participant.responseCreates = append(participant.responseCreates, responseID)
	participant.responseID = responseID
	participant.responseActive = true
	session := participant.session
	participant.mu.Unlock()
	if session == nil {
		return "", fmt.Errorf("participant %q has no session", participantID)
	}
	for _, event := range []messages.StreamMessage{
		{Type: messages.StreamTypeInputItemAdded, Role: messages.RoleUser, Value: messages.NewInputItemAddedValue("item-" + responseID)},
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewMessageStartValue()},
	} {
		if !session.enqueue(context.Background(), event) {
			return "", fmt.Errorf("enqueue %s for %s", event.Type, participantID)
		}
	}
	return responseID, nil
}

func (p *hermeticLatencyProvider) releaseResponse(participantID, responseID string, pcm []byte) error {
	participant := p.participants[participantID]
	if participant == nil {
		return fmt.Errorf("unknown hermetic participant %q", participantID)
	}
	participant.mu.Lock()
	if !participant.responseActive || participant.responseID != responseID {
		participant.mu.Unlock()
		return fmt.Errorf("response %s is not active for %s", responseID, participantID)
	}
	participant.responseActive = false
	session := participant.session
	participant.mu.Unlock()
	if session == nil {
		return fmt.Errorf("participant %q has no session", participantID)
	}
	for _, event := range []messages.StreamMessage{
		{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewAudioDeltaValue(append([]byte(nil), pcm...))},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	} {
		if !session.enqueue(context.Background(), event) {
			return fmt.Errorf("enqueue %s for %s", event.Type, participantID)
		}
	}
	return nil
}

func (p *hermeticLatencyProvider) currentResponseID(participantID string) string {
	participant := p.participants[participantID]
	if participant == nil {
		return ""
	}
	participant.mu.Lock()
	defer participant.mu.Unlock()
	return participant.responseID
}

func (p *hermeticLatencyProvider) assertScriptedTurns(t *testing.T, participantID string, wantResponses int, wantCommits [][]byte) {
	t.Helper()
	participant := p.participants[participantID]
	participant.mu.Lock()
	responses := append([]string(nil), participant.responseCreates...)
	commits := make([][]byte, len(participant.commits))
	for index, pcm := range participant.commits {
		commits[index] = append([]byte(nil), pcm...)
	}
	participant.mu.Unlock()
	if len(responses) != wantResponses {
		t.Fatalf("%s response.create count = %d, want %d (%v)", participantID, len(responses), wantResponses, responses)
	}
	if len(commits) != len(wantCommits) {
		t.Fatalf("%s commit count = %d, want %d", participantID, len(commits), len(wantCommits))
	}
	for index, want := range wantCommits {
		if len(commits[index]) == 0 || !bytes.Equal(commits[index], want) {
			t.Fatalf("%s commit %d = %v, want one non-empty exact fixture %v", participantID, index+1, commits[index], want)
		}
	}
}

func (p *hermeticLatencyProvider) assertNoOutboundResponseControls(t *testing.T) {
	t.Helper()
	for participantID, participant := range p.participants {
		participant.mu.Lock()
		outbound := append([]messages.StreamMessage(nil), participant.outbound...)
		participant.mu.Unlock()
		for _, msg := range outbound {
			if msg.Type == messages.StreamTypeMessageEnd || msg.Type == messages.StreamTypeResponseCreate {
				t.Fatalf("scripted provider %s observed unexpected outbound %s", participantID, msg.Type)
			}
		}
	}
}

func (p *hermeticLatencyProvider) assertInputFrames(t *testing.T, wantCounts map[string]int, wantPCM []byte) {
	t.Helper()
	for participantID, participant := range p.participants {
		participant.mu.Lock()
		received := make([][]byte, len(participant.audioInputs))
		for index, pcm := range participant.audioInputs {
			received[index] = append([]byte(nil), pcm...)
		}
		participant.mu.Unlock()
		wantCount := wantCounts[participantID]
		if len(received) != wantCount {
			t.Fatalf("%s provider audio input frames = %d, want %d", participantID, len(received), wantCount)
		}
		for index, pcm := range received {
			if !bytes.Equal(pcm, wantPCM) {
				t.Fatalf("%s received frame %d = %v, want exact fixture %v", participantID, index+1, pcm, wantPCM)
			}
		}
	}
}
