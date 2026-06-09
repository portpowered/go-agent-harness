package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/internal/sessionfixturevalidator"
)

func main() {
	if err := sessionfixturevalidator.Run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if !errors.Is(err, sessionfixturevalidator.ErrValidationFailed) {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}
