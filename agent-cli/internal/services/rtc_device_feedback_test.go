package services

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

func TestLocalFeedbackGateSuppressesLoopAndWarnsOnce(t *testing.T) {
	warning := make(chan string, 1)
	gate, err := newLocalFeedbackGate(audio.DefaultSelfHearingConfig(), feedbackWarningChannel(warning))
	if err != nil {
		t.Fatalf("new local feedback gate: %v", err)
	}

	playback := make([][]int16, 5)
	for frameIndex := range playback {
		playback[frameIndex] = feedbackSignal(frameIndex, 17)
		if err := gate.WritePlayback(context.Background(), playback[frameIndex], func() error { return nil }); err != nil {
			t.Fatalf("observe playback frame %d: %v", frameIndex, err)
		}
	}

	for frameIndex, want := range playback {
		released, err := gate.FilterCapture(context.Background(), want)
		if err != nil {
			t.Fatalf("filter looped capture frame %d: %v", frameIndex, err)
		}
		if len(released) != 0 {
			t.Fatalf("looped capture frame %d released %d frames, want none (state=%q)", frameIndex, len(released), gate.state)
		}
	}

	select {
	case got := <-warning:
		if !strings.Contains(got, "Acoustic feedback detected") || !strings.Contains(got, "headphones") || !strings.Contains(got, "file") {
			t.Fatalf("warning = %q, want diagnosis and headphones/file remedies", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for feedback warning")
	}

	// A continuing correlated window is still suppressed, but its warning is
	// session-scoped and cannot flood stderr.
	for frameIndex := range playback {
		if err := gate.WritePlayback(context.Background(), playback[frameIndex], func() error { return nil }); err != nil {
			t.Fatalf("observe repeated playback frame %d: %v", frameIndex, err)
		}
		released, err := gate.FilterCapture(context.Background(), playback[frameIndex])
		if err != nil {
			t.Fatalf("filter repeated looped capture frame %d: %v", frameIndex, err)
		}
		if len(released) != 0 {
			t.Fatalf("repeated looped capture frame %d released %d frames, want none", frameIndex, len(released))
		}
	}
	select {
	case extra := <-warning:
		t.Fatalf("repeated feedback emitted another warning %q", extra)
	default:
	}

	if err := gate.Close(); err != nil {
		t.Fatalf("close feedback gate: %v", err)
	}
	_, err = gate.FilterCapture(context.Background(), playback[0])
	if !errors.Is(err, audio.ErrClosed) {
		t.Fatalf("filter after close = %v, want audio.ErrClosed", err)
	}
}

func TestLocalFeedbackGateReleasesIndependentCaptureOnceInOrder(t *testing.T) {
	warning := make(chan string, 1)
	gate, err := newLocalFeedbackGate(audio.DefaultSelfHearingConfig(), feedbackWarningChannel(warning))
	if err != nil {
		t.Fatalf("new local feedback gate: %v", err)
	}
	defer gate.Close()

	for frameIndex := 0; frameIndex < 5; frameIndex++ {
		if err := gate.WritePlayback(context.Background(), feedbackSignal(frameIndex, 23), func() error { return nil }); err != nil {
			t.Fatalf("observe playback frame %d: %v", frameIndex, err)
		}
	}

	want := make([][]int16, 5)
	var got [][]int16
	for frameIndex := range want {
		want[frameIndex] = feedbackSignal(frameIndex, 71)
		released, err := gate.FilterCapture(context.Background(), want[frameIndex])
		if err != nil {
			t.Fatalf("filter independent capture frame %d: %v", frameIndex, err)
		}
		got = append(got, released...)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("released independent frames = %d/%d or reordered, got %#v", len(got), len(want), got)
	}
	select {
	case extra := <-warning:
		t.Fatalf("independent capture emitted feedback warning %q", extra)
	default:
	}
}

func TestLocalFeedbackGateBlockedWarningWriterCannotBlockMedia(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	writer := blockingFeedbackWarningWriter{started: started, release: release}
	gate, err := newLocalFeedbackGate(audio.DefaultSelfHearingConfig(), writer)
	if err != nil {
		t.Fatalf("new local feedback gate: %v", err)
	}
	defer func() {
		close(release)
		_ = gate.Close()
	}()

	for frameIndex := 0; frameIndex < 5; frameIndex++ {
		if err := gate.WritePlayback(context.Background(), feedbackSignal(frameIndex, 17), func() error { return nil }); err != nil {
			t.Fatalf("observe playback frame %d: %v", frameIndex, err)
		}
	}
	for frameIndex := 0; frameIndex < 5; frameIndex++ {
		if _, err := gate.FilterCapture(context.Background(), feedbackSignal(frameIndex, 17)); err != nil {
			t.Fatalf("filter looped capture frame %d: %v", frameIndex, err)
		}
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for blocked warning writer")
	}

	mediaDone := make(chan struct{})
	go func() {
		_, _ = gate.FilterCapture(context.Background(), feedbackSignal(5, 17))
		close(mediaDone)
	}()
	select {
	case <-mediaDone:
	case <-time.After(time.Second):
		t.Fatal("capture classification blocked behind warning writer")
	}
}

func TestPairedDeviceBindingDropsLoopedSpeakerFramesBeforeProviderMedia(t *testing.T) {
	registry, err := audio.NewVirtualRegistry(audio.VirtualBackendConfig{
		Devices: []audio.VirtualDeviceConfig{
			{ID: "mic", Name: "Virtual Mic", Direction: audio.DirectionInput, LoopbackID: "speaker"},
			{ID: "speaker", Name: "Virtual Speaker", Direction: audio.DirectionOutput, LoopbackID: "mic"},
		},
		Defaults: map[audio.Direction]string{
			audio.DirectionInput:  "mic",
			audio.DirectionOutput: "speaker",
		},
	})
	if err != nil {
		t.Fatalf("new virtual feedback registry: %v", err)
	}
	warning := make(chan string, 1)
	feedbackConfig := audio.DefaultSelfHearingConfig()
	feedbackConfig.CorrelationLagWindow = audio.PCM16LagWindow{Min: -5 * time.Millisecond, Max: 5 * time.Millisecond}
	feedbackConfig.MinimumEvidence = 30 * time.Millisecond
	feedbackConfig.MaximumReleaseLatency = 500 * time.Millisecond
	binding, err := PrepareRTCDeviceBindings(RTCDeviceBindingRequest{
		Registry:              registry,
		InputPresent:          true,
		OutputPresent:         true,
		SelfHearingConfig:     feedbackConfig,
		FeedbackWarningWriter: feedbackWarningChannel(warning),
	})
	if err != nil {
		t.Fatalf("prepare paired binding: %v", err)
	}
	if binding == nil || binding.Source == nil || binding.Sink == nil {
		t.Fatalf("binding = %#v, want paired source and sink", binding)
	}
	userFeed, err := audio.NewDeviceSink(registry, binding.Sink.DeviceID())
	if err != nil {
		_ = binding.Close()
		t.Fatalf("open independent virtual user feeder: %v", err)
	}
	defer userFeed.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inbound := newFeedbackInbound(5)
	outbound := &feedbackOutbound{frames: make(chan rtc.PCMFrame, 5)}
	sinkDone := make(chan error, 1)
	sourceDone := make(chan error, 1)
	go func() { sinkDone <- binding.Sink.Pump(ctx, inbound) }()
	go func() { sourceDone <- binding.Source.Pump(ctx, outbound) }()

	for frameIndex := 0; frameIndex < 5; frameIndex++ {
		inbound.push(feedbackSignal(frameIndex, 47))
	}
	inbound.closeInput()

	select {
	case err := <-sinkDone:
		if err != nil {
			t.Fatalf("speaker pump: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("speaker pump did not consume virtual playback")
	}
	select {
	case got := <-warning:
		if !strings.Contains(got, "Acoustic feedback detected") {
			t.Fatalf("warning = %q, want acoustic-feedback diagnosis", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("paired device loop did not confirm self-hearing")
	}

	// Confirmation occurs before the first provider write can be made visible.
	// Feed a distinct user signal through the same virtual microphone after
	// confirmation to prove the gate suppresses only the correlated playback.
	wantUserFrames := make([][]int16, 5)
	for frameIndex := range wantUserFrames {
		wantUserFrames[frameIndex] = feedbackSignal(frameIndex, 97)
		if err := userFeed.WriteFrame(ctx, wantUserFrames[frameIndex]); err != nil {
			t.Fatalf("feed independent virtual user frame %d: %v", frameIndex, err)
		}
	}
	gotUserFrames := make([][]int16, 0, len(wantUserFrames))
	for range wantUserFrames {
		select {
		case frame := <-outbound.frames:
			gotUserFrames = append(gotUserFrames, frame.Samples)
		case <-time.After(5 * time.Second):
			t.Fatal("independent user frames did not reach provider media")
		}
	}
	if !reflect.DeepEqual(gotUserFrames, wantUserFrames) {
		t.Fatalf("provider user frames = %d/%d or reordered, got %#v", len(gotUserFrames), len(wantUserFrames), gotUserFrames)
	}
	if err := userFeed.Close(); err != nil {
		t.Fatalf("close independent virtual user feeder: %v", err)
	}

	// The remaining source pump is cancelled only after the paired loop has
	// supplied enough evidence for the gate to discard its held frames and the
	// independent user frames have been delivered.
	cancel()
	select {
	case err := <-sourceDone:
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, ErrRTCDeviceSourceClosed) {
			t.Fatalf("microphone pump: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("microphone pump did not stop after cancellation")
	}
	select {
	case frame := <-outbound.frames:
		t.Fatalf("looped speaker frame reached provider media with %d samples", len(frame.Samples))
	default:
	}

	if err := binding.Close(); err != nil {
		t.Fatalf("close paired binding: %v", err)
	}
}

func TestPairedDeviceFeedbackPreservesAssistantTerminal(t *testing.T) {
	registry, err := audio.NewVirtualRegistry(audio.VirtualBackendConfig{
		Devices: []audio.VirtualDeviceConfig{
			{ID: "mic", Name: "Virtual Mic", Direction: audio.DirectionInput, LoopbackID: "speaker"},
			{ID: "speaker", Name: "Virtual Speaker", Direction: audio.DirectionOutput, LoopbackID: "mic"},
		},
		Defaults: map[audio.Direction]string{
			audio.DirectionInput:  "mic",
			audio.DirectionOutput: "speaker",
		},
	})
	if err != nil {
		t.Fatalf("new virtual session registry: %v", err)
	}
	warning := make(chan string, 1)
	feedbackConfig := audio.DefaultSelfHearingConfig()
	feedbackConfig.CorrelationLagWindow = audio.PCM16LagWindow{Min: -5 * time.Millisecond, Max: 5 * time.Millisecond}
	feedbackConfig.MinimumEvidence = 30 * time.Millisecond
	feedbackConfig.MaximumReleaseLatency = 500 * time.Millisecond
	binding, err := PrepareRTCDeviceBindings(RTCDeviceBindingRequest{
		Registry:              registry,
		InputPresent:          true,
		OutputPresent:         true,
		SelfHearingConfig:     feedbackConfig,
		FeedbackWarningWriter: feedbackWarningChannel(warning),
	})
	if err != nil {
		t.Fatalf("prepare paired session binding: %v", err)
	}

	userFeed, err := audio.NewDeviceSink(registry, binding.Sink.DeviceID())
	if err != nil {
		_ = binding.Close()
		t.Fatalf("open virtual user feeder: %v", err)
	}
	defer userFeed.Close()

	providerInput := &feedbackOutbound{frames: make(chan rtc.PCMFrame, 16)}
	providerOutput := newFeedbackInbound(5)
	session := newFeedbackLiveSession(RTCMediaEndpoints{
		Inbound:  providerOutput,
		Outbound: providerInput,
	})
	inferencer := &feedbackLiveInferencer{session: session, connected: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t.Cleanup(func() {
		_ = session.Close()
		_ = binding.Close()
	})
	runErr := make(chan error, 1)
	go func() {
		runErr <- runAgentLoopSession(ctx, io.Discard, inferencer, sessionLoopOptions{
			rtcDeviceBinding: binding,
		})
	}()

	select {
	case <-inferencer.connected:
	case <-ctx.Done():
		t.Fatalf("live session did not connect: %v", ctx.Err())
	}

	for frameIndex := 0; frameIndex < 5; frameIndex++ {
		providerOutput.push(feedbackSignal(frameIndex, 113))
	}
	providerOutput.closeInput()
	select {
	case got := <-warning:
		if !strings.Contains(got, "Acoustic feedback detected") {
			t.Fatalf("warning = %q, want acoustic-feedback diagnosis", got)
		}
	case <-ctx.Done():
		t.Fatalf("session feedback was not confirmed: %v", ctx.Err())
	}

	wantUserFrames := make([][]int16, 5)
	for frameIndex := range wantUserFrames {
		wantUserFrames[frameIndex] = feedbackSignal(frameIndex, 127)
		if err := userFeed.WriteFrame(ctx, wantUserFrames[frameIndex]); err != nil {
			t.Fatalf("feed independent user frame %d: %v", frameIndex, err)
		}
	}
	gotUserFrames := make([][]int16, 0, len(wantUserFrames))
	for range wantUserFrames {
		select {
		case frame := <-providerInput.frames:
			gotUserFrames = append(gotUserFrames, frame.Samples)
		case <-ctx.Done():
			t.Fatalf("independent user frames did not reach provider media: %v", ctx.Err())
		}
	}
	if !reflect.DeepEqual(gotUserFrames, wantUserFrames) {
		t.Fatalf("provider user frames = %d/%d or reordered, got %#v", len(gotUserFrames), len(wantUserFrames), gotUserFrames)
	}
	select {
	case frame := <-providerInput.frames:
		t.Fatalf("correlated assistant frame reached provider media with %d samples", len(frame.Samples))
	default:
	}

	for _, message := range []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("completed")},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	} {
		if !session.recv.Write(ctx, message) {
			t.Fatalf("write provider response event %s: %v", message.Type, ctx.Err())
		}
	}

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("live session with filtered feedback: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("assistant terminal response did not complete: %v", ctx.Err())
	}
	if session.sawResponseCancel() {
		t.Fatal("filtered assistant playback caused a response cancellation")
	}
}

func feedbackSignal(frameIndex, seed int) []int16 {
	samples := make([]int16, audio.FrameSize)
	state := uint32(seed*7919 + frameIndex*104729 + 1)
	for index := range samples {
		state = state*1664525 + 1013904223
		samples[index] = int16(int32(state>>16)%24000 - 12000) //nolint:gosec // bounded deterministic PCM fixture
	}
	return samples
}

type feedbackLiveInferencer struct {
	session   *feedbackLiveSession
	connected chan struct{}
}

func (i *feedbackLiveInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if !i.session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("feedback-live-session", "test"),
	}) {
		return nil, ctx.Err()
	}
	select {
	case <-i.connected:
	default:
		close(i.connected)
	}
	return i.session, nil
}

type feedbackLiveSession struct {
	recv  *messages.TypedBuffer[messages.StreamMessage]
	done  chan struct{}
	media RTCMediaEndpoints
	sent  chan messages.StreamMessage
	once  sync.Once
}

func newFeedbackLiveSession(media RTCMediaEndpoints) *feedbackLiveSession {
	return &feedbackLiveSession{
		recv:  messages.NewTypedBuffer[messages.StreamMessage](32),
		done:  make(chan struct{}),
		media: media,
		sent:  make(chan messages.StreamMessage, 32),
	}
}

func (s *feedbackLiveSession) Send(ctx context.Context, message messages.StreamMessage) bool {
	select {
	case s.sent <- message:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *feedbackLiveSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.recv }

func (s *feedbackLiveSession) Done() <-chan struct{} { return s.done }

func (s *feedbackLiveSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

func (s *feedbackLiveSession) RTCMedia() RTCMediaEndpoints { return s.media }

func (s *feedbackLiveSession) sawResponseCancel() bool {
	for {
		select {
		case message := <-s.sent:
			if message.Type == messages.StreamTypeResponseCancel {
				return true
			}
		default:
			return false
		}
	}
}

var (
	_ messages.SessionInferencer = (*feedbackLiveInferencer)(nil)
	_ messages.Session           = (*feedbackLiveSession)(nil)
	_ RTCMediaSession            = (*feedbackLiveSession)(nil)
)

func feedbackWarningChannel(warning chan<- string) *feedbackChannelWriter {
	return &feedbackChannelWriter{warning: warning}
}

type feedbackChannelWriter struct {
	warning chan<- string
}

func (w *feedbackChannelWriter) Write(data []byte) (int, error) {
	w.warning <- string(data)
	return len(data), nil
}

type blockingFeedbackWarningWriter struct {
	started chan<- struct{}
	release <-chan struct{}
}

func (w blockingFeedbackWarningWriter) Write(data []byte) (int, error) {
	select {
	case w.started <- struct{}{}:
	default:
	}
	<-w.release
	return len(data), nil
}

type feedbackInbound struct {
	frames chan rtc.PCMFrame
	done   chan struct{}
	once   sync.Once
}

func newFeedbackInbound(capacity int) *feedbackInbound {
	return &feedbackInbound{frames: make(chan rtc.PCMFrame, capacity), done: make(chan struct{})}
}

func (m *feedbackInbound) ReadFrame(ctx context.Context) (rtc.PCMFrame, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		select {
		case frame := <-m.frames:
			return frame, nil
		default:
		}
		select {
		case frame := <-m.frames:
			return frame, nil
		case <-m.done:
			// closeInput may race with the final queued frame. Drain the queue
			// before reporting EOF so a finite provider playback sequence cannot
			// lose its last frame nondeterministically.
			select {
			case frame := <-m.frames:
				return frame, nil
			default:
				return rtc.PCMFrame{}, io.EOF
			}
		case <-ctx.Done():
			return rtc.PCMFrame{}, ctx.Err()
		}
	}
}

func (m *feedbackInbound) Close() error {
	m.once.Do(func() { close(m.done) })
	return nil
}

func (m *feedbackInbound) push(samples []int16) {
	m.frames <- rtc.PCMFrame{Samples: append([]int16(nil), samples...)}
}

func (m *feedbackInbound) closeInput() { m.once.Do(func() { close(m.done) }) }

type feedbackOutbound struct {
	frames chan rtc.PCMFrame
}

func (m *feedbackOutbound) WriteFrame(ctx context.Context, frame rtc.PCMFrame) error {
	select {
	case m.frames <- rtc.PCMFrame{Samples: append([]int16(nil), frame.Samples...)}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *feedbackOutbound) Close() error { return nil }

var _ rtc.InboundMedia = (*feedbackInbound)(nil)
var _ rtc.OutboundMedia = (*feedbackOutbound)(nil)
