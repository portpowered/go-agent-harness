package audiooutput

import "testing"

func TestErrorCodesHaveStableText(t *testing.T) {
	if ErrInvalidConfig.Error() != "invalid audio output configuration" {
		t.Fatalf("ErrInvalidConfig = %q", ErrInvalidConfig)
	}
	if ErrDeviceObserverUnavailable.Error() != "audio output device observer is unavailable" {
		t.Fatalf("ErrDeviceObserverUnavailable = %q", ErrDeviceObserverUnavailable)
	}
}
