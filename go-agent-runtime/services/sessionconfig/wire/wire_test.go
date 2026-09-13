package wire

import "testing"

func TestNewServiceBuildsFromExplicitDependencies(t *testing.T) {
	if NewService(Dependencies{}) == nil {
		t.Fatal("NewService returned nil")
	}
}
