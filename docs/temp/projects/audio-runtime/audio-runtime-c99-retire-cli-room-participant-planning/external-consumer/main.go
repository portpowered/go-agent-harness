// Command external-consumer proves the roomplanning public boundary from a
// separate module. It imports no agent-cli package or internal implementation.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomplanning"
	roomplanningwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomplanning/wire"
)

func plan(service roomplanning.Service, root string) error {
	result, err := service.Plan(context.Background(), roomplanning.Options{
		Filesystem: &roomplanning.FilesystemScope{PrimaryRoot: root},
	})
	if err != nil {
		return err
	}
	if len(result.Plans) != 0 {
		return fmt.Errorf("empty consumer manifest produced %d plans", len(result.Plans))
	}
	return nil
}

func main() {
	root, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	first := roomplanningwire.NewService(roomplanningwire.Dependencies{})
	second := roomplanningwire.NewService(roomplanningwire.Dependencies{})
	if err := plan(first, root); err != nil {
		panic(fmt.Errorf("first service: %w", err))
	}
	if err := plan(second, root); err != nil {
		panic(fmt.Errorf("second service: %w", err))
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
		"instances": 2,
		"first":    "ok",
		"second":   "ok",
		"imports":  "public-roomplanning-and-wire-only",
	}); err != nil {
		panic(err)
	}
}
