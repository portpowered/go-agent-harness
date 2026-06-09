// Package inference exposes the public bridge from go-llm-gateway runtime
// behavior into the authoritative loop-owned contracts in
// github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages.
//
// GatewayInferencer adapts stateless gateway inference to messages.Inferencer.
// SessionGatewayInferencer adapts gateway session establishment to
// messages.SessionInferencer and returns the loop-owned messages.Session
// boundary contract. This package does not define an independent shared core;
// it is the cross-library adapter seam for composing go-llm-gateway with
// go-agent-loop.
package inference
