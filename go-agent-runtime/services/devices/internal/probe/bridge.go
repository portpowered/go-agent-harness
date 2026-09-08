package deviceprobe

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/participants"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

type liveDeviceProbeSessionBridge struct {
	runner *participants.ModelRunner
	output *liveDeviceProbeMediaLink
	sink   audio.AudioSink

	opened       chan struct{}
	responseDone chan struct{}
	done         chan struct{}
	openOnce     sync.Once
	responseOnce sync.Once

	mu             sync.Mutex
	transcript     strings.Builder
	fullTranscript string
	outputSamples  []int16
	outputFrames   int
	err            error
	audioBuffer    []int16
	sinkBuffer     []int16
}

func newLiveDeviceProbeSessionBridge(runner *participants.ModelRunner, output *liveDeviceProbeMediaLink, sink audio.AudioSink) *liveDeviceProbeSessionBridge {
	return &liveDeviceProbeSessionBridge{
		runner:       runner,
		output:       output,
		sink:         sink,
		opened:       make(chan struct{}),
		responseDone: make(chan struct{}),
		done:         make(chan struct{}),
	}
}

func (b *liveDeviceProbeSessionBridge) Run(ctx context.Context) {
	defer close(b.done)
	for {
		message, ok := b.runner.DeltaOutbox.ReadBlockingContext(ctx)
		if !ok {
			b.handleOutputEnd(ctx)
			b.finishResponse()
			return
		}
		done, err := b.handleMessage(ctx, message)
		if err != nil {
			b.setError(err)
			b.finishResponse()
			return
		}
		if done {
			b.finishResponse()
			return
		}
	}
}

func (b *liveDeviceProbeSessionBridge) handleOutputEnd(ctx context.Context) {
	if ctx.Err() == nil {
		b.setError(errors.New("session output ended before MESSAGE.END"))
	}
}

func (b *liveDeviceProbeSessionBridge) handleMessage(ctx context.Context, message messages.StreamMessage) (bool, error) {
	switch message.Type {
	case messages.StreamTypeSessionOpen:
		b.openOnce.Do(func() { close(b.opened) })
	case messages.StreamTypeTranscriptDelta:
		return false, b.handleTranscriptDelta(message.Value)
	case messages.StreamTypeTranscriptEnd:
		return false, b.handleTranscriptEnd(message.Value)
	case messages.StreamTypeAudioDelta:
		return false, b.handleAudioDelta(ctx, message.Value)
	case messages.StreamTypeAudioEnd:
		return false, b.flushAudio(ctx, true)
	case messages.StreamTypeError:
		if value, ok := message.Value.(*messages.ErrorValue); ok && value != nil && value.IsNonTerminal() {
			return false, nil
		}
		return true, liveDeviceProbeSessionError(message)
	case messages.StreamTypeMessageEnd:
		return true, b.flushAudio(ctx, true)
	case messages.StreamTypeSessionClose:
		return true, errors.New("session closed before response completion")
	case messages.StreamTypeMessageStart,
		messages.StreamTypeTextStart, messages.StreamTypeTextDelta, messages.StreamTypeTextEnd,
		messages.StreamTypeToolCallStart, messages.StreamTypeToolCallDelta, messages.StreamTypeToolCallEnd,
		messages.StreamTypeAudioStart,
		messages.StreamTypeImageStart, messages.StreamTypeImageDelta, messages.StreamTypeImageEnd,
		messages.StreamTypeVideoStart, messages.StreamTypeVideoDelta, messages.StreamTypeVideoEnd,
		messages.StreamTypeFileStart, messages.StreamTypeFileDelta, messages.StreamTypeFileEnd,
		messages.StreamTypeEmbeddingStart, messages.StreamTypeEmbeddingDelta, messages.StreamTypeEmbeddingEnd,
		messages.StreamTypeReasoningStart, messages.StreamTypeReasoningDelta, messages.StreamTypeReasoningEnd,
		messages.StreamTypeVADSpeechStarted, messages.StreamTypeVADSpeechStopped,
		messages.StreamTypeTranscriptStart,
		messages.StreamTypeInputItemAdded, messages.StreamTypePong,
		messages.StreamTypeSessionCreated, messages.StreamTypeSessionUpdated, messages.StreamTypeSessionUpdate,
		messages.StreamTypeResponseCancel, messages.StreamTypeResponseCreate,
		messages.StreamTypeRefusal, messages.StreamTypeLoopEnd, messages.StreamTypeUsageInfo,
		messages.StreamTypeSystemFullMessage:
		return false, nil
	}
	return false, nil
}

func (b *liveDeviceProbeSessionBridge) handleTranscriptDelta(value any) error {
	delta, ok := value.(*messages.TranscriptDeltaValue)
	if !ok || delta == nil {
		return fmt.Errorf("session transcript delta value = %T", value)
	}
	b.mu.Lock()
	b.transcript.WriteString(delta.Text)
	b.mu.Unlock()
	return nil
}

func (b *liveDeviceProbeSessionBridge) handleTranscriptEnd(value any) error {
	end, ok := value.(*messages.TranscriptEndValue)
	if !ok || end == nil {
		return fmt.Errorf("session transcript end value = %T", value)
	}
	b.mu.Lock()
	b.fullTranscript = end.FullText
	b.mu.Unlock()
	return nil
}

func (b *liveDeviceProbeSessionBridge) handleAudioDelta(ctx context.Context, value any) error {
	delta, ok := value.(*messages.AudioDeltaValue)
	if !ok || delta == nil {
		return fmt.Errorf("session audio delta value = %T", value)
	}
	return b.writeAudioDelta(ctx, delta)
}

func (b *liveDeviceProbeSessionBridge) writeAudioDelta(ctx context.Context, value *messages.AudioDeltaValue) error {
	if value.MediaType != "" && !strings.Contains(strings.ToLower(value.MediaType), "pcm") {
		return fmt.Errorf("session output audio format %q is not PCM16", value.MediaType)
	}
	samples, err := codec.DecodePCM16(value.Content)
	if err != nil {
		return fmt.Errorf("session output audio: %w", err)
	}
	b.mu.Lock()
	b.audioBuffer = append(b.audioBuffer, samples...)
	b.mu.Unlock()
	return b.flushAudio(ctx, false)
}

func (b *liveDeviceProbeSessionBridge) flushAudio(ctx context.Context, final bool) error {
	for {
		b.mu.Lock()
		if len(b.audioBuffer) == 0 || (!final && len(b.audioBuffer) < deviceProbeProviderFrameSamples) {
			b.mu.Unlock()
			break
		}
		frameLength := deviceProbeProviderFrameSamples
		if len(b.audioBuffer) < frameLength {
			frameLength = len(b.audioBuffer)
		}
		providerFrame := make([]int16, deviceProbeProviderFrameSamples)
		copy(providerFrame, b.audioBuffer[:frameLength])
		b.audioBuffer = b.audioBuffer[frameLength:]
		b.mu.Unlock()
		if err := b.writeOutputFrame(ctx, providerFrame); err != nil {
			return err
		}
	}
	if final {
		return b.flushSink(ctx, true)
	}
	return nil
}

func (b *liveDeviceProbeSessionBridge) writeOutputFrame(ctx context.Context, providerFrame []int16) error {
	outputFrame, err := wavio.Resample(providerFrame, deviceProbeProviderSampleRate, deviceProbeInputSampleRate)
	if err != nil {
		return fmt.Errorf("resample session output: %w", err)
	}
	if len(outputFrame) != deviceProbeInputFrameSamples {
		return fmt.Errorf("resampled session output has %d samples, want %d", len(outputFrame), deviceProbeInputFrameSamples)
	}
	emitted, err := b.output.RoundTrip(ctx, outputFrame)
	if err != nil {
		return fmt.Errorf("round-trip session output over WebRTC: %w", err)
	}
	b.mu.Lock()
	b.sinkBuffer = append(b.sinkBuffer, emitted...)
	b.mu.Unlock()
	return b.flushSink(ctx, false)
}

func (b *liveDeviceProbeSessionBridge) flushSink(ctx context.Context, final bool) error {
	for {
		b.mu.Lock()
		if len(b.sinkBuffer) == 0 || (!final && len(b.sinkBuffer) < audio.FrameSize) {
			b.mu.Unlock()
			return nil
		}
		frameLength := audio.FrameSize
		if len(b.sinkBuffer) < frameLength {
			frameLength = len(b.sinkBuffer)
		}
		frame := make([]int16, audio.FrameSize)
		copy(frame, b.sinkBuffer[:frameLength])
		b.sinkBuffer = b.sinkBuffer[frameLength:]
		b.mu.Unlock()
		if err := b.sink.WriteFrame(ctx, frame); err != nil {
			return fmt.Errorf("write session output to selected speaker: %w", err)
		}
		b.mu.Lock()
		b.outputSamples = append(b.outputSamples, frame...)
		b.outputFrames++
		b.mu.Unlock()
	}
}

func (b *liveDeviceProbeSessionBridge) waitOpened(ctx context.Context) error {
	select {
	case <-b.opened:
		return nil
	case <-b.responseDone:
		return b.errorValue(errors.New("session ended before SESSION.OPEN"))
	case <-b.done:
		return b.errorValue(errors.New("session ended before SESSION.OPEN"))
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *liveDeviceProbeSessionBridge) waitResponse(ctx context.Context) error {
	select {
	case <-b.responseDone:
		return b.errorValue(nil)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *liveDeviceProbeSessionBridge) finishResponse() {
	b.responseOnce.Do(func() { close(b.responseDone) })
}

func (b *liveDeviceProbeSessionBridge) setError(err error) {
	if err == nil {
		return
	}
	b.mu.Lock()
	if b.err == nil {
		b.err = err
	}
	b.mu.Unlock()
}

func (b *liveDeviceProbeSessionBridge) errorValue(fallback error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	return fallback
}

func (b *liveDeviceProbeSessionBridge) snapshot() probe.ObservationSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	transcript := b.fullTranscript
	if strings.TrimSpace(transcript) == "" {
		transcript = b.transcript.String()
	}
	return probe.ObservationSnapshot{
		PCM16Samples:       append([]int16(nil), b.outputSamples...),
		Transcript:         transcript,
		FrameCount:         b.outputFrames,
		TerminalReason:     "message_end",
		TerminalProvenance: "provider",
		OutputState:        "complete",
	}
}

func liveDeviceProbeSessionError(message messages.StreamMessage) error {
	if value, ok := message.Value.(*messages.ErrorValue); ok && value != nil {
		return fmt.Errorf("realtime provider session error: %s", value.Message)
	}
	return fmt.Errorf("realtime provider session error: %T", message.Value)
}
