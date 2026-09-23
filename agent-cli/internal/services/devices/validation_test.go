package devices

import (
	"errors"
	"testing"

	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

func TestSessionAudioDeviceConflictPreservesPublicClassification(t *testing.T) {
	err := ValidateSessionAudioDeviceConflicts(true, false, true, false)
	if err == nil {
		t.Fatal("ValidateSessionAudioDeviceConflicts() = nil, want input selector conflict")
	}

	var conflict *SessionAudioDeviceConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error type = %T, want *SessionAudioDeviceConflictError", err)
	}
	if conflict.FileFlag != "--audio-in" || conflict.DeviceFlag != "--audio-in-device" {
		t.Fatalf("conflict flags = (%q, %q)", conflict.FileFlag, conflict.DeviceFlag)
	}
	if got, want := err.Error(), ErrSessionAudioInputConflict.Error(); got != want {
		t.Fatalf("error text = %q, want %q", got, want)
	}
	if !errors.Is(err, ErrSessionAudioInputConflict) {
		t.Fatal("conflict does not retain ErrSessionAudioInputConflict identity")
	}

	var selectionConflict *devicegw.DeviceSelectionConflictError
	if !errors.As(err, &selectionConflict) {
		t.Fatalf("error does not expose shared selection conflict classification: %v", err)
	}
	if selectionConflict.FileOption != "--audio-in" || selectionConflict.DeviceOption != "--audio-in-device" {
		t.Fatalf("shared conflict flags = (%q, %q)", selectionConflict.FileOption, selectionConflict.DeviceOption)
	}
}
