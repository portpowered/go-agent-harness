//go:build live

package integration

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// liveBargeInTrace is a gate over messages consumed by the shipped session
// command. It intentionally records only normalized event kinds and byte
// counts; raw provider identity and payload remain in the private capture.
type liveBargeInTrace struct {
	mu sync.Mutex

	responseOrdinal int
	responseOpen    bool
	sessionUpdated  bool
	sessionReady    chan struct{}
	readyOnce       sync.Once
	created         chan int
	audio           chan int
	done            chan int
	events          []liveBargeInStreamEvent
	inputStarts     map[int]int
}

type liveBargeInStreamEvent struct {
	Type            messages.StreamMessageType
	ResponseOrdinal int
	AudioBytes      int
	TextBytes       int
}

func newLiveBargeInTrace() *liveBargeInTrace {
	return &liveBargeInTrace{
		sessionReady: make(chan struct{}),
		created:      make(chan int, liveBargeInTurns),
		audio:        make(chan int, liveBargeInTurns),
		done:         make(chan int, liveBargeInTurns),
		inputStarts:  make(map[int]int, liveBargeInTurns),
	}
}

func (t *liveBargeInTrace) observe(msg messages.StreamMessage) {
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
	t.events = append(t.events, liveBargeInStreamEvent{
		Type:            msg.Type,
		ResponseOrdinal: ordinal,
		AudioBytes:      audioBytes,
		TextBytes:       textBytes,
	})
	t.mu.Unlock()

	if ready {
		t.readyOnce.Do(func() { close(t.sessionReady) })
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

func (t *liveBargeInTrace) waitFor(ctx context.Context, boundary string, signal <-chan int, minimum int) error {
	if ctx == nil {
		return fmt.Errorf("%s: nil context", boundary)
	}
	for {
		select {
		case ordinal := <-signal:
			if ordinal >= minimum {
				return nil
			}
		case <-ctx.Done():
			return probeWaitDiagnostic(ctx, boundary)
		}
	}
}

func (t *liveBargeInTrace) waitForSession(ctx context.Context) error {
	if ctx == nil {
		return errors.New("session.updated: nil context")
	}
	select {
	case <-t.sessionReady:
		return nil
	case <-ctx.Done():
		return probeWaitDiagnostic(ctx, "session.updated acknowledgement")
	}
}

func probeWaitDiagnostic(ctx context.Context, boundary string) error {
	if ctx == nil {
		return fmt.Errorf("%s: wait cancelled", boundary)
	}
	return fmt.Errorf("%s: %w", boundary, ctx.Err())
}

func (t *liveBargeInTrace) markInputStart(turn int) {
	t.mu.Lock()
	if _, exists := t.inputStarts[turn]; !exists {
		t.inputStarts[turn] = len(t.events)
	}
	t.mu.Unlock()
}

func (t *liveBargeInTrace) snapshot() ([]liveBargeInStreamEvent, map[int]int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	events := append([]liveBargeInStreamEvent(nil), t.events...)
	starts := make(map[int]int, len(t.inputStarts))
	for turn, index := range t.inputStarts {
		starts[turn] = index
	}
	return events, starts
}

func (t *liveBargeInTrace) evidence() string {
	events, starts := t.snapshot()
	parts := make([]string, 0, len(events))
	for _, event := range events {
		parts = append(parts, fmt.Sprintf("%s:r%d:a%d:t%d", event.Type, event.ResponseOrdinal, event.AudioBytes, event.TextBytes))
	}
	return fmt.Sprintf("input_starts=%v stream=[%s]", starts, strings.Join(parts, ","))
}

// liveBargeInAudioReader feeds four non-empty fixture utterances through the
// shipped --audio-in - path. The production audio source paces every frame;
// this reader also keeps the fixture transitions at the documented frame
// cadence so the collision is governed by observed provider boundaries, not a
// wall-clock sleep.
type liveBargeInAudioReader struct {
	mu       sync.Mutex
	trace    *liveBargeInTrace
	segments []liveBargeInAudioSegment
	segment  int
	frame    int
	gateUsed bool
	marker   bool
	paceBase time.Time
}

type liveBargeInAudioSegment struct {
	turn      int
	frames    [][]byte
	gate      func(context.Context) error
	endOfTurn bool
}

func newLiveBargeInAudioReader(t *testing.T, trace *liveBargeInTrace) *liveBargeInAudioReader {
	t.Helper()
	frameSets := make([][][]byte, 0, liveBargeInTurns)
	for _, name := range multiturnTurnWAVs {
		frameSets = append(frameSets, multiturnAudioFrames(t, locateCLIFixture(t, name)))
	}
	return &liveBargeInAudioReader{
		trace: trace,
		segments: []liveBargeInAudioSegment{
			{turn: 1, frames: frameSets[0], gate: trace.waitForSession, endOfTurn: true},
			{turn: 2, frames: frameSets[1], gate: func(ctx context.Context) error {
				return trace.waitFor(ctx, "active assistant audio for response 1", trace.audio, 1)
			}, endOfTurn: true},
			{turn: 3, frames: frameSets[2], gate: func(ctx context.Context) error {
				return trace.waitFor(ctx, "response 2 creation before first output", trace.created, 2)
			}, endOfTurn: true},
			{turn: 4, frames: frameSets[3], gate: func(ctx context.Context) error {
				return trace.waitFor(ctx, "completed response 3 before continuation", trace.done, 3)
			}},
		},
	}
}

func (r *liveBargeInAudioReader) Read(p []byte) (int, error) {
	return r.ReadContext(context.Background(), p)
}

func (r *liveBargeInAudioReader) ReadContext(ctx context.Context, p []byte) (int, error) {
	want := audio.FrameSize * 2
	if len(p) != want {
		return 0, fmt.Errorf("live barge-in reader received %d bytes, want %d", len(p), want)
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
				if err := waitLiveBargeInFrame(ctx, paceBase.Add(time.Duration(frameIndex)*liveBargeInFrameWait)); err != nil {
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

func waitLiveBargeInFrame(ctx context.Context, target time.Time) error {
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

func (*liveBargeInAudioReader) Close() error { return nil }

// observeClient normalizes one client-to-server capture record.
func (a *liveBargeInCaptureAdapter) observeClient(record gwtesting.CapturedSessionEvent, payload []byte) {
	switch record.Type {
	case rtEventInputAudioAppend:
		a.observeInputAppend(record, payload)
	case rtEventInputAudioCommit:
		a.observeInputCommit()
	case rtEventResponseCancel:
		a.observeResponseCancel(record)
	}
}

// observeServer normalizes one server-to-client capture record.
func (a *liveBargeInCaptureAdapter) observeServer(record gwtesting.CapturedSessionEvent, payload []byte) {
	switch record.Type {
	case rtEventSessionCreated:
		a.facts.SessionCreated++
	case "session.updated":
		a.facts.SessionUpdated++
	case rtEventSessionClosed:
		a.facts.SessionClosed++
	case rtEventError:
		a.facts.ProviderErrors++
		code := liveBargeInSafeToken(liveBargeInJSONField(payload, "error.code", "error.type", "code", "type"))
		a.facts.ProviderCodes = append(a.facts.ProviderCodes, code)
	case rtEventConversationItemCreated:
		if liveBargeInJSONField(payload, "item.role") == rtRoleUser {
			a.observeUserTurn(liveBargeInJSONField(payload, "item.id"))
		}
	case "input_audio_buffer.committed", "conversation.item.input_audio_transcription.completed":
		// Realtime exposes the committed user item on the commit
		// acknowledgement and, in current sessions, on the transcription
		// completion; older captures expose it through
		// conversation.item.created above. All are one logical user-turn
		// representation and are deduplicated by item ID.
		a.observeUserTurn(liveBargeInJSONField(payload, "item_id", "item.id"))
	case rtEventResponseCreated:
		a.observeResponseCreated(record, payload)
	case rtEventOutputAudioDelta, rtEventLegacyAudioDelta:
		a.observeAudioOutput(record, payload)
	case rtEventOutputTextDelta, "response.text.delta", rtEventOutputAudioTranscriptDelta, "response.audio_transcript.delta", rtEventOutputAudioTranscriptDone, "response.audio_transcript.done":
		a.observeTextOutput(record, payload)
	case rtEventResponseDone:
		a.observeResponseDone(record, payload)
	}
}

func (a *liveBargeInCaptureAdapter) observeInputAppend(record gwtesting.CapturedSessionEvent, payload []byte) {
	if a.currentInput == "" {
		a.inputOrdinal++
		a.currentInput = fmt.Sprintf("input-%d", a.inputOrdinal)
		a.facts.InputStarts = append(a.facts.InputStarts, record.Sequence)
	}
	decoded, err := base64.StdEncoding.DecodeString(liveBargeInJSONField(payload, "audio"))
	if err != nil || len(decoded) == 0 {
		a.issues = append(a.issues, fmt.Sprintf("input append %d was not non-empty base64 audio", record.Sequence))
	}
	a.facts.Appends++
	a.ledger.Observe(probe.BargeInEvent{
		Sequence:      a.nextEventSequence(),
		Kind:          probe.BargeInEventInputAppend,
		InputID:       a.currentInput,
		TurnID:        liveBargeInTurnID(a.currentInput),
		AppendGroupID: a.currentInput,
		Bytes:         len(decoded),
		NonEmpty:      len(decoded) > 0,
	})
}

func (a *liveBargeInCaptureAdapter) observeInputCommit() {
	a.facts.Commits++
	a.ledger.Observe(probe.BargeInEvent{
		Sequence: a.nextEventSequence(),
		Kind:     probe.BargeInEventInputCommit,
		InputID:  a.currentInput,
		TurnID:   liveBargeInTurnID(a.currentInput),
	})
	a.lastCommittedInput = a.currentInput
	if a.currentInput != "" {
		a.committedInputs = append(a.committedInputs, a.currentInput)
	}
	a.currentInput = ""
}

// observeResponseCreated assigns the next stable response identity and
// appends its wire-response record.
func (a *liveBargeInCaptureAdapter) observeResponseCreated(record gwtesting.CapturedSessionEvent, payload []byte) {
	a.responseOrdinal++
	providerID := liveBargeInJSONField(payload, "response.id", "response_id")
	if providerID == "" {
		a.issues = append(a.issues, "response.created had no provider identity")
	}
	stableID := fmt.Sprintf("response-%d", a.responseOrdinal)
	identity := liveBargeInResponseIdentity{
		stable:     stableID,
		providerID: providerID,
		inputID:    a.lastCommittedInput,
		turnID:     liveBargeInTurnID(a.lastCommittedInput),
		ordinal:    a.responseOrdinal,
	}
	if _, exists := a.providerResponses[providerID]; exists {
		a.issues = append(a.issues, "response provider identity was reused")
	}
	a.providerResponses[providerID] = identity
	a.responseByProvider[providerID] = stableID
	a.facts.Responses = append(a.facts.Responses, liveBargeInWireResponse{Created: record.Sequence})
	a.ledger.Observe(probe.BargeInEvent{
		Sequence:   a.nextEventSequence(),
		Kind:       probe.BargeInEventResponseCreated,
		InputID:    identity.inputID,
		TurnID:     identity.turnID,
		ResponseID: stableID,
	})
	if a.responseOrdinal > 1 {
		a.ledger.Observe(probe.BargeInEvent{
			Sequence:   a.nextEventSequence(),
			Kind:       probe.BargeInEventContinuation,
			InputID:    identity.inputID,
			TurnID:     identity.turnID,
			ResponseID: stableID,
		})
	}
}

func (a *liveBargeInCaptureAdapter) observeAudioOutput(record gwtesting.CapturedSessionEvent, payload []byte) {
	providerID := liveBargeInJSONField(payload, "response_id", "response.id")
	stableID := a.responseByProvider[providerID]
	if a.providerOutputWasDiscarded(providerID, record.Sequence) {
		return
	}
	decoded, err := base64.StdEncoding.DecodeString(liveBargeInJSONField(payload, "delta"))
	if err != nil || len(decoded) == 0 {
		a.issues = append(a.issues, "response audio output was not non-empty base64 audio")
	}
	if response := a.wireResponse(providerID); response != nil {
		if len(decoded) > 0 && response.FirstAudio == 0 {
			response.FirstAudio = record.Sequence
		}
		response.AudioBytes += len(decoded)
	}
	if stableID == "" {
		a.issues = append(a.issues, "response audio output referenced unknown provider identity")
	}
	a.ledger.Observe(probe.BargeInEvent{
		Sequence:   a.nextEventSequence(),
		Kind:       probe.BargeInEventResponseOutput,
		ResponseID: stableID,
		Bytes:      len(decoded),
		NonEmpty:   len(decoded) > 0,
	})
}

func (a *liveBargeInCaptureAdapter) observeTextOutput(record gwtesting.CapturedSessionEvent, payload []byte) {
	providerID := liveBargeInJSONField(payload, "response_id", "response.id")
	stableID := a.responseByProvider[providerID]
	if a.providerOutputWasDiscarded(providerID, record.Sequence) {
		return
	}
	text := liveBargeInJSONField(payload, "delta", "transcript")
	if response := a.wireResponse(providerID); response != nil && text != "" && response.FirstText == 0 {
		response.FirstText = record.Sequence
	}
	if stableID == "" {
		a.issues = append(a.issues, "response text output referenced unknown provider identity")
	}
	a.ledger.Observe(probe.BargeInEvent{
		Sequence:   a.nextEventSequence(),
		Kind:       probe.BargeInEventResponseOutput,
		ResponseID: stableID,
		Bytes:      len(text),
		NonEmpty:   text != "",
	})
}

func (a *liveBargeInCaptureAdapter) observeResponseCancel(record gwtesting.CapturedSessionEvent) {
	identity := a.activeResponse()
	interruptingInput := a.currentInput
	if interruptingInput == "" {
		interruptingInput = fmt.Sprintf("input-%d", a.inputOrdinal+1)
	}
	if identity.ordinal == 0 {
		a.issues = append(a.issues, "response.cancel had no active response")
	} else {
		identity.cancelSeq = record.Sequence
		a.providerResponses[identity.providerID] = identity
		if response := a.wireResponse(identity.providerID); response != nil {
			response.Cancel = record.Sequence
		}
		a.facts.Cancels++
	}
	a.ledger.Observe(probe.BargeInEvent{
		Sequence:   a.nextEventSequence(),
		Kind:       probe.BargeInEventResponseCancel,
		InputID:    interruptingInput,
		TurnID:     liveBargeInTurnID(interruptingInput),
		ResponseID: identity.stable,
	})
}

func (a *liveBargeInCaptureAdapter) observeResponseDone(record gwtesting.CapturedSessionEvent, payload []byte) {
	providerID := liveBargeInJSONField(payload, "response.id", "response_id")
	identity, exists := a.providerResponses[providerID]
	if !exists {
		a.issues = append(a.issues, "response.done referenced unknown provider identity")
	}
	status := liveBargeInJSONField(payload, "response.status", "status")
	reason := liveBargeInJSONField(payload, "response.status_details.reason", "status_details.reason")
	if status == "" {
		a.issues = append(a.issues, "response.done had no terminal status")
	}
	a.ledger.Observe(probe.BargeInEvent{
		Sequence:    a.nextEventSequence(),
		Kind:        probe.BargeInEventResponseTerminal,
		ResponseID:  identity.stable,
		Disposition: liveBargeInDisposition(status, reason, identity.cancelSeq > 0),
		Reason:      liveBargeInSafeToken(reason),
	})
	if response := a.wireResponse(providerID); response != nil {
		response.Done = record.Sequence
		response.Terminal = true
	}
	identity.terminal = true
	a.providerResponses[providerID] = identity
}
