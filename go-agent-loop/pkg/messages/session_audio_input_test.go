package messages

import "testing"

func TestSessionAudioInputPolicyInterruptsResponseDefaultsSafely(t *testing.T) {
	tests := []struct {
		name   string
		policy SessionAudioInputPolicy
		want   bool
	}{
		{name: "default", policy: SessionAudioInputPolicyDefault, want: true},
		{name: "explicit interrupt", policy: SessionAudioInputPolicyInterrupt, want: true},
		{name: "peer agent", policy: SessionAudioInputPolicyDoNotInterrupt, want: false},
		{name: "unknown", policy: SessionAudioInputPolicy("future-policy"), want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.policy.InterruptsResponse(); got != test.want {
				t.Fatalf("InterruptsResponse(%q) = %t, want %t", test.policy, got, test.want)
			}
		})
	}
}
