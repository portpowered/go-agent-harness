package wire

import "testing"

func TestNewServiceReturnsPublicService(t *testing.T) {
	if NewService() == nil {
		t.Fatal("NewService() returned nil")
	}
}
