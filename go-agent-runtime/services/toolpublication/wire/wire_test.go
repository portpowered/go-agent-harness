package wire

import "testing"

func TestNewServiceBuildsPrivatePublisher(t *testing.T) {
	if NewService(Dependencies{}) == nil {
		t.Fatal("NewService returned nil")
	}
}
