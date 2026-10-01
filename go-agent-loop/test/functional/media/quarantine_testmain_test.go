package media

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/test/functional"
)

func TestMain(m *testing.M) {
	functional.RunPackageTests(context.Background(), m, "github.com/portpowered/go-agent-harness/go-agent-loop/test/functional/media")
}
