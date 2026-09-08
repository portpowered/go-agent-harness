package audio_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

func TestProcessorChunkInvariantAndExactTail(t *testing.T) {
	for _, rates := range [][2]int{{16000, 24000}, {24000, 16000}, {24000, 48000}, {48000, 24000}, {24000, 24000}} {
		input := make([]int16, 1007)
		for i := range input {
			input[i] = int16((i*137)%15000 - 7500)
		}
		process := func(chunk int) ([]int16, int) {
			p, err := audio.NewProcessor(audio.PCM16DeviceFormat(rates[0]), audio.PCM16DeviceFormat(rates[1]), 480)
			if err != nil {
				t.Fatal(err)
			}
			var got []int16
			ends := 0
			for i := 0; i < len(input); i += chunk {
				end := min(i+chunk, len(input))
				frames, err := p.Process(audio.PCMFrame{Samples: input[i:end], EndOfResponse: end == len(input), StreamID: "s", Epoch: 2})
				if err != nil {
					t.Fatal(err)
				}
				for _, f := range frames {
					if f.StartSample != uint64(len(got)) || f.Epoch != 2 || f.StreamID != "s" || f.Format.SampleRate != rates[1] {
						t.Fatalf("lineage=%+v", f)
					}
					got = append(got, f.Samples...)
					if f.EndOfResponse {
						ends++
					}
				}
			}
			if _, err := p.Process(audio.PCMFrame{}); !errors.Is(err, wavio.ErrResamplerEnded) {
				t.Fatalf("double flush=%v", err)
			}
			return got, ends
		}
		whole, _ := process(len(input))
		for _, size := range []int{1, 7, 159, 480, 997} {
			got, ends := process(size)
			if !reflect.DeepEqual(got, whole) || ends != 1 {
				t.Fatalf("rates=%v chunk=%d samples=%d want=%d end markers=%d", rates, size, len(got), len(whole), ends)
			}
		}
	}
}

func TestProcessorResetDiscardsPendingWithoutPadding(t *testing.T) {
	f := audio.PCM16DeviceFormat(24000)
	p, err := audio.NewProcessor(f, f, 480)
	if err != nil {
		t.Fatal(err)
	}
	if frames, err := p.Process(audio.PCMFrame{Samples: []int16{1, 2}}); err != nil || len(frames) != 0 {
		t.Fatalf("partial=%v,%v", frames, err)
	}
	if n, err := p.Reset(); n != 2 || err != nil {
		t.Fatalf("reset=%d,%v", n, err)
	}
	frames, err := p.Process(audio.PCMFrame{Samples: []int16{9}, EndOfResponse: true, Epoch: 1})
	if err != nil || len(frames) != 1 || !reflect.DeepEqual(frames[0].Samples, []int16{9}) {
		t.Fatalf("new response=%v,%v", frames, err)
	}
}

func TestProcessorRejectsCrossStreamHistoryWithoutReset(t *testing.T) {
	for _, change := range []func(*audio.PCMFrame){func(f *audio.PCMFrame) { f.StreamID = "other" }, func(f *audio.PCMFrame) { f.Epoch++ }, func(f *audio.PCMFrame) { f.PlaybackResponse.ResponseID = "other" }} {
		processor, err := audio.NewProcessor(audio.PCM16DeviceFormat(16000), audio.PCM16DeviceFormat(24000), 480)
		if err != nil {
			t.Fatal(err)
		}
		first := audio.PCMFrame{StreamID: "capture", Epoch: 1, Samples: []int16{123, 456}}
		if _, err := processor.Process(first); err != nil {
			t.Fatal(err)
		}
		next := first
		change(&next)
		if _, err := processor.Process(next); err != audio.ErrStreamIdentityChanged {
			t.Fatalf("cross-stream audio accepted: %v", err)
		}
		if _, err := processor.Reset(); err != nil {
			t.Fatal(err)
		}
		next.EndOfResponse = true
		got, err := processor.Process(next)
		if err != nil || len(got) == 0 || got[0].StartSample != 0 || got[0].StreamID != next.StreamID || got[0].Epoch != next.Epoch {
			t.Fatalf("reset did not establish new stream: %v %v", got, err)
		}
	}
}

func TestFrameAccumulatorSplitsAtBudgetAndPreservesLineage(t *testing.T) {
	target := &recordingFrameTarget{}
	accumulator, err := audio.NewFrameAccumulator(target, 5)
	if err != nil {
		t.Fatal(err)
	}
	format := audio.PCM16DeviceFormat(24000)
	response := audio.PlaybackResponse{ResponseID: "response-1", ItemID: "item-1", ContentIndex: 2}
	first := audio.PCMFrame{
		Samples: []int16{1, 2, 3}, Format: format, StreamID: "capture", Epoch: 4,
		Sequence: 7, StartSample: 11, PlaybackResponse: response,
	}
	if err := accumulator.WriteFrame(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	first.Samples[0] = 99
	if err := accumulator.WriteFrame(context.Background(), audio.PCMFrame{
		Samples: []int16{4, 5, 6, 7}, Format: format, StreamID: "capture", Epoch: 4,
		Sequence: 8, StartSample: 14, PlaybackResponse: response,
	}); err != nil {
		t.Fatal(err)
	}
	if len(target.frames) != 1 || !reflect.DeepEqual(target.frames[0].Samples, []int16{1, 2, 3, 4, 5}) {
		t.Fatalf("full frames=%+v, want one five-sample frame", target.frames)
	}
	if err := accumulator.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(target.frames) != 2 {
		t.Fatalf("emitted frames=%d, want two", len(target.frames))
	}
	for index, frame := range target.frames {
		if len(frame.Samples) > 5 || frame.Format != format || frame.StreamID != "capture" || frame.Epoch != 4 || frame.PlaybackResponse != response {
			t.Fatalf("frame %d lineage=%+v", index, frame)
		}
		if frame.Sequence != uint64(7+index) || frame.StartSample != uint64(11+index*5) {
			t.Fatalf("frame %d cursor sequence=%d start=%d", index, frame.Sequence, frame.StartSample)
		}
		if frame.EndOfResponse != (index == len(target.frames)-1) {
			t.Fatalf("frame %d end=%t", index, frame.EndOfResponse)
		}
	}
}

func TestFrameAccumulatorRejectsOversizeAndMetadataDiscontinuity(t *testing.T) {
	target := &recordingFrameTarget{}
	accumulator, err := audio.NewFrameAccumulator(target, 3)
	if err != nil {
		t.Fatal(err)
	}
	base := audio.PCMFrame{Samples: []int16{1}, Format: audio.PCM16DeviceFormat(24000), StreamID: "capture", Epoch: 2}
	if err := accumulator.WriteFrame(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	if err := accumulator.WriteFrame(context.Background(), audio.PCMFrame{Samples: []int16{1, 2, 3, 4}, Format: base.Format, StreamID: base.StreamID, Epoch: base.Epoch}); !errors.Is(err, audio.ErrFrameAccumulatorFrameTooLarge) {
		t.Fatalf("oversize error=%v, want ErrFrameAccumulatorFrameTooLarge", err)
	}
	mutations := []func(*audio.PCMFrame){
		func(frame *audio.PCMFrame) { frame.Format = audio.PCM16DeviceFormat(16000) },
		func(frame *audio.PCMFrame) { frame.StreamID = "other" },
		func(frame *audio.PCMFrame) { frame.Epoch = 3 },
		func(frame *audio.PCMFrame) { frame.PlaybackResponse.ResponseID = "other" },
	}
	for _, mutate := range mutations {
		next := base
		next.Samples = []int16{2}
		mutate(&next)
		if err := accumulator.WriteFrame(context.Background(), next); !errors.Is(err, audio.ErrFrameAccumulatorMetadataChanged) {
			t.Fatalf("metadata error=%v, want ErrFrameAccumulatorMetadataChanged", err)
		}
	}
	if err := accumulator.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(target.frames) != 1 || !reflect.DeepEqual(target.frames[0].Samples, []int16{1}) {
		t.Fatalf("accepted samples=%+v, want only base frame", target.frames)
	}
}

func TestFrameAccumulatorFlushesExplicitEmptyTailOnce(t *testing.T) {
	target := &recordingFrameTarget{}
	accumulator, err := audio.NewFrameAccumulator(target, 3)
	if err != nil {
		t.Fatal(err)
	}
	frame := audio.PCMFrame{Format: audio.PCM16DeviceFormat(24000), StreamID: "capture", Epoch: 9, EndOfResponse: true}
	if err := accumulator.WriteFrame(context.Background(), frame); err != nil {
		t.Fatal(err)
	}
	if err := accumulator.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := accumulator.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(target.frames) != 1 || len(target.frames[0].Samples) != 0 || !target.frames[0].EndOfResponse {
		t.Fatalf("empty tail=%+v, want one empty end marker", target.frames)
	}
	if err := accumulator.WriteFrame(context.Background(), audio.PCMFrame{Samples: []int16{1}}); !errors.Is(err, audio.ErrFrameAccumulatorClosed) {
		t.Fatalf("write after flush=%v, want ErrFrameAccumulatorClosed", err)
	}
}

func TestFrameAccumulatorRejectsInputCursorGaps(t *testing.T) {
	target := &recordingFrameTarget{}
	accumulator, err := audio.NewFrameAccumulator(target, 4)
	if err != nil {
		t.Fatal(err)
	}
	format := audio.PCM16DeviceFormat(24000)
	first := audio.PCMFrame{Samples: []int16{1, 2}, Format: format, StreamID: "capture", Epoch: 1, Sequence: 10, StartSample: 20}
	if err := accumulator.WriteFrame(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	for _, gap := range []audio.PCMFrame{
		{Samples: []int16{3, 4}, Format: format, StreamID: "capture", Epoch: 1, Sequence: 12, StartSample: 22},
		{Samples: []int16{3, 4}, Format: format, StreamID: "capture", Epoch: 1, Sequence: 11, StartSample: 23},
	} {
		if err := accumulator.WriteFrame(context.Background(), gap); !errors.Is(err, audio.ErrFrameAccumulatorMetadataChanged) {
			t.Fatalf("cursor gap error=%v, want ErrFrameAccumulatorMetadataChanged", err)
		}
	}
	if err := accumulator.WriteFrame(context.Background(), audio.PCMFrame{
		Samples: []int16{3, 4}, Format: format, StreamID: "capture", Epoch: 1,
		Sequence: 11, StartSample: 22,
	}); err != nil {
		t.Fatalf("contiguous frame rejected after gap: %v", err)
	}
	if err := accumulator.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(target.frames) != 1 || !reflect.DeepEqual(target.frames[0].Samples, []int16{1, 2, 3, 4}) {
		t.Fatalf("accepted samples=%+v, want contiguous frames only", target.frames)
	}
}

func TestFrameAccumulatorRequiresInterleavedChannelAlignment(t *testing.T) {
	target := &recordingFrameTarget{}
	oddBudget, err := audio.NewFrameAccumulator(target, 3)
	if err != nil {
		t.Fatal(err)
	}
	stereo := audio.DeviceFormat{SampleRate: 24000, Channels: 2, BitDepth: audio.DeviceBitDepthPCM16, Encoding: audio.DeviceEncodingPCM16}
	if err := oddBudget.WriteFrame(context.Background(), audio.PCMFrame{Samples: []int16{1, 2}, Format: stereo}); !errors.Is(err, audio.ErrInvalidDeviceFormat) {
		t.Fatalf("odd stereo budget error=%v, want ErrInvalidDeviceFormat", err)
	}

	accumulator, err := audio.NewFrameAccumulator(target, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := accumulator.WriteFrame(context.Background(), audio.PCMFrame{Samples: []int16{1, 2}, Format: stereo, Sequence: 4, StartSample: 8}); err != nil {
		t.Fatal(err)
	}
	if err := accumulator.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(target.frames) != 1 || target.frames[0].StartSample != 8 || target.frames[0].Sequence != 4 || len(target.frames[0].Samples) != 2 {
		t.Fatalf("stereo frame=%+v, want two samples at source cursor", target.frames)
	}
}

type recordingFrameTarget struct {
	frames []audio.PCMFrame
}

func (target *recordingFrameTarget) WriteFrame(_ context.Context, frame audio.PCMFrame) error {
	frame.Samples = append([]int16(nil), frame.Samples...)
	target.frames = append(target.frames, frame)
	return nil
}

func (*recordingFrameTarget) Close() error { return nil }
