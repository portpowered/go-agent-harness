// Package models exposes gateway-facing model values.
//
// Shared message, tool, content-part, and token-usage types in this package are
// compatibility aliases over the authoritative contracts in
// go-agent-loop/pkg/messages. Gateway consumers may keep importing pkg/models
// for those shared shapes, but compatibility for that subset follows the loop's
// contract ownership.
//
// Gateway-owned types in this package are limited to gateway concerns such as
// session configuration and session event payloads.
package models
