package tools

import (
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/test/functional"
)

func TestMain(m *testing.M) {
	functional.RunPackageTests(m, "github.com/portpowered/go-agent-harness/go-agent-loop/test/functional/tools")
}
