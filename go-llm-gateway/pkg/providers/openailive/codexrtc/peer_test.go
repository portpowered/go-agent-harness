package codexrtc_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexrtc"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexrtc/fakecodex"
)

// toneFrames is how many 20 ms frames each side sends; minAudibleRMS is the
// level a decoded 8000-amplitude sine stays well above once Opus settles.
const (
	toneFrames    = 25
	minAudibleRMS = 2000
)

func newNetwork(t *testing.T) *fakecodex.VirtualNetwork {
	t.Helper()
	network, err := fakecodex.NewVirtualNetwork()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := network.Close(); err != nil {
			t.Errorf("close network: %v", err)
		}
	})
	return network
}

func newPeer(t *testing.T, network *fakecodex.VirtualNetwork) *codexrtc.Peer {
	t.Helper()
	peer, err := codexrtc.NewPeer(codexrtc.PeerConfig{SettingEngine: network.Client})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := peer.Close(); err != nil {
			t.Errorf("close peer: %v", err)
		}
	})
	return peer
}

// sendTone writes toneFrames frames of a sine and returns once all are sent.
func sendTone(ctx context.Context, peer *codexrtc.Peer, hz float64) error {
	for i := range toneFrames {
		if err := peer.WriteFrame(ctx, tone(hz, i)); err != nil {
			return err
		}
	}
	return nil
}

// loudestFrame reads frames until one is audible.
func loudestFrame(ctx context.Context, peer *codexrtc.Peer) (float64, error) {
	loudest := 0.0
	for range toneFrames {
		frame, err := peer.ReadFrame(ctx)
		if err != nil {
			return loudest, err
		}
		if len(frame) != codexrtc.FrameSamples {
			return loudest, errors.New("decoded frame has the wrong length")
		}
		loudest = max(loudest, codec.RMS(frame))
		if loudest >= minAudibleRMS {
			return loudest, nil
		}
	}
	return loudest, nil
}

// The full route against the fake backend: the offer goes in the JSON call
// request, an in-process pion peer answers, the answer is applied, and PCM
// flows both ways through Opus over a virtual network.
func TestLoopbackCallCarriesPCMBothWaysThroughOpus(t *testing.T) {
	network := newNetwork(t)
	h := newHarness(t, fixedCredential(), fakecodex.WithPeerNetwork(network))
	ctx := deadline(t)
	peer := newPeer(t, network)
	offer, err := peer.CreateOffer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// OpenClaw realtime-quicksilver-media.runtime.ts:86-91: Opus on payload
	// type 111 in one send-and-receive audio section; no data channel.
	for _, line := range []string{"m=audio", "a=rtpmap:111 opus/48000/2", "a=sendrecv"} {
		if !strings.Contains(offer, line) {
			t.Fatalf("offer lacks %q:\n%s", line, offer)
		}
	}
	if strings.Contains(offer, "m=application") || strings.Contains(offer, "m=video") {
		t.Fatalf("offer has more than audio:\n%s", offer)
	}
	call := h.create(t, ctx, offer)
	if err := peer.ApplyAnswer(call.AnswerSDP); err != nil {
		t.Fatal(err)
	}
	remote := h.backend.Peer()
	for name, side := range map[string]*codexrtc.Peer{"client": peer, "backend": remote} {
		if err := side.WaitConnected(ctx); err != nil {
			t.Fatalf("%s connect: %v", name, err)
		}
	}

	sent := make(chan error, 2)
	go func() { sent <- sendTone(ctx, peer, 440) }()
	go func() { sent <- sendTone(ctx, remote, 660) }()
	for name, receiver := range map[string]*codexrtc.Peer{"backend": remote, "client": peer} {
		loudest, err := loudestFrame(ctx, receiver)
		if err != nil || loudest < minAudibleRMS {
			t.Fatalf("%s heard RMS %.0f, err %v", name, loudest, err)
		}
	}
	for range 2 {
		if err := <-sent; err != nil {
			t.Fatalf("send: %v", err)
		}
	}
}

func TestClosedPeerRefusesEveryOperation(t *testing.T) {
	network := newNetwork(t)
	peer := newPeer(t, network)
	if err := peer.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := deadline(t)
	if err := peer.WriteFrame(ctx, tone(440, 0)); !errors.Is(err, codexrtc.ErrPeerClosed) {
		t.Fatalf("write: %v", err)
	}
	if _, err := peer.ReadFrame(ctx); !errors.Is(err, codexrtc.ErrPeerClosed) {
		t.Fatalf("read: %v", err)
	}
	if err := peer.WaitConnected(ctx); !errors.Is(err, codexrtc.ErrPeerClosed) {
		t.Fatalf("wait: %v", err)
	}
	if _, err := peer.CreateOffer(ctx); err == nil {
		t.Fatal("offer on a closed peer")
	}
	if err := peer.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestPeerRejectsBadInputWithoutConnecting(t *testing.T) {
	network := newNetwork(t)
	peer := newPeer(t, network)
	ctx := deadline(t)
	if err := peer.ApplyAnswer("v=0\r\n"); !errors.Is(err, codexrtc.ErrBadCallResponse) {
		t.Fatalf("answer without audio: %v", err)
	}
	if err := peer.ApplyAnswer(answerSDP); err == nil {
		t.Fatal("answer applied before an offer")
	}
	if err := peer.WriteFrame(ctx, make([]int16, codexrtc.FrameSamples-1)); !errors.Is(err, codec.ErrOpusInvalidPCM) {
		t.Fatalf("short frame: %v", err)
	}
	if _, err := peer.Answer(ctx, "not sdp"); err == nil {
		t.Fatal("garbage offer answered")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := peer.WaitConnected(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait: %v", err)
	}
	if _, err := peer.ReadFrame(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("read: %v", err)
	}
	if _, err := codexrtc.NewPeer(codexrtc.PeerConfig{Opus: codec.OpusCodecConfig{Bitrate: 1}}); !errors.Is(err, codec.ErrOpusInvalidConfiguration) {
		t.Fatalf("bad bitrate: %v", err)
	}
}
