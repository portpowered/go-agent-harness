package codexlive

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexrtc"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/internal/livesession"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/quicksilver"
)

func sendAudio(t *testing.T, h *harness, samples []int16) {
	t.Helper()
	outcome := h.session.SendWithOutcome(t.Context(), messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue(codec.EncodePCM16(samples))})
	if !outcome.OK() {
		t.Fatalf("send audio: %+v", outcome)
	}
}

// One second of 24 kHz input becomes fifty 48 kHz frames that leave one per
// 20 ms of virtual time: the first at once, none early, none late.
func TestInputIsResampledFramedAndPacedAt20ms(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		start := time.Now()
		sendAudio(t, h, make([]int16, 24000))
		wait(2 * time.Second)
		frames := h.peer.frames()
		if len(frames) < 49 || len(frames) > 50 {
			t.Fatalf("frames = %d, want 50 (the resampler may hold back its last samples)", len(frames))
		}
		for i, frame := range frames {
			if len(frame.samples) != codexrtc.FrameSamples {
				t.Fatalf("frame %d has %d samples", i, len(frame.samples))
			}
			if want := start.Add(time.Duration(i) * codexrtc.FrameDuration); !frame.at.Equal(want) {
				t.Fatalf("frame %d left at +%v, want +%v", i, frame.at.Sub(start), want.Sub(start))
			}
		}
		if err := h.session.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

// Silent peer frames never open a segment. An audible frame opens one;
// silence after it joins the segment without extending it, so the segment
// closes a quiet gap after the last audible audio.
func TestSilenceJoinsButNeverOpensOrExtendsASegment(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.peer.inbound <- make([]int16, codexrtc.FrameSamples)
		synctest.Wait()
		if h.pending() {
			t.Fatal("a silent frame produced a message")
		}
		opened := time.Now()
		loud := make([]int16, codexrtc.FrameSamples)
		for i := range loud {
			loud[i] = 9000
		}
		h.peer.inbound <- loud
		h.expect(t, messages.StreamTypeMessageStart)
		h.expect(t, messages.StreamTypeAudioStart)
		delta := h.expect(t, messages.StreamTypeAudioDelta)
		if n := len(valueOf[*messages.AudioDeltaValue](t, delta).Content); n < 900 || n > 960 {
			t.Fatalf("delta = %d bytes, want about 20 ms of 24 kHz PCM16", n)
		}
		for range 10 {
			h.peer.inbound <- make([]int16, codexrtc.FrameSamples)
			if msg := h.expect(t, messages.StreamTypeAudioDelta); msg.ResponseID != "live_seg_1" {
				t.Fatalf("silence in %q", msg.ResponseID)
			}
		}
		h.expect(t, messages.StreamTypeAudioEnd)
		end := h.expect(t, messages.StreamTypeMessageEnd)
		if closedAfter := time.Since(opened); closedAfter > DefaultSegmentGap+100*time.Millisecond {
			t.Fatalf("segment closed %v after its only audible frame; silence extended it", closedAfter)
		}
		if valueOf[*messages.MessageEndValue](t, end).Status != "completed" {
			t.Fatal("segment did not end completed")
		}
		if err := h.session.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

// Non-credential errors are non-terminal diagnostics; session.updated is
// reported; delegations, session.started, the sideband's audio copies and
// unknown turns map to nothing; a final user transcript with no open
// utterance is a whole utterance.
func TestDialectMapsTheRestOfTheVocabulary(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.side.events <- quicksilver.ErrorEvent{Error: &quicksilver.ErrorDetail{Code: "missing_scope", Message: "temporary provider rejection"}}
		failure := valueOf[*messages.ErrorValue](t, h.expect(t, messages.StreamTypeError))
		if !failure.IsNonTerminal() || failure.Code != "missing_scope" || failure.Message != "temporary provider rejection" {
			t.Fatalf("error = %+v, want a non-terminal diagnostic", failure)
		}
		for _, ignored := range []quicksilver.Event{
			quicksilver.DelegationCreated{Item: quicksilver.DelegationItem{ID: "del_1", Type: quicksilver.ItemTypeDelegation, Target: quicksilver.TargetClient}},
			quicksilver.SessionStarted{},
			quicksilver.OutputAudioDelta{Audio: "AAE="},
			quicksilver.TurnDone{Turn: quicksilver.Turn{Role: "system"}},
			quicksilver.TurnDone{Turn: quicksilver.Turn{Role: quicksilver.RoleUser}},
			quicksilver.UnknownEvent{Type: "rate_limits.updated", Raw: []byte(`{"type":"rate_limits.updated"}`)},
		} {
			h.side.events <- ignored
		}
		h.side.events <- quicksilver.SessionUpdated{Session: &quicksilver.SessionResource{ID: "sess_x"}}
		if updated := h.expect(t, messages.StreamTypeSessionUpdated); valueOf[*messages.SessionUpdatedValue](t, updated).SessionID != "sess_x" {
			t.Fatalf("SESSION.UPDATED = %+v", updated.Value)
		}
		h.side.events <- quicksilver.SessionUpdated{}
		h.expect(t, messages.StreamTypeSessionUpdated)
		h.side.events <- quicksilver.TurnDone{Turn: quicksilver.Turn{Role: quicksilver.RoleUser, Transcript: "hi"}}
		h.expect(t, messages.StreamTypeInputItemAdded)
		h.expect(t, messages.StreamTypeTranscriptDelta)
		if end := h.expect(t, messages.StreamTypeTranscriptEnd); valueOf[*messages.TranscriptEndValue](t, end).FullText != "hi" {
			t.Fatal("the final user transcript was not the utterance")
		}
		h.side.events <- fmt.Errorf("%w: bad frame", quicksilver.ErrMalformedEvent)
		h.side.events <- quicksilver.SessionUpdated{}
		h.expect(t, messages.StreamTypeSessionUpdated)
		if err := h.session.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

// A lost sideband is redialed after the Codex backoff, then with the
// OpenClaw attempt spacing; control events written while it is down are
// held and sent on the new sideband in order; a second rapid loss waits
// twice as long.
func TestALostSidebandRedialsWithBackoffAndFlushesHeldEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		second, third := newFakeControl(), newFakeControl()
		dial := &dialer{results: []any{errors.New("refused"), errors.New("refused"), second, third}}
		h := newHarness(t, dial)
		lost := time.Now()
		h.side.events <- errors.New("connection reset")
		synctest.Wait()
		appendFrame := []byte(`{"type":"session.context.append","channel":"speakable","content":[{"type":"input_text","text":"held"}]}`)
		if err := h.conn.WriteMessage(1, appendFrame); err != nil {
			t.Fatal(err)
		}
		wait(time.Second)
		dials := dial.dialTimes()
		var offsets []time.Duration
		for _, at := range dials {
			offsets = append(offsets, at.Sub(lost))
		}
		if want := []time.Duration{200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond}; !slices.Equal(offsets, want) {
			t.Fatalf("dials at %v after the loss, want %v", offsets, want)
		}
		if sent := second.sentEvents(); len(sent) != 1 || sent[0].EventType() != quicksilver.TypeSessionContextAppend {
			t.Fatalf("held events on the new sideband = %v", sent)
		}
		second.events <- quicksilver.SessionUpdated{}
		h.expect(t, messages.StreamTypeSessionUpdated)

		lostAgain := time.Now()
		second.events <- errors.New("connection reset")
		wait(time.Second)
		if dials := dial.dialTimes(); len(dials) != 4 || dials[3].Sub(lostAgain) != 400*time.Millisecond {
			t.Fatalf("second redial at %v after the loss, want 400ms", dials[len(dials)-1].Sub(lostAgain))
		}
		if err := h.session.Close(); err != nil {
			t.Fatal(err)
		}
		if sent := third.sentEvents(); len(sent) != 1 || sent[0].EventType() != quicksilver.TypeSessionClose {
			t.Fatalf("close on the third sideband = %v", sent)
		}
	})
}

// A send that fails is held and sent again once the sideband is back.
func TestAFailedSendIsHeldForTheReconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		second := newFakeControl()
		h := newHarness(t, &dialer{results: []any{second}})
		h.side.sendErr = errors.New("broken pipe")
		if err := h.conn.WriteMessage(1, []byte(`{"type":"session.context.append","content":[{"type":"input_text","text":"x"}]}`)); err != nil {
			t.Fatal(err)
		}
		h.side.events <- errors.New("connection reset")
		wait(time.Second)
		if sent := second.sentEvents(); len(sent) != 1 {
			t.Fatalf("events on the new sideband = %v, want the failed one", sent)
		}
		if err := h.session.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

// Ends the transport reports: redials exhausted, a call that ended, a
// rejected credential, a failed media connection, a failed input write and
// a normal close nobody asked for.
func TestTransportEndsMapOntoTheSessionClose(t *testing.T) {
	refused := errors.New("refused")
	for name, tc := range map[string]struct {
		dials    []any
		trigger  func(t *testing.T, h *harness)
		reason   string
		terminal messages.TerminalReason
	}{
		"redials exhausted": {
			dials:   []any{refused, refused, refused, refused, refused},
			trigger: func(_ *testing.T, h *harness) { h.side.events <- errors.New("reset") }, reason: livesession.CloseReasonConnectionLost, terminal: messages.TerminalReasonTerminalFailure,
		},
		"call ended": {
			dials:   []any{&codexrtc.StatusError{Op: "sideband", StatusCode: 404}},
			trigger: func(_ *testing.T, h *harness) { h.side.events <- errors.New("reset") }, reason: reasonCallEnded, terminal: messages.TerminalReasonProviderClose,
		},
		"credential rejected": {
			dials:   []any{fmt.Errorf("%w: refresh failed", codexrtc.ErrNoCredential)},
			trigger: func(_ *testing.T, h *harness) { h.side.events <- errors.New("reset") }, reason: reasonAuthFailed, terminal: messages.TerminalReasonTerminalFailure,
		},
		"media failed": {
			trigger: func(_ *testing.T, h *harness) { close(h.peer.failed) }, reason: livesession.CloseReasonConnectionLost, terminal: messages.TerminalReasonTerminalFailure,
		},
		"input write failed": {
			trigger: func(t *testing.T, h *harness) {
				t.Helper()
				h.peer.mu.Lock()
				h.peer.fail = errors.New("track closed")
				h.peer.mu.Unlock()
				outcome := h.session.SendWithOutcome(t.Context(), messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue(make([]byte, 1920))})
				if !outcome.OK() {
					t.Errorf("send audio: %+v", outcome)
				}
			},
			reason: livesession.CloseReasonConnectionLost, terminal: messages.TerminalReasonTerminalFailure,
		},
		"remote hangup": {
			trigger: func(_ *testing.T, h *harness) { h.side.events <- codexrtc.ErrSidebandClosed }, reason: livesession.CloseReasonRemoteHangup, terminal: messages.TerminalReasonProviderClose,
		},
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHarness(t, &dialer{results: tc.dials})
				tc.trigger(t, h)
				var msg messages.StreamMessage
				for msg = h.next(t); msg.Type != messages.StreamTypeSessionClose; msg = h.next(t) {
				}
				closed := valueOf[*messages.SessionCloseValue](t, msg)
				if closed.Reason != tc.reason || closed.TerminalReason != tc.terminal {
					t.Fatalf("SESSION.CLOSE = %s/%s, want %s/%s", closed.Reason, closed.TerminalReason, tc.reason, tc.terminal)
				}
				if err := h.session.Close(); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

// The transport refuses undecodable writes and every write after Close.
func TestTransportWritesAfterCloseAndMalformedWritesFail(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		for _, frame := range []string{`not json`, `{"type":"input_audio.append","audio":"%%%"}`, `{"type":"input_audio.append","audio":"AAE"}`, `{"type":"input_audio.append","audio":"AAEC"}`, `{"type":""}`} {
			if err := h.conn.WriteMessage(1, []byte(frame)); err == nil {
				t.Errorf("WriteMessage(%s) succeeded", frame)
			}
		}
		if err := h.session.Close(); err != nil {
			t.Fatal(err)
		}
		if err := h.conn.WriteMessage(1, []byte(`{"type":"session.close"}`)); !errors.Is(err, errConnClosed) {
			t.Fatalf("write after close = %v", err)
		}
		if _, _, err := h.conn.ReadMessage(); !errors.Is(err, errConnClosed) {
			t.Fatalf("read after close = %v", err)
		}
	})
}

func TestReconnectPolicyDoublesCapsAndResetsAfterAStableConnection(t *testing.T) {
	policy := defaultReconnectPolicy()
	var delays []time.Duration
	for range 7 {
		delays = append(delays, policy.delayAfterLoss(time.Second))
	}
	want := []time.Duration{200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, 1600 * time.Millisecond, 3200 * time.Millisecond, 5 * time.Second, 5 * time.Second}
	if !slices.Equal(delays, want) {
		t.Fatalf("delays = %v, want %v", delays, want)
	}
	if got := policy.delayAfterLoss(DefaultStableConnection); got != DefaultReconnectBaseDelay {
		t.Fatalf("delay after a stable connection = %v, want the base delay", got)
	}
	if got := policy.retryDelay(4); got != 1600*time.Millisecond {
		t.Fatalf("delay before the fifth dial = %v", got)
	}
}

func TestSessionRateAcceptsOneSharedPCMRate(t *testing.T) {
	for _, tc := range []struct {
		cfg  models.SessionConfig
		want int
	}{
		{cfg: models.SessionConfig{}, want: 24000},
		{cfg: models.SessionConfig{InputAudioSampleRate: 16000}, want: 16000},
		{cfg: models.SessionConfig{OutputAudioSampleRate: 24000, InputAudioFormat: models.AudioFormatPCM16}, want: 24000},
	} {
		if got, err := sessionRate(tc.cfg); err != nil || got != tc.want {
			t.Errorf("sessionRate(%+v) = %d, %v; want %d", tc.cfg, got, err, tc.want)
		}
	}
	for _, cfg := range []models.SessionConfig{
		{InputAudioFormat: models.AudioFormatG711Ulaw},
		{InputAudioSampleRate: 16000, OutputAudioSampleRate: 24000},
		{InputAudioSampleRate: 8000},
	} {
		if _, err := sessionRate(cfg); !errors.Is(err, quicksilver.ErrInvalidSessionConfig) {
			t.Errorf("sessionRate(%+v) = %v, want a config error", cfg, err)
		}
	}
}

// A 16 kHz session upsamples by three: 320 input samples make one frame.
func TestInputFramerCarriesPartialFrames(t *testing.T) {
	framer, err := newInputFramer(16000)
	if err != nil {
		t.Fatal(err)
	}
	frames, err := framer.frames(make([]int16, 200))
	if err != nil || len(frames) != 0 {
		t.Fatalf("frames after 200 samples = %d, %v; want none yet", len(frames), err)
	}
	frames, err = framer.frames(make([]int16, 500))
	if err != nil || len(frames) != 2 {
		t.Fatalf("frames after 700 samples = %d, %v; want 2", len(frames), err)
	}
	if _, err := newInputFramer(11025); err == nil {
		t.Fatal("an unsupported rate was accepted")
	}
	if _, err := newOutputConverter(11025); err == nil {
		t.Fatal("an unsupported output rate was accepted")
	}
}

// The end of a user turn (MESSAGE.END) sends the held partial frame, padded
// with silence, instead of keeping it until the next turn; it asks the model
// for nothing.
func TestTheEndOfAUserTurnFlushesTheHeldFrame(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		loud := make([]int16, 300)
		for i := range loud {
			loud[i] = 9000
		}
		sendAudio(t, h, loud)
		wait(time.Second)
		if frames := h.peer.frames(); len(frames) != 0 {
			t.Fatalf("frames before the turn ended = %d, want the partial frame held", len(frames))
		}
		if outcome := h.session.SendWithOutcome(t.Context(), messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}); !outcome.OK() {
			t.Fatalf("MESSAGE.END: %+v", outcome)
		}
		wait(time.Second)
		frames := h.peer.frames()
		if len(frames) != 1 || frames[0].samples[0] != 9000 || frames[0].samples[codexrtc.FrameSamples-1] != 0 {
			t.Fatalf("frames after the turn ended = %d, want one padded frame", len(frames))
		}
		if sent := h.side.sentEvents(); len(sent) != 0 {
			t.Fatalf("the turn end reached the sideband: %v", sent)
		}
		if err := h.session.Close(); err != nil {
			t.Fatal(err)
		}
	})
}
