package functional

import "github.com/portpowered/go-agent-harness/go-agent-loop/test/functional/internal/sessionmock"

// MockSessionInferencer remains available from the historical functional
// package path for cross-module functional consumers.
type MockSessionInferencer = sessionmock.Inferencer

// NewMockSessionInferencer remains available from the historical functional
// package path for cross-module functional consumers.
var NewMockSessionInferencer = sessionmock.NewInferencer
