package wire

import (
	"testing"
	"time"

	duration "github.com/portpowered/go-agent-harness/go-agent-runtime/services/duration"
	clockpkg "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type sourceOnly struct{}

func (sourceOnly) Now() time.Time { return time.Unix(0, 0) }

func TestGeneratedDurationInjectors(t *testing.T) {
	if NewService(clockpkg.Real{}) == nil || NewServiceWithClock(clockpkg.Real{}) == nil {
		t.Fatal("generated duration injector returned nil service")
	}
	if NewService(sourceOnly{}) == nil {
		t.Fatal("source-only duration injector returned nil service")
	}
	var _ duration.Service = NewService(clockpkg.Real{})
}
