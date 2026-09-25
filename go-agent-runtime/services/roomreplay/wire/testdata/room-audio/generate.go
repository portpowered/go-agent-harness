// Command generate refreshes one committed room-audio replay fixture. The
// generator lives in services/roomreplay/internal/roomaudiofixture, where it
// is linted and tested; this entry point keeps the regeneration command
// recorded in each run-manifest.json stable.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay/internal/roomaudiofixture"
)

func main() {
	output := flag.String("output", "go-agent-runtime/services/roomreplay/wire/testdata/room-audio/clean-turn-taking", "fixture directory to refresh")
	shape := flag.String("shape", roomaudiofixture.ShapeCleanTurnTaking, "fixture shape to generate")
	flag.Parse()
	if err := roomaudiofixture.Generate(*shape, *output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
