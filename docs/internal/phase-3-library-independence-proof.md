# Phase 3 Library Independence Proof

This note is the reviewer-facing proof guide for the
`phase-3-library-independence-proof` slice. It cites only the focused consumer
proofs for library independence and follows the dependency model in
[`docs/architecture/dependencies.md`](../architecture/dependencies.md).

## Loop Consumer Proof

- `checked package`: `go-agent-loop/test/functional`
- `focused test`: `TestConsumerCanUseLoopWithLocalInferencer`
- `command`: `(cd go-agent-loop && go test ./test/functional -run TestConsumerCanUseLoopWithLocalInferencer -count=1)`
- `expected pass condition`: the test constructs `go-agent-loop/pkg/agentloop`
  with a local implementation of the `go-agent-loop/pkg/messages` inferencer
  contract, executes one deterministic user turn, observes the expected
  assistant response and conversation history, and rejects dependencies under
  `github.com/portpowered/go-llm-gateway/pkg/providers/...`.

This proof advances `P3-CORE-03` by showing that a loop consumer can import and
exercise the public loop API without any gateway provider-package imports.

## Gateway Consumer Proof

- `checked package`: `go-llm-gateway/test/functional`
- `focused test`: `TestGatewayConsumerUsesOnlySharedLoopContract`
- `command`: `(cd go-llm-gateway && go test ./test/functional -run TestGatewayConsumerUsesOnlySharedLoopContract -count=1)`
- `expected pass condition`: the test constructs `go-llm-gateway/pkg/gateway`
  with a local `go-llm-gateway/pkg/providers.Provider`, observes deterministic
  non-streaming and streaming responses, allows only
  `github.com/portpowered/go-agent-loop/pkg/messages` as the shared loop
  package, and rejects non-contract loop runtime packages.

The forbidden non-contract loop runtime dependency class includes packages
under `github.com/portpowered/go-agent-loop/pkg/...` other than
`github.com/portpowered/go-agent-loop/pkg/messages`, including:

- `github.com/portpowered/go-agent-loop/pkg/agentloop`
- `github.com/portpowered/go-agent-loop/pkg/engine`
- `github.com/portpowered/go-agent-loop/pkg/participants`
- `github.com/portpowered/go-agent-loop/pkg/logging`

This proof advances `P3-CORE-04` by showing that gateway consumers can use the
gateway/provider entrypoints without depending on loop runtime packages beyond
the deliberate shared message contract.

## Review Mapping

- `P3-CORE-03`: cite the loop consumer proof command and its rejection of
  `go-llm-gateway/pkg/providers/...` dependencies.
- `P3-CORE-04`: cite the gateway consumer proof command and its allowance of
  only `go-agent-loop/pkg/messages` from loop-owned packages.
- `P3-GATE-01`: cite both proof commands together when reviewing whether the
  Phase 3 independence evidence is deterministic, local, and scoped to
  consumer import/use behavior.

Both proof commands use local stubs, require no live provider credentials, and
make no provider network calls.
