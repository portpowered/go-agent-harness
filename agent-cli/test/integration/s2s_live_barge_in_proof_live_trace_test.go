//go:build live

package integration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
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
