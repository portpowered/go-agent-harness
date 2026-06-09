package models

import "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"

// Loop-owned shared-contract aliases.
//
// These names exist as a compatibility facade for gateway consumers that
// already import pkg/models. The authoritative message and tool contract lives
// in go-agent-loop/pkg/messages.
type Role = messages.Role
type ToolCall = messages.ToolCall
type ContentPart = messages.ContentPart
type TextPart = messages.TextPart
type ImagePart = messages.ImagePart
type AudioPart = messages.AudioPart
type VideoPart = messages.VideoPart
type EmbeddingPart = messages.EmbeddingPart
type Message = messages.Message
type ToolDefinition = messages.ToolDefinition
type ToolParameter = messages.ToolParameter
type TokenUsage = messages.TokenUsage

// Role constants re-exported for compatibility and convenience.
const (
	RoleUser      = messages.RoleUser
	RoleAssistant = messages.RoleAssistant
	RoleTool      = messages.RoleTool
	RoleSystem    = messages.RoleSystem
)

// NewTextMessage re-exports the loop-owned helper for building a text message.
var NewTextMessage = messages.NewTextMessage
