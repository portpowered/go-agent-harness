//go:build live

package localai

import "testing"

const localAIComposeStartCommand = "docker compose -f deploy/localai/docker-compose.yml up -d"

// TestLocalAIRealtimeAudio runs only with the live build tag: the LocalAI
// fixture must be running and must complete a real audio round trip.
func TestLocalAIRealtimeAudio(t *testing.T) {
	wsURL, ok := NewProber().Endpoint(t)
	if !ok {
		t.Fatalf("LocalAI realtime endpoint %s is unavailable; start it with: %s", wsURL, localAIComposeStartCommand)
	}

	proof, err := verifyRealtimeAudio(t.Context(), wsURL)
	if err != nil {
		t.Fatalf("LocalAI realtime audio proof failed for %s: %v", wsURL, err)
	}

	t.Logf("LocalAI realtime audio proof endpoint=%s session.created=true audio_deltas=%d pcm_bytes=%d rms=%.6f threshold=%.6f first_audio=%s total=%s", wsURL, proof.AudioDeltaCount, proof.AudioBytes, proof.AudioRMS, pcmSilenceRMSThreshold, proof.FirstAudioLatency, proof.TotalDuration)
}
