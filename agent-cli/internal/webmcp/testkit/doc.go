// Package testkit contains deterministic, semantic WebMCP runtime fakes.
//
// The fakes model browser targets and WebMCP events without importing Chrome,
// CDP, a provider, or a websocket implementation. They are intended for
// broker and integration tests that need observable ordering and lifecycle
// behavior.
package testkit
