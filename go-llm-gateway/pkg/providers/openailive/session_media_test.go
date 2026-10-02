package openailive_test

import (
	"bytes"
	"testing"
	"testing/synctest"

	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	live "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/fakelive"
)

func TestRTCMediaCarriesSegmentAudioBothWays(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		output := codec.EncodePCM16(make([]int16, 720))
		server := newFake(fakelive.AwaitClient(live.TypeInputAudioAppend, 1), fakelive.Send(audioDelta(output)))
		session := connectFake(t, server, pcmConfig())
		endpoints := as[sharedaudio.MediaSession](t, session).RTCMedia()
		input := []int16{1, -1, 2, -2}
		if err := endpoints.Outbound.WriteFrame(t.Context(), sharedaudio.PCMFrame{Samples: input}); err != nil {
			t.Fatalf("write frame: %v", err)
		}
		frame, err := endpoints.Inbound.ReadFrame(t.Context())
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		if frame.PlaybackResponse.ResponseID != firstSeg {
			t.Fatalf("frame belongs to %+v, want live_seg_1", frame.PlaybackResponse)
		}
		if got := server.InputAudio(); !bytes.Equal(got, codec.EncodePCM16(input)) {
			t.Fatalf("server input = %v, want the frame as PCM16", got)
		}
	})
}

func TestRTCMediaRefusesG711Sessions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := newFake()
		session := connectFake(t, server, models.SessionConfig{Model: live.Model1, InputAudioFormat: models.AudioFormatG711Ulaw, OutputAudioFormat: models.AudioFormatG711Ulaw})
		skipOpen(t, session)
		endpoints := as[sharedaudio.MediaSession](t, session).RTCMedia()
		if err := endpoints.Outbound.WriteFrame(t.Context(), sharedaudio.PCMFrame{Samples: []int16{1}}); err == nil {
			t.Fatal("G.711 session accepted a PCM16 RTC frame")
		}
		// G.711 bytes pass through unchanged, odd counts included.
		sendAudio(t, session, []byte{0xff, 0x7f, 0x00})
		if err := session.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		if got := server.InputAudio(); !bytes.Equal(got, []byte{0xff, 0x7f, 0x00}) {
			t.Fatalf("server input = %v, want the G.711 bytes", got)
		}
	})
}
