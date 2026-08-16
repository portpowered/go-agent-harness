package localai

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestVerifyRealtimeAudioRejectsNonSpeakingListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for non-speaking endpoint: %v", err)
	}
	defer func() { _ = listener.Close() }()

	accepted := make(chan net.Conn, 1)
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
	}()

	endpoint := "ws://" + listener.Addr().String() + "/v1/realtime?model=non-speaking"
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, verifyErr := verifyRealtimeAudioContext(ctx, endpoint)
	elapsed := time.Since(started)
	if verifyErr == nil {
		t.Fatal("non-speaking endpoint unexpectedly passed the realtime proof")
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("non-speaking endpoint took %s, want a bounded failure under two seconds: %v", elapsed, verifyErr)
	}

	_ = listener.Close()
	select {
	case conn := <-accepted:
		_ = conn.Close()
	case <-time.After(time.Second):
		t.Fatal("non-speaking listener did not accept the probe")
	}
	<-acceptDone
}

func TestDeterministicPCM16UtteranceIsValidAndNonSilent(t *testing.T) {
	audio := deterministicPCM16Utterance()
	if len(audio) == 0 || len(audio)%2 != 0 {
		t.Fatalf("generated PCM16 byte count = %d, want a non-zero even count", len(audio))
	}
	rms, err := pcm16RMS(audio)
	if err != nil {
		t.Fatalf("pcm16RMS: %v", err)
	}
	if rms <= pcmSilenceRMSThreshold {
		t.Fatalf("generated PCM16 RMS = %.6f, want above %.6f", rms, pcmSilenceRMSThreshold)
	}
}

func TestPCM16RMSRejectsMalformedAudio(t *testing.T) {
	for _, audio := range [][]byte{nil, {0x01}} {
		if _, err := pcm16RMS(audio); err == nil {
			t.Fatalf("pcm16RMS(%v) returned nil error", audio)
		}
	}

	if _, err := pcm16RMS([]byte{0, 0}); err != nil {
		t.Fatalf("pcm16RMS of one valid silent sample: %v", err)
	}
}
