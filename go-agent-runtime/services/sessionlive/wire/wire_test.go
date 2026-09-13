//go:build !wireinject

package wire

import "testing"

func TestNewService(t *testing.T) {
	if NewService() == nil {
		t.Fatal("NewService returned nil")
	}
}
