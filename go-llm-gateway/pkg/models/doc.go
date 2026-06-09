// Package models exposes the public gateway request/response model surface.
//
// Shared message, tool, content-part, and token-usage names in this package
// are compatibility aliases over the authoritative contracts in
// github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages. Gateway consumers may keep
// importing pkg/models for those shared shapes, but compatibility for that
// subset follows the loop-owned contract and does not define an independent
// gateway message vocabulary.
//
// Gateway-owned types in this package are limited to gateway-specific session
// configuration and realtime event shapes such as SessionConfig,
// SessionEvent, and related audio/session enums.
package models
