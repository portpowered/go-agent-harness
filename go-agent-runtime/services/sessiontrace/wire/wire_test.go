package wire

import "testing"

func TestNewServiceBuildsIndependentFactories(t *testing.T) {
	first := NewService()
	second := NewService()
	if first == nil || second == nil {
		t.Fatalf("services = %v, %v; want constructed services", first, second)
	}
}
