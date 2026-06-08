// Package messages defines the authoritative shared runtime contracts for
// cross-library composition in Phase 3.
//
// The go-agent-loop module owns the shared vocabulary that other modules build
// against: conversation messages, streaming events, tool payloads, token-usage
// reporting, inference request and response types, and the session interfaces
// used by long-running bidirectional transports.
//
// This package is the deliberate boundary between the reusable loop and
// external inference implementations. Packages such as go-llm-gateway may
// adapt provider behavior into these contracts or re-export compatibility
// aliases, but they do not own an independent shared message or session core in
// this phase.
package messages
