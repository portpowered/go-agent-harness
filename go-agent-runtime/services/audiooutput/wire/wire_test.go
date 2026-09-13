package wire

import "testing"

func TestNewServiceReturnsPublicAudioOutputService(t *testing.T) {
	if NewService() == nil {
		t.Fatal("NewService returned nil")
	}
}
