package codexlive

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai/chatgptauth"
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
			quicksilver.DelegationCreated{Item: quicksilver.DelegationItem{ID: "del_srv", Type: quicksilver.ItemTypeDelegation, Target: "server"}},
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
// transientCredential is a refresh that failed on the network: the token
// manager may well succeed on the next dial.
func transientCredential() error {
	return fmt.Errorf("%w: refresh: connection reset by peer", codexrtc.ErrNoCredential)
}

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
		"sign-in expired": {
			dials:   []any{fmt.Errorf("%w: %w", codexrtc.ErrNoCredential, chatgptauth.ErrReauthRequired)},
			trigger: func(_ *testing.T, h *harness) { h.side.events <- errors.New("reset") }, reason: reasonAuthFailed, terminal: messages.TerminalReasonTerminalFailure,
		},
		"signed out": {
			dials:   []any{fmt.Errorf("%w: %w", codexrtc.ErrNoCredential, chatgptauth.ErrNotLoggedIn)},
			trigger: func(_ *testing.T, h *harness) { h.side.events <- errors.New("reset") }, reason: reasonAuthFailed, terminal: messages.TerminalReasonTerminalFailure,
		},
		"refresh keeps failing": {
			dials:   []any{transientCredential(), transientCredential(), transientCredential(), transientCredential(), transientCredential()},
			trigger: func(_ *testing.T, h *harness) { h.side.events <- errors.New("reset") }, reason: livesession.CloseReasonConnectionLost, terminal: messages.TerminalReasonTerminalFailure,
		},
		"credential rejected": {
			dials:   []any{&codexrtc.StatusError{Op: "sideband", StatusCode: 401}},
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

// contextFrame is a session.context.append frame carrying text.
func contextFrame(text string) []byte {
	return []byte(`{"type":"session.context.append","content":[{"type":"input_text","text":"` + text + `"}]}`)
}

// sentTexts is the text of each context append side received, in order.
func sentTexts(t *testing.T, side *fakeControl) []string {
	t.Helper()
	var texts []string
	for _, event := range side.sentEvents() {
		appended, ok := event.(quicksilver.SessionContextAppend)
		if !ok {
			t.Fatalf("event %T, want session.context.append", event)
		}
		texts = append(texts, appended.Content[0].Text)
	}
	return texts
}

// Events held while the sideband is down keep their order when it returns:
// a write that arrives while the held events are being sent is held behind
// them instead of overtaking them (a split CONTEXT.APPEND depends on it).
func TestHeldEventsKeepTheirOrderAcrossAReconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		second := newFakeControl()
		second.gate = make(chan struct{})
		h := newHarness(t, &dialer{results: []any{second}})
		h.side.events <- errors.New("connection reset")
		synctest.Wait()
		for _, text := range []string{"A", "B"} {
			if err := h.conn.WriteMessage(1, contextFrame(text)); err != nil {
				t.Fatal(err)
			}
		}
		wait(time.Second) // redialed; the flush is blocked on its first send
		if err := h.conn.WriteMessage(1, contextFrame("C")); err != nil {
			t.Fatal(err)
		}
		close(second.gate)
		synctest.Wait()
		if err := h.conn.WriteMessage(1, contextFrame("D")); err != nil {
			t.Fatal(err)
		}
		if got := sentTexts(t, second); !slices.Equal(got, []string{"A", "B", "C", "D"}) {
			t.Fatalf("sent %v, want A B C D", got)
		}
		if err := h.session.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

// A sideband that fails while the held events are flushed is a new loss:
// the unsent events stay held, first, and go out on the next sideband.
func TestAFailedFlushRedialsAndKeepsTheHeldEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		broken, third := newFakeControl(), newFakeControl()
		broken.sendErr = errors.New("broken pipe")
		h := newHarness(t, &dialer{results: []any{broken, third}})
		h.side.events <- errors.New("connection reset")
		synctest.Wait()
		for _, text := range []string{"A", "B"} {
			if err := h.conn.WriteMessage(1, contextFrame(text)); err != nil {
				t.Fatal(err)
			}
		}
		wait(2 * time.Second)
		if got := sentTexts(t, third); !slices.Equal(got, []string{"A", "B"}) {
			t.Fatalf("sent on the third sideband %v, want A B", got)
		}
		if err := h.session.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

// A credential the token manager failed to refresh on the network is
// retried on the next dial; only a lost sign-in is fatal.
func TestATransientCredentialFailureIsRetried(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		second := newFakeControl()
		dial := &dialer{results: []any{transientCredential(), second}}
		h := newHarness(t, dial)
		h.side.events <- errors.New("connection reset")
		wait(time.Second)
		if n := len(dial.dialTimes()); n != 2 {
			t.Fatalf("dials = %d, want the transient failure retried once", n)
		}
		second.events <- quicksilver.SessionUpdated{}
		h.expect(t, messages.StreamTypeSessionUpdated)
		if err := h.session.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

// session.started's expires_at schedules the end of the session, and a later
// session.updated expiry replaces it.
func TestTheSessionExpiresAtExpiresAt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		start := time.Now()
		h.side.events <- quicksilver.SessionStarted{Session: &quicksilver.SessionResource{ExpiresAt: start.Add(10 * time.Second).Unix()}}
		h.side.events <- quicksilver.SessionUpdated{Session: &quicksilver.SessionResource{ID: "s", ExpiresAt: start.Add(20 * time.Second).Unix()}}
		h.expect(t, messages.StreamTypeSessionUpdated)
		closed := valueOf[*messages.SessionCloseValue](t, h.expect(t, messages.StreamTypeSessionClose))
		if elapsed := time.Since(start); elapsed != 20*time.Second || closed.Reason != livesession.CloseReasonExpired || closed.TerminalReason != messages.TerminalReasonProviderClose {
			t.Fatalf("SESSION.CLOSE %s/%s after %v, want expired after 20s", closed.Reason, closed.TerminalReason, elapsed)
		}
		if err := h.session.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

// A client delegation.created becomes DELEGATION.CREATED, with no response
// id, its input_text as the task and the recent transcript; with an offset
// the untimed quicksilver transcript cannot cover, it waits out the settle
// window.
func TestADelegationIsReportedWithItsTask(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		h.side.events <- quicksilver.InputTranscriptAdded{Item: quicksilver.TranscriptItem{Text: "what is the weather"}}
		h.expect(t, messages.StreamTypeInputItemAdded)
		h.expect(t, messages.StreamTypeTranscriptDelta)
		offset := int64(1200)
		start := time.Now()
		h.side.events <- quicksilver.DelegationCreated{OffsetMS: &offset, Item: quicksilver.DelegationItem{
			ID: "del_1", Type: quicksilver.ItemTypeDelegation, Target: quicksilver.TargetClient,
			Content: []quicksilver.ContentPart{{Type: quicksilver.PartInputText, Text: "check the "}, {Type: quicksilver.PartOutputText, Text: "x"}, {Type: quicksilver.PartInputText, Text: "weather"}},
		}}
		var msg messages.StreamMessage
		for msg = h.next(t); msg.Type != messages.StreamTypeDelegationCreated; msg = h.next(t) {
		}
		value := valueOf[*messages.DelegationCreatedValue](t, msg)
		if time.Since(start) != DefaultDelegationSettle || msg.ResponseID != "" || value.ID != "del_1" || value.Task != "check the weather" || value.OffsetMS != 1200 {
			t.Fatalf("DELEGATION.CREATED after %v: %+v (response %q)", time.Since(start), value, msg.ResponseID)
		}
		if len(value.Transcript) != 1 || value.Transcript[0].Text != "what is the weather" || value.Transcript[0].Speaker != messages.RoleUser {
			t.Fatalf("transcript = %+v", value.Transcript)
		}
		h.side.events <- quicksilver.DelegationCreated{Item: quicksilver.DelegationItem{ID: "del_2", Type: quicksilver.ItemTypeDelegation, Target: quicksilver.TargetClient}}
		if second := valueOf[*messages.DelegationCreatedValue](t, h.expect(t, messages.StreamTypeDelegationCreated)); second.ID != "del_2" {
			t.Fatalf("second delegation = %+v, want it reported at once (no offset)", second)
		}
		if err := h.session.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

// CONTEXT.APPEND maps onto delegation.context.append or
// session.context.append with the Codex channels, split into chunks of at
// most 500 bytes in order; a kind the dialect has no event for fails.
func TestContextAppendMapsOntoTheQuicksilverAppends(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, nil)
		id := "del_1"
		send := func(kind messages.ContextAppendKind, delegationID *string, content string) messages.SessionSendOutcome {
			return h.session.SendWithOutcome(t.Context(), messages.StreamMessage{
				Type: messages.StreamTypeContextAppend, Value: &messages.ContextAppendValue{Kind: kind, DelegationID: delegationID, Content: content},
			})
		}
		long := strings.Repeat("The table is booked. ", 60)
		for _, tc := range []struct {
			kind    messages.ContextAppendKind
			id      *string
			content string
		}{
			{messages.ContextAppendCommentary, &id, long},
			{messages.ContextAppendThinking, &id, "Checking."},
			{messages.ContextAppendInstructions, nil, "Be brief."},
		} {
			if outcome := send(tc.kind, tc.id, tc.content); !outcome.OK() {
				t.Fatalf("CONTEXT.APPEND %s: %+v", tc.kind, outcome)
			}
		}
		if outcome := send("shout", nil, "x"); outcome.OK() || !errors.Is(outcome.Err, livesession.ErrNoWireEvent) {
			t.Fatalf("unknown kind = %+v, want ErrNoWireEvent", outcome)
		}
		synctest.Wait()
		sent := h.side.sentEvents()
		var joined strings.Builder
		i := 0
		for ; i < len(sent); i++ {
			appended, ok := sent[i].(quicksilver.DelegationContextAppend)
			if !ok || appended.Channel != quicksilver.ChannelSpeakable {
				break
			}
			if appended.DelegationItemID != id || len(appended.Content[0].Text) > livesession.MaxAppendTokens {
				t.Fatalf("chunk %d = %+v", i, appended)
			}
			joined.WriteString(appended.Content[0].Text + " ")
		}
		if i < 2 || strings.TrimSpace(joined.String()) != strings.TrimSpace(long) {
			t.Fatalf("long result went out as %d speakable chunks, want the content split in order", i)
		}
		thinking, ok := sent[i].(quicksilver.DelegationContextAppend)
		if !ok || thinking.Channel != quicksilver.ChannelCommentary || thinking.Content[0].Text != "Checking." {
			t.Fatalf("thinking = %#v", sent[i])
		}
		instructions, ok := sent[i+1].(quicksilver.SessionContextAppend)
		if !ok || instructions.Channel != "" || instructions.Content[0].Text != "Be brief." || len(sent) != i+2 {
			t.Fatalf("instructions = %#v (%d events)", sent[i+1], len(sent))
		}
		if err := h.session.Close(); err != nil {
			t.Fatal(err)
		}
	})
}
