package models

import "github.com/portpowered/go-agent-loop/pkg/messages"

// Re-export message and tool types from go-agent-loop so the gateway uses a single source of truth.
// All gateway code should use these aliases; providers convert to/from provider-specific APIs.

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

// Role constants (re-exported for convenience).
const (
	RoleUser      = messages.RoleUser
	RoleAssistant = messages.RoleAssistant
	RoleTool      = messages.RoleTool
	RoleSystem    = messages.RoleSystem
)

// NewTextMessage builds a message with a single text part (re-exported from go-agent-loop).
var NewTextMessage = messages.NewTextMessage
