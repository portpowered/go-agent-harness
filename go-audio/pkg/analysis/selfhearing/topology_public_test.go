package selfhearing

import "testing"

func TestEnablesPCM16SelfHearingOnlyForPairedLiveDevices(t *testing.T) {
	paired := PCM16SelfHearingTopology{LiveMicrophone: true, LiveSpeaker: true}
	if !paired.EnablesPCM16SelfHearing() {
		t.Fatal("paired live microphone and speaker do not enable self-hearing")
	}
	for _, bypass := range []PCM16SelfHearingTopology{
		{LiveMicrophone: true},
		{LiveMicrophone: true, LiveSpeaker: true, Replay: true},
		{LiveMicrophone: true, LiveSpeaker: true, RoomPeerIngress: true},
	} {
		if bypass.EnablesPCM16SelfHearing() {
			t.Fatalf("topology %#v enables self-hearing, want bypass", bypass)
		}
	}
}
