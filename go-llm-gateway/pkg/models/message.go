package models

import "github.com/portpowered/go-agent-loop/pkg/messages"

// Compatibility aliases for the loop-owned shared message contract.
// Keep pkg/models as the stable gateway import path, but treat
// go-agent-loop/pkg/messages as the authoritative owner for these shapes.

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

// Role constants re-export the loop-owned role vocabulary for convenience.
const (
	RoleUser      = messages.RoleUser
	RoleAssistant = messages.RoleAssistant
	RoleTool      = messages.RoleTool
	RoleSystem    = messages.RoleSystem
)

// NewTextMessage forwards to the loop-owned shared message constructor.
var NewTextMessage = messages.NewTextMessage
