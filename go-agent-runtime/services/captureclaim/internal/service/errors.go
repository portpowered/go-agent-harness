package service

import (
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/captureclaim"
)

func unavailable(path, operation string, err error) error {
	return &captureclaim.ClaimError{Path: path, Err: fmt.Errorf("%s: %w", operation, err)}
}

func occupied(path string, err error) error {
	return &captureclaim.ClaimError{Kind: captureclaim.ErrDestinationOccupied, Path: path, Err: err}
}

func lost(path string, err error) error {
	return &captureclaim.ClaimError{Kind: captureclaim.ErrClaimLost, Path: path, Err: err}
}
