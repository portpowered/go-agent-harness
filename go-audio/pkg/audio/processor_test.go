package audio_test

import (
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
