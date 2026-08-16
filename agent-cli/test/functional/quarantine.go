package functional

import (
	"embed"
	"fmt"

	loopfunctional "github.com/portpowered/go-agent-harness/go-agent-loop/test/functional"
)

//go:embed functional-quarantine.json
var manifestFiles embed.FS

// ReadFunctionalQuarantine consumes the manifest committed with the
// agent-cli functional-test tree. The tree has no executable concern package
// yet, so its current empty inventory is validated by the contract test.
func ReadFunctionalQuarantine() (loopfunctional.Manifest, error) {
	body, err := manifestFiles.ReadFile("functional-quarantine.json")
	if err != nil {
		return loopfunctional.Manifest{}, fmt.Errorf("read embedded agent-cli functional quarantine: %w", err)
	}
	return loopfunctional.ParseManifest(body)
}
