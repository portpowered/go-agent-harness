// Package selfplay defines the application-facing self-play service boundary.
//
// The command configuration is intentionally value-only. Provider session
// factories, inferencers, and other runtime seams remain private to the
// services implementation and its test-support package.
package selfplay

import (
	"time"

	runtimeselfplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
)

const (
	// SelfPlayDefaultProvider is the provider enabled by the Phase 1 command.
	SelfPlayDefaultProvider = "openai"
	// SelfPlayDefaultModel is the default OpenAI Realtime model for self-play.
	SelfPlayDefaultModel = "gpt-realtime"
	// SelfPlayDefaultMaxDuration bounds an invocation that omits the flag.
	SelfPlayDefaultMaxDuration = 2 * time.Minute
	// SelfPlayDefaultTurnTarget is the default completed-turn target per side.
	SelfPlayDefaultTurnTarget = 3

	// SelfPlayCustomerPersona and SelfPlayAssistantPersona are the fixed Phase
	// 1 prompts shown by the CLI and supplied to the private runtime.
	SelfPlayCustomerPersona  = "You are the customer. Speak naturally, briefly, and only as part of a spoken conversation. Ask one practical follow-up at a time. Do not call tools."
	SelfPlayAssistantPersona = "You are the helpful assistant. Speak naturally, briefly, and only as part of a spoken conversation. Answer the customer's latest request and ask one concise follow-up when useful. Do not call tools."
	// SelfPlayOpeningSeed is sent once as the customer-side text seed.
	SelfPlayOpeningSeed = "Hi, I need help planning a simple weekend trip."
)

// Service, Request, and Result come from the runtime-owned self-play contract.
// The aliases keep the unreleased CLI caller seams source-compatible while
// their owner completes its exact-path cutover.
type Service = runtimeselfplay.Service
type Request = runtimeselfplay.Request
type Result = runtimeselfplay.Result
