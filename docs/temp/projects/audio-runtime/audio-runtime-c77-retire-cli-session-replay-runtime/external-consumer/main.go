package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/wire"
)

type consumerReport struct {
	Schema         string `json:"schema"`
	ConstructedVia string `json:"constructed_via"`
}

func run() (consumerReport, error) {
	if wire.NewService() == nil {
		return consumerReport{}, fmt.Errorf("replay Wire returned a nil service")
	}
	return consumerReport{
		Schema:         "audio-runtime-c77-replay-consumer/v1",
		ConstructedVia: "replay/wire.NewService",
	}, nil
}

func main() {
	result, err := run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "C77 consumer failure: %v\n", err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintf(os.Stderr, "C77 consumer report: %v\n", err)
		os.Exit(1)
	}
}
