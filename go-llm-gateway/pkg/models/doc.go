// Package models exposes the public gateway request/response model surface.
//
// Message, tool, and token-usage names in this package are compatibility
// aliases over github.com/portpowered/go-agent-loop/pkg/messages. They follow
// the loop-owned shared contract and do not define an independent gateway
// message vocabulary.
//
// Gateway-owned types in this package are limited to gateway-specific session
// configuration and realtime event shapes such as SessionConfig,
// SessionEvent, and related audio/session enums.
package models
