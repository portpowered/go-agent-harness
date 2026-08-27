//go:build live

// This file is an explicit, credential-gated confirmation of the live-shaped
// barge-in matrix. The default integration suite never compiles or runs this
// provider probe; see docs/architecture/s2s-live-shaped-barge-in-v2-live.md.
package integration

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	liveS2SBargeInAPIKeyEnv = "OPENAI_API_KEY"
	liveS2SBargeInOptInEnv  = "AGENT_HARNESS_LIVE_S2S_BARGE_IN"
	liveS2SBargeInModel     = "gpt-realtime"
	liveS2SBargeInTurns     = 4
	liveS2SBargeInTimeout   = 90 * time.Second
	liveS2SBargeInFrameTime = 30 * time.Millisecond
)

// liveS2SBargeInTrace records only normalized boundary facts. It deliberately
// retains no transcript, provider session ID, or audio payload.
type liveS2SBargeInTrace struct {
	mu sync.Mutex

	responseOrdinal int
	responseOpen    bool
	sessionUpdated  bool
	sessionReady    chan struct{}
	sessionOnce     sync.Once
	created         chan int
	audio           chan int
	done            chan int
	events          []liveS2SBargeInStreamEvent
	inputStarts     map[int]int
}

type liveS2SBargeInStreamEvent struct {
	Type            messages.StreamMessageType
	ResponseOrdinal int
	AudioBytes      int
	TextBytes       int
}

func newLiveS2SBargeInTrace() *liveS2SBargeInTrace {
	return &liveS2SBargeInTrace{
		sessionReady: make(chan struct{}),
		created:      make(chan int, liveS2SBargeInTurns),
		audio:        make(chan int, liveS2SBargeInTurns),
		done:         make(chan int, liveS2SBargeInTurns),
		inputStarts:  make(map[int]int, liveS2SBargeInTurns),
	}
}

func (t *liveS2SBargeInTrace) observe(msg messages.StreamMessage) {
	if t == nil {
		return
	}
	var created, audio, done int
	ready := false
	t.mu.Lock()
	if msg.Type == messages.StreamTypeSessionUpdated && !t.sessionUpdated {
		t.sessionUpdated = true
		ready = true
	}
	if msg.Type == messages.StreamTypeMessageStart {
		t.responseOrdinal++
		t.responseOpen = true
		created = t.responseOrdinal
	}
	ordinal := t.responseOrdinal
	audioBytes := 0
	textBytes := 0
	if value, ok := msg.Value.(*messages.AudioDeltaValue); ok && value != nil {
		audioBytes = len(value.Content)
		if audioBytes > 0 && t.responseOpen {
			audio = ordinal
		}
	}
	if value, ok := msg.Value.(*messages.TextDeltaValue); ok && value != nil {
		textBytes = len(value.Content)
	}
	if value, ok := msg.Value.(*messages.TranscriptDeltaValue); ok && value != nil && msg.Role == messages.RoleAssistant {
		textBytes = len(value.Text)
	}
	if msg.Type == messages.StreamTypeMessageEnd && t.responseOpen {
		done = ordinal
		t.responseOpen = false
	}
	t.events = append(t.events, liveS2SBargeInStreamEvent{
		Type:            msg.Type,
		ResponseOrdinal: ordinal,
		AudioBytes:      audioBytes,
		TextBytes:       textBytes,
	})
	t.mu.Unlock()

	if ready {
		t.sessionOnce.Do(func() { close(t.sessionReady) })
	}
	for channel, value := range map[chan int]int{
		t.created: created,
		t.audio:   audio,
		t.done:    done,
	} {
		if value == 0 {
			continue
		}
		select {
		case channel <- value:
		default:
		}
	}
}

func (t *liveS2SBargeInTrace) waitForSessionUpdated(ctx context.Context) error {
	select {
	case <-t.sessionReady:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *liveS2SBargeInTrace) waitFor(channel <-chan int, minimum int, ctx context.Context) error {
	for {
		select {
		case ordinal := <-channel:
			if ordinal >= minimum {
				return nil
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (t *liveS2SBargeInTrace) markInputStart(turn int) {
	t.mu.Lock()
	t.inputStarts[turn] = len(t.events)
	t.mu.Unlock()
}

func (t *liveS2SBargeInTrace) snapshot() ([]liveS2SBargeInStreamEvent, map[int]int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	events := append([]liveS2SBargeInStreamEvent(nil), t.events...)
	starts := make(map[int]int, len(t.inputStarts))
	for turn, index := range t.inputStarts {
		starts[turn] = index
	}
	return events, starts
}

func (t *liveS2SBargeInTrace) evidence() string {
	events, starts := t.snapshot()
	parts := make([]string, 0, len(events))
	for _, event := range events {
		parts = append(parts, fmt.Sprintf("%s:r%d:a%d:t%d", event.Type, event.ResponseOrdinal, event.AudioBytes, event.TextBytes))
	}
	return fmt.Sprintf("input_starts=%v stream=[%s]", starts, strings.Join(parts, ","))
}

func liveS2SBargeInTraceBoundary(trace *liveS2SBargeInTrace, response, turn int, want messages.StreamMessageType) (before, after int, ok bool) {
	events, starts := trace.snapshot()
	start, ok := starts[turn]
	if !ok {
		return 0, 0, false
	}
	for index, event := range events {
		if event.ResponseOrdinal != response {
			continue
		}
		matched := event.Type == want
		if want == messages.StreamTypeAudioDelta {
			matched = event.AudioBytes > 0
		}
		if !matched {
			continue
		}
		if index < start {
			before++
		} else {
			after++
		}
	}
	return before, after, true
}

func (t *liveS2SBargeInTrace) outputAfterInputStart(response, turn int) (audio, text int, ok bool) {
	events, starts := t.snapshot()
	start, ok := starts[turn]
	if !ok {
		return 0, 0, false
	}
	for index, event := range events {
		if index < start || event.ResponseOrdinal != response {
			continue
		}
		audio += event.AudioBytes
		text += event.TextBytes
	}
	return audio, text, true
}

// liveS2SBargeInAudioReader paces real speech PCM at the harness frame rate,
// but releases each next utterance only from a normalized provider boundary.
// No wall-clock sleep is used to manufacture the collision itself.
type liveS2SBargeInAudioReader struct {
	mu       sync.Mutex
	trace    *liveS2SBargeInTrace
	segments []liveS2SBargeInAudioSegment
	segment  int
	frame    int
	gateUsed bool
	marker   bool
	paceBase time.Time
}

type liveS2SBargeInAudioSegment struct {
	turn      int
	frames    [][]byte
	gate      func(context.Context) error
	endOfTurn bool
}

func newLiveS2SBargeInAudioReader(t *testing.T, trace *liveS2SBargeInTrace) *liveS2SBargeInAudioReader {
	t.Helper()
	frameSet := make([][][]byte, 0, liveS2SBargeInTurns)
	for _, name := range []string{"multiturn_turn1.wav", "multiturn_turn2.wav", "multiturn_turn3.wav", "multiturn_turn4.wav"} {
		frameSet = append(frameSet, multiturnAudioFrames(t, locateCLIFixture(t, name)))
	}
	return &liveS2SBargeInAudioReader{
		trace: trace,
		segments: []liveS2SBargeInAudioSegment{
			{turn: 1, frames: frameSet[0], gate: trace.waitForSessionUpdated, endOfTurn: true},
			{turn: 2, frames: frameSet[1], gate: func(ctx context.Context) error {
				return trace.waitFor(trace.audio, 1, ctx)
			}, endOfTurn: true},
			{turn: 3, frames: frameSet[2], gate: func(ctx context.Context) error {
				return trace.waitFor(trace.created, 2, ctx)
			}, endOfTurn: true},
			{turn: 4, frames: frameSet[3], gate: func(ctx context.Context) error {
				return trace.waitFor(trace.done, 3, ctx)
			}},
		},
	}
}

func (r *liveS2SBargeInAudioReader) Read(p []byte) (int, error) {
	return r.ReadContext(context.Background(), p)
}

func (r *liveS2SBargeInAudioReader) ReadContext(ctx context.Context, p []byte) (int, error) {
	if len(p) != audio.FrameSize*2 {
		return 0, fmt.Errorf("live barge-in reader received %d bytes, want %d", len(p), audio.FrameSize*2)
	}
	for {
		r.mu.Lock()
		if r.segment >= len(r.segments) {
			r.mu.Unlock()
			return 0, io.EOF
		}
		segment := r.segments[r.segment]
		if !r.gateUsed {
			r.gateUsed = true
			gate := segment.gate
			r.mu.Unlock()
			if gate != nil {
				if err := gate(ctx); err != nil {
					return 0, err
				}
			}
			r.mu.Lock()
			r.paceBase = time.Now()
			r.mu.Unlock()
			continue
		}
		if r.frame < len(segment.frames) {
			frameIndex := r.frame
			frame := segment.frames[frameIndex]
			r.frame++
			paceBase := r.paceBase
			r.mu.Unlock()
			if frameIndex > 0 {
				if err := waitLiveS2SFrame(ctx, paceBase.Add(time.Duration(frameIndex)*liveS2SBargeInFrameTime)); err != nil {
					return 0, err
				}
			}
			if frameIndex == 0 {
				r.trace.markInputStart(segment.turn)
			}
			copy(p, frame)
			return len(p), nil
		}
		if !segment.endOfTurn {
			r.segment = len(r.segments)
			r.mu.Unlock()
			return 0, io.EOF
		}
		if !r.marker {
			r.marker = true
			r.mu.Unlock()
			return 0, audio.ErrEndOfTurn
		}
		r.segment++
		r.frame = 0
		r.gateUsed = false
		r.marker = false
		r.paceBase = time.Time{}
		r.mu.Unlock()
	}
}

func waitLiveS2SFrame(ctx context.Context, target time.Time) error {
	delay := time.Until(target)
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (*liveS2SBargeInAudioReader) Close() error { return nil }

type liveS2SBargeInRuntimeObserver struct {
	mu           sync.Mutex
	observations []liveS2SBargeInRuntimeFact
}

type liveS2SBargeInRuntimeFact struct {
	Kind               services.SessionRuntimeObservationKind
	PayloadBytes       int
	TurnsCompleted     int
	InputCommit        int
	Clean              bool
	HasError           bool
	HasFinalAccounting bool
}

func (o *liveS2SBargeInRuntimeObserver) ObserveSessionRuntime(observation services.SessionRuntimeObservation) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.observations = append(o.observations, liveS2SBargeInRuntimeFact{
		Kind:               observation.Kind,
		PayloadBytes:       len(observation.Payload),
		TurnsCompleted:     observation.TurnsCompleted,
		InputCommit:        observation.InputCommit,
		Clean:              observation.Clean,
		HasError:           observation.Error != "",
		HasFinalAccounting: observation.FinalAccounting != nil,
	})
	o.mu.Unlock()
}

func (o *liveS2SBargeInRuntimeObserver) snapshot() []liveS2SBargeInRuntimeFact {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]liveS2SBargeInRuntimeFact(nil), o.observations...)
}

func liveS2SBargeInRuntimeEvidence(observations []liveS2SBargeInRuntimeFact) string {
	parts := make([]string, 0, len(observations))
	for _, observation := range observations {
		parts = append(parts, fmt.Sprintf("%s{bytes=%d,turns=%d,commit=%d,clean=%t,error=%t,accounting=%t}",
			observation.Kind,
			observation.PayloadBytes,
			observation.TurnsCompleted,
			observation.InputCommit,
			observation.Clean,
			observation.HasError,
			observation.HasFinalAccounting,
		))
	}
	return strings.Join(parts, ",")
}

type liveS2SBargeInTurn struct {
	Ordinal        int
	FirstAppend    int
	LastAppend     int
	AppendCount    int
	AudioBytes     int
	Commit         int
	ResponseCreate int
	UserItem       int
	UserItemID     string
}

type liveS2SBargeInResponse struct {
	Ordinal           int
	ID                string
	Turn              int
	Created           int
	FirstAudio        int
	AudioDeltas       int
	TextDeltas        int
	Cancel            int
	Done              int
	Status            string
	StatusReason      string
	PostCancelAudio   int
	PostCancelText    int
	PostTerminalAudio int
	PostTerminalText  int
}

type liveS2SBargeInLedger struct {
	Turns           []*liveS2SBargeInTurn
	Responses       []*liveS2SBargeInResponse
	ByResponseID    map[string]*liveS2SBargeInResponse
	CancelSequences []int
	SessionCreated  int
	SessionUpdated  int
	SessionClosed   int
	UserItems       int
	ProviderErrors  []string
	Violations      []string
}

func newLiveS2SBargeInLedger() *liveS2SBargeInLedger {
	return &liveS2SBargeInLedger{ByResponseID: make(map[string]*liveS2SBargeInResponse)}
}

func (l *liveS2SBargeInLedger) evidence() string {
	responses := make([]string, 0, len(l.Responses))
	for _, response := range l.Responses {
		responses = append(responses, fmt.Sprintf("R%d{turn=%d,created=%d,audio=%d,text=%d,cancel=%d,done=%d:%s,post_cancel=%d/%d,post_terminal=%d/%d}",
			response.Ordinal,
			response.Turn,
			response.Created,
			response.AudioDeltas,
			response.TextDeltas,
			response.Cancel,
			response.Done,
			liveS2SSafeStatus(response.Status, response.StatusReason),
			response.PostCancelAudio,
			response.PostCancelText,
			response.PostTerminalAudio,
			response.PostTerminalText,
		))
	}
	turns := make([]string, 0, len(l.Turns))
	for _, turn := range l.Turns {
		turns = append(turns, fmt.Sprintf("T%d{append=%d/%d,bytes=%d,commit=%d,create=%d,user=%s@%d}",
			turn.Ordinal,
			turn.AppendCount,
			turn.LastAppend-turn.FirstAppend+1,
			turn.AudioBytes,
			turn.Commit,
			turn.ResponseCreate,
			turn.UserItemID,
			turn.UserItem,
		))
	}
	return fmt.Sprintf("turns=[%s] responses=[%s] cancel_count=%d session_created=%d session_updated=%d session_closed=%d user_items=%d provider_errors=%v violations=%v",
		strings.Join(turns, ";"),
		strings.Join(responses, ";"),
		len(l.CancelSequences),
		l.SessionCreated,
		l.SessionUpdated,
		l.SessionClosed,
		l.UserItems,
		l.ProviderErrors,
		l.Violations,
	)
}

func validateLiveS2SBargeInCapture(capture gwtesting.SessionCapture, trace *liveS2SBargeInTrace) (*liveS2SBargeInLedger, error) {
	ledger := newLiveS2SBargeInLedger()
	if capture.Version != gwtesting.SessionCaptureVersion {
		ledger.Violations = append(ledger.Violations, "capture version is not current")
	}
	if capture.Provider.Name != "openai" || capture.Provider.Model != liveS2SBargeInModel {
		ledger.Violations = append(ledger.Violations, "capture provider/model is not openai/gpt-realtime")
	}
	lastSequence := 0
	for index, record := range capture.Records {
		if record.Sequence <= lastSequence {
			ledger.Violations = append(ledger.Violations, fmt.Sprintf("capture sequence regressed at record %d", index))
		}
		lastSequence = record.Sequence
		if record.PayloadType != gwtesting.SessionPayloadTypeWebSocketMessage {
			ledger.Violations = append(ledger.Violations, fmt.Sprintf("record %d is not a raw websocket event", index))
		}
		liveS2SValidateRecord(ledger, record)
	}
	liveS2SAssignCancelIdentities(ledger)

	if len(ledger.Turns) < liveS2SBargeInTurns || len(ledger.Responses) < liveS2SBargeInTurns {
		return ledger, &liveS2SBargeInInconclusiveError{Reason: "fewer than four observed input/response turns"}
	}
	if ledger.SessionCreated == 0 || ledger.SessionUpdated == 0 {
		return ledger, &liveS2SBargeInInconclusiveError{Reason: "session readiness boundary was not observed"}
	}
	if len(ledger.CancelSequences) < 2 {
		return ledger, &liveS2SBargeInInconclusiveError{Reason: "the active-audio and pre-first-audio cancellation boundaries did not both occur"}
	}
	if len(ledger.Turns) != liveS2SBargeInTurns || len(ledger.Responses) != liveS2SBargeInTurns {
		ledger.Violations = append(ledger.Violations, "the bounded run produced duplicate or missing turns")
	}
	if ledger.SessionCreated != 1 {
		ledger.Violations = append(ledger.Violations, "session was not reused for all collisions")
	}
	if ledger.UserItems != liveS2SBargeInTurns {
		ledger.Violations = append(ledger.Violations, "user item identity count did not reconcile with input turns")
	}

	for index, turn := range ledger.Turns {
		want := index + 1
		if turn.Ordinal != want || turn.AppendCount == 0 || turn.AudioBytes == 0 || turn.Commit == 0 || turn.ResponseCreate == 0 {
			ledger.Violations = append(ledger.Violations, fmt.Sprintf("T%d does not have one non-empty append group, commit, and response request", want))
		}
		if turn.Commit == 0 || turn.ResponseCreate == 0 {
			continue
		}
		if turn.UserItem == 0 || turn.UserItemID == "" {
			ledger.Violations = append(ledger.Violations, fmt.Sprintf("T%d has no attributable redacted user item identity", want))
		}
		if turn.Commit >= turn.ResponseCreate {
			ledger.Violations = append(ledger.Violations, fmt.Sprintf("T%d commit/response request order is invalid", want))
		}
	}
	for index, response := range ledger.Responses {
		wantTurn := index + 1
		if response.Ordinal != index+1 || response.Turn != wantTurn || response.ID == "" {
			ledger.Violations = append(ledger.Violations, fmt.Sprintf("R%d identity or input ownership is invalid", index+1))
		}
		if response.Done == 0 || response.Status == "" {
			ledger.Violations = append(ledger.Violations, fmt.Sprintf("R%d has no explicit terminal status", index+1))
		}
		if response.PostTerminalAudio != 0 || response.PostTerminalText != 0 {
			ledger.Violations = append(ledger.Violations, fmt.Sprintf("R%d emitted output after its terminal boundary", index+1))
		}
	}

	activeBefore, activeAfter, activeOK := liveS2SBargeInTraceBoundary(trace, 1, 2, messages.StreamTypeAudioDelta)
	if !activeOK || activeBefore == 0 || activeAfter != 0 {
		return ledger, &liveS2SBargeInInconclusiveError{Reason: "active assistant audio was not observed before the second input without post-input output"}
	}
	createdBefore, _, createdOK := liveS2SBargeInTraceBoundary(trace, 2, 3, messages.StreamTypeMessageStart)
	preAudioBefore, _, preAudioOK := liveS2SBargeInTraceBoundary(trace, 2, 3, messages.StreamTypeAudioDelta)
	if !createdOK || createdBefore == 0 || !preAudioOK || preAudioBefore != 0 {
		return ledger, &liveS2SBargeInInconclusiveError{Reason: "response-created-before-first-audio boundary was not observed"}
	}
	completionBefore, _, completionOK := liveS2SBargeInTraceBoundary(trace, 3, 4, messages.StreamTypeMessageEnd)
	if !completionOK || completionBefore == 0 {
		return ledger, &liveS2SBargeInInconclusiveError{Reason: "completion-boundary continuation was not observed"}
	}
	for response, inputTurn := range map[int]int{1: 2, 2: 3} {
		audioAfter, textAfter, ok := trace.outputAfterInputStart(response, inputTurn)
		if !ok {
			return ledger, &liveS2SBargeInInconclusiveError{Reason: "input release boundary was not observed for the selected collision"}
		}
		if audioAfter != 0 || textAfter != 0 {
			ledger.Violations = append(ledger.Violations, fmt.Sprintf("R%d produced output after the gated interrupting input began", response))
		}
	}

	if !liveS2SResponseCancelled(ledger.Responses[0]) || !liveS2SResponseCancelled(ledger.Responses[1]) {
		ledger.Violations = append(ledger.Violations, "the first two live responses were not cancelled explicitly")
	}
	for _, response := range ledger.Responses[2:] {
		if response.Cancel != 0 || !strings.EqualFold(response.Status, "completed") {
			ledger.Violations = append(ledger.Violations, fmt.Sprintf("R%d did not win the completion/continuation boundary without cancel", response.Ordinal))
		}
		if response.AudioDeltas == 0 && response.TextDeltas == 0 {
			return ledger, &liveS2SBargeInInconclusiveError{Reason: fmt.Sprintf("R%d produced no observable replacement output", response.Ordinal)}
		}
	}
	if ledger.Responses[0].AudioDeltas == 0 {
		return ledger, &liveS2SBargeInInconclusiveError{Reason: "the first response produced no active assistant audio"}
	}
	if ledger.Responses[1].AudioDeltas != 0 {
		ledger.Violations = append(ledger.Violations, "the pre-first-audio response emitted audio before its cancellation")
	}
	if len(ledger.Violations) > 0 {
		return ledger, errors.New(strings.Join(ledger.Violations, "; "))
	}
	return ledger, nil
}

func liveS2SValidateRecord(ledger *liveS2SBargeInLedger, record gwtesting.CapturedSessionEvent) {
	payload := liveS2SPayload(record)
	sequence := record.Sequence
	client := record.Direction == gwtesting.DirectionClientToServer
	server := record.Direction == gwtesting.DirectionServerToClient
	switch record.Type {
	case "session.created":
		if server {
			ledger.SessionCreated++
		}
	case "session.updated":
		if server {
			ledger.SessionUpdated++
		}
	case "input_audio_buffer.append":
		if !client {
			ledger.Violations = append(ledger.Violations, "input append had the wrong direction")
			return
		}
		turn := liveS2SCurrentTurn(ledger)
		encoded := liveS2SJSONField(payload, "audio")
		if encoded == "" {
			ledger.Violations = append(ledger.Violations, fmt.Sprintf("T%d input append was empty", turn.Ordinal))
			return
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(decoded) == 0 {
			ledger.Violations = append(ledger.Violations, fmt.Sprintf("T%d input append was not non-empty base64 audio", turn.Ordinal))
			return
		}
		turn.AppendCount++
		turn.AudioBytes += len(decoded)
		if turn.FirstAppend == 0 {
			turn.FirstAppend = sequence
		}
		turn.LastAppend = sequence
	case "input_audio_buffer.commit":
		if !client {
			ledger.Violations = append(ledger.Violations, "input commit had the wrong direction")
			return
		}
		turn := liveS2SCurrentTurn(ledger)
		if turn.Commit != 0 {
			ledger.Violations = append(ledger.Violations, fmt.Sprintf("T%d received duplicate input commit", turn.Ordinal))
		}
		if turn.AppendCount == 0 {
			ledger.Violations = append(ledger.Violations, fmt.Sprintf("T%d committed without non-empty input", turn.Ordinal))
		}
		turn.Commit = sequence
	case "response.create":
		if !client {
			ledger.Violations = append(ledger.Violations, "response request had the wrong direction")
			return
		}
		turn := liveS2SResponseTurn(ledger)
		if turn.ResponseCreate != 0 {
			ledger.Violations = append(ledger.Violations, fmt.Sprintf("T%d received duplicate response request", turn.Ordinal))
		}
		if turn.Commit == 0 {
			ledger.Violations = append(ledger.Violations, fmt.Sprintf("T%d requested a response before commit", turn.Ordinal))
		}
		turn.ResponseCreate = sequence
	case "conversation.item.created":
		if !server || liveS2SJSONField(payload, "item.role") != "user" {
			return
		}
		if liveS2SJSONField(payload, "item.id") == "" {
			ledger.Violations = append(ledger.Violations, "user conversation item had no identity")
			return
		}
		ledger.UserItems++
		liveS2SAssignUserItem(ledger, sequence, fmt.Sprintf("I%d", ledger.UserItems))
	case "response.created":
		if !server {
			ledger.Violations = append(ledger.Violations, "response creation had the wrong direction")
			return
		}
		id := liveS2SJSONField(payload, "response.id", "response_id")
		if id == "" {
			ledger.Violations = append(ledger.Violations, "response creation had no identity")
			return
		}
		if _, exists := ledger.ByResponseID[id]; exists {
			ledger.Violations = append(ledger.Violations, "response identity was created more than once")
			return
		}
		response := &liveS2SBargeInResponse{
			Ordinal: len(ledger.Responses) + 1,
			ID:      id,
			Turn:    len(ledger.Responses) + 1,
			Created: sequence,
		}
		ledger.Responses = append(ledger.Responses, response)
		ledger.ByResponseID[id] = response
	case "response.output_audio.delta", "response.audio.delta":
		if !server {
			ledger.Violations = append(ledger.Violations, "audio output had the wrong direction")
			return
		}
		response := liveS2SResponseForRecord(ledger, payload, sequence)
		if response == nil {
			return
		}
		encoded := liveS2SJSONField(payload, "delta")
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(decoded) == 0 {
			ledger.Violations = append(ledger.Violations, fmt.Sprintf("R%d output audio was not non-empty base64 audio", response.Ordinal))
			return
		}
		response.AudioDeltas++
		if response.FirstAudio == 0 {
			response.FirstAudio = sequence
		}
		if response.Cancel != 0 && sequence > response.Cancel {
			response.PostCancelAudio++
		}
		if response.Done != 0 && sequence > response.Done {
			response.PostTerminalAudio++
		}
	case "response.output_text.delta", "response.text.delta", "response.output_audio_transcript.delta", "response.audio_transcript.delta":
		if !server {
			ledger.Violations = append(ledger.Violations, "text output had the wrong direction")
			return
		}
		response := liveS2SResponseForRecord(ledger, payload, sequence)
		if response == nil || liveS2SJSONField(payload, "delta") == "" {
			return
		}
		response.TextDeltas++
		if response.Cancel != 0 && sequence > response.Cancel {
			response.PostCancelText++
		}
		if response.Done != 0 && sequence > response.Done {
			response.PostTerminalText++
		}
	case "response.output_audio_transcript.done", "response.audio_transcript.done":
		if !server {
			return
		}
		response := liveS2SResponseForRecord(ledger, payload, sequence)
		if response == nil || liveS2SJSONField(payload, "transcript") == "" {
			return
		}
		response.TextDeltas++
		if response.Cancel != 0 && sequence > response.Cancel {
			response.PostCancelText++
		}
		if response.Done != 0 && sequence > response.Done {
			response.PostTerminalText++
		}
	case "response.cancel":
		if !client {
			ledger.Violations = append(ledger.Violations, "response cancel had the wrong direction")
			return
		}
		ledger.CancelSequences = append(ledger.CancelSequences, sequence)
	case "response.done":
		if !server {
			ledger.Violations = append(ledger.Violations, "response terminal event had the wrong direction")
			return
		}
		response := liveS2SResponseForRecord(ledger, payload, sequence)
		if response == nil {
			return
		}
		if response.Done != 0 {
			ledger.Violations = append(ledger.Violations, fmt.Sprintf("R%d received duplicate terminal event", response.Ordinal))
			return
		}
		response.Done = sequence
		response.Status = liveS2SJSONField(payload, "response.status", "status")
		response.StatusReason = liveS2SJSONField(payload, "response.status_details.reason", "status_details.reason")
		if response.Status == "" {
			ledger.Violations = append(ledger.Violations, fmt.Sprintf("R%d terminal event had no status", response.Ordinal))
		}
	case "session.closed":
		if server {
			ledger.SessionClosed++
		}
	case "error":
		if server {
			ledger.ProviderErrors = append(ledger.ProviderErrors, liveS2SSafeErrorCode(
				liveS2SJSONField(payload, "error.type"),
				liveS2SJSONField(payload, "error.code"),
			))
		}
	}
}

func liveS2SAssignUserItem(ledger *liveS2SBargeInLedger, sequence int, redactedID string) {
	for _, turn := range ledger.Turns {
		if turn.UserItem == 0 {
			turn.UserItem = sequence
			turn.UserItemID = redactedID
			return
		}
	}
}

func liveS2SCurrentTurn(ledger *liveS2SBargeInLedger) *liveS2SBargeInTurn {
	if len(ledger.Turns) == 0 || ledger.Turns[len(ledger.Turns)-1].Commit != 0 {
		ledger.Turns = append(ledger.Turns, &liveS2SBargeInTurn{Ordinal: len(ledger.Turns) + 1})
	}
	return ledger.Turns[len(ledger.Turns)-1]
}

func liveS2SResponseForRecord(ledger *liveS2SBargeInLedger, payload []byte, sequence int) *liveS2SBargeInResponse {
	id := liveS2SJSONField(payload, "response_id", "response.id")
	if id == "" {
		ledger.Violations = append(ledger.Violations, fmt.Sprintf("response output at sequence %d had no response identity", sequence))
		return nil
	}
	response := ledger.ByResponseID[id]
	if response == nil {
		ledger.Violations = append(ledger.Violations, fmt.Sprintf("response output at sequence %d referenced an unknown identity", sequence))
	}
	return response
}

func liveS2SAssignCancelIdentities(ledger *liveS2SBargeInLedger) {
	for index, sequence := range ledger.CancelSequences {
		if index >= len(ledger.Responses) {
			ledger.Violations = append(ledger.Violations, "response cancel had no attributable response")
			continue
		}
		ledger.Responses[index].Cancel = sequence
	}
}

func liveS2SResponseTurn(ledger *liveS2SBargeInLedger) *liveS2SBargeInTurn {
	for index := len(ledger.Turns) - 1; index >= 0; index-- {
		turn := ledger.Turns[index]
		if turn.Commit != 0 && turn.ResponseCreate == 0 {
			return turn
		}
	}
	turn := liveS2SCurrentTurn(ledger)
	ledger.Violations = append(ledger.Violations, fmt.Sprintf("T%d response request had no preceding committed input", turn.Ordinal))
	return turn
}

func liveS2SResponseCancelled(response *liveS2SBargeInResponse) bool {
	if response == nil {
		return false
	}
	if strings.EqualFold(response.Status, "cancelled") || strings.EqualFold(response.Status, "canceled") {
		return true
	}
	return strings.EqualFold(response.Status, "incomplete") &&
		(strings.Contains(strings.ToLower(response.StatusReason), "cancel") || strings.EqualFold(response.StatusReason, "turn_detected"))
}

func liveS2SSafeStatus(status, reason string) string {
	if status == "" {
		return "unknown"
	}
	if reason == "" {
		return status
	}
	return status + "/" + reason
}

func liveS2SSafeErrorCode(errorType, code string) string {
	return liveS2SSafeToken(errorType) + "/" + liveS2SSafeToken(code)
}

func liveS2SSafeToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "unknown"
	}
	if len(value) > 64 {
		return "redacted"
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' && char != '-' && char != '.' {
			return "redacted"
		}
	}
	return value
}

func liveS2SPayload(record gwtesting.CapturedSessionEvent) []byte {
	if len(record.Payload) > 0 {
		return record.Payload
	}
	return record.Data
}

func liveS2SJSONField(payload []byte, paths ...string) string {
	var value any
	if json.Unmarshal(payload, &value) != nil {
		return ""
	}
	for _, path := range paths {
		current := value
		for _, part := range strings.Split(path, ".") {
			object, ok := current.(map[string]any)
			if !ok {
				current = nil
				break
			}
			current = object[part]
		}
		if text, ok := current.(string); ok && text != "" {
			return text
		}
	}
	return ""
}

type liveS2SBargeInInconclusiveError struct{ Reason string }

func (e *liveS2SBargeInInconclusiveError) Error() string {
	if e == nil {
		return "live barge-in observation was inconclusive"
	}
	return "live barge-in observation was inconclusive: " + e.Reason
}

func liveS2SBargeInRunErrorClass(err error) string {
	if err == nil {
		return ""
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "api key"), strings.Contains(text, "authentication"), strings.Contains(text, "unauthorized"), strings.Contains(text, "invalid_api_key"), strings.Contains(text, "401"), strings.Contains(text, "403"):
		return "authentication/setup"
	case strings.Contains(text, "rate limit"), strings.Contains(text, "rate_limit"), strings.Contains(text, "quota"), strings.Contains(text, "429"):
		return "rate-limited/provider-unavailable"
	case strings.Contains(text, "deadline"), strings.Contains(text, "timeout"), strings.Contains(text, "timed out"), strings.Contains(text, "context canceled"):
		return "timeout"
	case strings.Contains(text, "connection"), strings.Contains(text, "websocket"), strings.Contains(text, "network"), strings.Contains(text, "no such host"), strings.Contains(text, "unavailable"):
		return "provider-unavailable"
	default:
		return "runtime-contract-failure"
	}
}

func validateLiveS2SBargeInRecordDir(path string) error {
	for _, name := range []string{"manifest.json", "client.transcript.jsonl", "agent.transcript.jsonl", "session-log.jsonl"} {
		info, err := os.Stat(filepath.Join(path, name))
		if err != nil || info.Size() == 0 {
			return fmt.Errorf("recording diagnostic artifact %q is missing or empty", name)
		}
	}
	file, err := os.Open(filepath.Join(path, "session-log.jsonl"))
	if err != nil {
		return fmt.Errorf("open session diagnostic log: %w", err)
	}
	defer file.Close()
	entries := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var entry struct {
			TurnIndex int `json:"turn_index"`
			Input     struct {
				AudioBytes uint64 `json:"audio_bytes"`
				Committed  bool   `json:"committed"`
			} `json:"input"`
			Response struct {
				Complete   bool   `json:"complete"`
				AudioBytes uint64 `json:"audio_bytes"`
			} `json:"response"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return fmt.Errorf("decode session diagnostic log entry: %w", err)
		}
		entries++
		if entry.TurnIndex != entries || !entry.Input.Committed || entry.Input.AudioBytes == 0 || !entry.Response.Complete {
			return fmt.Errorf("session diagnostic log entry %d did not reconcile input and terminal response state", entries)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read session diagnostic log: %w", err)
	}
	if entries != liveS2SBargeInTurns {
		return fmt.Errorf("session diagnostic log entries=%d, want %d", entries, liveS2SBargeInTurns)
	}
	return nil
}

func newLiveS2SBargeInValidatorTrace() *liveS2SBargeInTrace {
	trace := newLiveS2SBargeInTrace()
	trace.markInputStart(1)
	trace.observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart})
	trace.observe(messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{1, 2})})
	trace.markInputStart(2)
	trace.observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd})
	trace.observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart})
	trace.markInputStart(3)
	trace.observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd})
	trace.observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart})
	trace.observe(messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{3, 4})})
	trace.observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd})
	trace.markInputStart(4)
	trace.observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart})
	trace.observe(messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{5, 6})})
	trace.observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd})
	return trace
}

func TestLiveSessionS2SLiveShapedBargeInV2ValidatorRejectsMissingUserItem(t *testing.T) {
	capture, _ := loadS2SLiveShapedBargeInV2ReplayCapture(t)
	trace := newLiveS2SBargeInValidatorTrace()

	removed := false
	filtered := make([]gwtesting.CapturedSessionEvent, 0, len(capture.Records))
	for _, record := range capture.Records {
		if !removed && record.Type == "conversation.item.created" && liveS2SJSONField(liveS2SPayload(record), "item.role") == "user" {
			removed = true
			continue
		}
		filtered = append(filtered, record)
	}
	if !removed {
		t.Fatal("missing-user-item negative control did not find a user conversation item")
	}
	capture.Records = filtered

	ledger, validationErr := validateLiveS2SBargeInCapture(capture, trace)
	if validationErr == nil || !strings.Contains(validationErr.Error(), "user item") {
		t.Fatalf("missing-user-item validation = %v, want a user-item contract failure; evidence=%s", validationErr, ledger.evidence())
	}
}

func TestLiveSessionS2SLiveShapedBargeInV2(t *testing.T) {
	if os.Getenv(liveS2SBargeInOptInEnv) != "1" {
		t.Skipf("%s!=1; live OpenAI Realtime barge-in confirmation is explicit opt-in", liveS2SBargeInOptInEnv)
	}
	apiKey := os.Getenv(liveS2SBargeInAPIKeyEnv)
	if apiKey == "" {
		t.Skipf("%s is not set; live OpenAI Realtime barge-in confirmation is inconclusive", liveS2SBargeInAPIKeyEnv)
	}

	trace := newLiveS2SBargeInTrace()
	runtimeObserver := &liveS2SBargeInRuntimeObserver{}
	agentCLI, err := wire.InitializeMockAgentCLIWithPorts(
		wire.NewPortSwap(wire.PortSessionRuntimeObserver, runtimeObserver),
	)
	if err != nil {
		t.Fatalf("initialize shipped CLI for live barge-in probe: %v", err)
	}
	agentCLI.SetSessionStreamObserver(trace.observe)
	workDir := t.TempDir()
	capturePath := filepath.Join(workDir, "live-s2s-barge-in.json")
	recordDir := filepath.Join(workDir, "recording")
	audioOutPath := filepath.Join(workDir, "assistant.wav")
	root := agentCLI.Generate()
	root.SetIn(newLiveS2SBargeInAudioReader(t, trace))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{
		"--config-dir", filepath.Join(workDir, "config"),
		"session",
		"--record", capturePath,
		"--record-dir", recordDir,
		"--provider", "openai",
		"--model", liveS2SBargeInModel,
		"--api-key", apiKey,
		"--system-prompt", "Answer every spoken request with a concise but clearly audible response of several short sentences.",
		"--audio-in", "-",
		"--audio-out", audioOutPath,
		"--max-duration", liveS2SBargeInTimeout.String(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), liveS2SBargeInTimeout+15*time.Second)
	defer cancel()
	runErr := root.ExecuteContext(ctx)
	capture, loadErr := gwtesting.LoadSessionCapture(capturePath)
	if loadErr != nil {
		if class := liveS2SBargeInRunErrorClass(runErr); class != "runtime-contract-failure" && class != "" {
			t.Skipf("INCONCLUSIVE live barge-in probe: %s", class)
		}
		t.Fatalf("live barge-in capture was not written; run class=%s", liveS2SBargeInRunErrorClass(runErr))
	}
	if class := liveS2SBargeInRunErrorClass(runErr); class != "" {
		if class != "runtime-contract-failure" {
			t.Skipf("INCONCLUSIVE live barge-in probe: %s", class)
		}
		ledger, _ := validateLiveS2SBargeInCapture(capture, trace)
		t.Fatalf("live barge-in probe returned a contract failure; evidence=%s; ledger=%s; trace=%s", captureSummary(capture), ledger.evidence(), trace.evidence())
	}
	ledger, validationErr := validateLiveS2SBargeInCapture(capture, trace)
	if validationErr != nil {
		var inconclusive *liveS2SBargeInInconclusiveError
		if errors.As(validationErr, &inconclusive) {
			t.Skipf("INCONCLUSIVE live barge-in probe: %s; evidence=%s; trace=%s", inconclusive.Reason, ledger.evidence(), trace.evidence())
		}
		t.Fatalf("live barge-in matrix failed: %v; evidence=%s; trace=%s", validationErr, ledger.evidence(), trace.evidence())
	}
	if err := validateLiveS2SBargeInRecordDir(recordDir); err != nil {
		t.Fatalf("live barge-in recording diagnostics failed: %v; evidence=%s", err, ledger.evidence())
	}
	if info, err := os.Stat(audioOutPath); err != nil || info.Size() <= 44 {
		t.Fatalf("live barge-in assistant audio artifact is missing or empty: %v", err)
	}
	runtimeFacts := runtimeObserver.snapshot()
	terminalCount := 0
	inputCommitCount := 0
	turnCount := 0
	outputBytes := 0
	for _, fact := range runtimeFacts {
		switch fact.Kind {
		case services.SessionRuntimeObservationInputCommit:
			inputCommitCount++
			if fact.PayloadBytes == 0 {
				t.Fatalf("live runtime diagnostic input commit %d carried no PCM", fact.InputCommit)
			}
		case services.SessionRuntimeObservationTurnCompleted:
			turnCount++
		case services.SessionRuntimeObservationAudioOutput:
			outputBytes += fact.PayloadBytes
		case services.SessionRuntimeObservationTerminal:
			terminalCount++
			if !fact.Clean || fact.HasError || !fact.HasFinalAccounting {
				t.Fatalf("live runtime diagnostic terminal state was not clean and accounted: %s", liveS2SBargeInRuntimeEvidence(runtimeFacts))
			}
		}
	}
	if inputCommitCount != liveS2SBargeInTurns || turnCount != liveS2SBargeInTurns || terminalCount != 1 || outputBytes == 0 {
		t.Fatalf("live runtime diagnostics did not reconcile input, turns, output, and terminal state: %s", liveS2SBargeInRuntimeEvidence(runtimeFacts))
	}
	t.Logf("live barge-in proof: %s; trace=%s; runtime=%s", ledger.evidence(), trace.evidence(), liveS2SBargeInRuntimeEvidence(runtimeFacts))
}

func captureSummary(capture gwtesting.SessionCapture) string {
	counts := make(map[string]int)
	for _, record := range capture.Records {
		counts[record.Type]++
	}
	return fmt.Sprintf("records=%d event_types=%v", len(capture.Records), counts)
}
