package session

import sessioninstructions "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessioninstructions"

// The session package keeps source compatibility for hosts that adopted the
// C41 contract. Policy ownership now lives in services/sessioninstructions;
// these aliases intentionally contain no second implementation.
type InstructionLoader = sessioninstructions.InstructionLoader
type ContextAwareInstructionLoader = sessioninstructions.ContextAwareInstructionLoader
type InstructionRequest = sessioninstructions.InstructionRequest
type InstructionResult = sessioninstructions.InstructionResult
type BrowserCapabilityState = sessioninstructions.BrowserCapabilityState
type InstructionComposition = sessioninstructions.InstructionComposition
type InstructionService = sessioninstructions.InstructionService

const BrowserCapabilityConnectedUnselected = sessioninstructions.BrowserCapabilityConnectedUnselected
