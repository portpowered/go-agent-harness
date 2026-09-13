# C107 candidate report

Accepted-main source: `d4766c3dbbf2c198142047ead4449d58dd47d485`

These are ordered, pairwise-disjoint future retirement candidates. C107 has no implementation lease and does not edit their source, destination, Wire registry, or architecture baseline.

## 1. session-duration-terminal-boundary

Writer paths: `agent-cli/internal/services/internal/agentruntime/session_duration_terminal.go`
Baseline: [{'path': 'agent-cli/internal/services/internal/agentruntime/session_duration_terminal.go', 'physical_lines': 131, 'bytes': 3970}]
Retirement floor: 1 file(s), 90 physical line(s). New runtime/tests/evidence and wrappers receive no credit.
Public workflow/effects: credential-free yui session --max-duration replay and bounded provider-close workflows; preserve provider-terminal versus loop-close precedence, output-state projection, terminal replay artifacts and joined lifecycle errors
Destination: public `go-agent-runtime/services/sessionduration/contract.go: bounded terminal admission, output state and lifecycle result`; private `go-agent-runtime/services/sessionduration/internal/service/: provider-terminal observation, max-duration close and transport errors`; Wire `go-agent-runtime/services/sessionduration/wire/: duration terminal coordinator and replay artifact writer`.
Negative control: admit a loop shutdown close as provider evidence or drop the bounded terminal artifact; the replay verifier must fail closed
Open criteria: SERVICE, REPLAY, FAILURES, QUALITY, PARITY

Exact current symbols and callers are in `candidates.json` and the pinned inventory.

## 2. session-interactive-tool-policy

Writer paths: `agent-cli/internal/services/internal/agentruntime/session_interactive_policy.go`
Baseline: [{'path': 'agent-cli/internal/services/internal/agentruntime/session_interactive_policy.go', 'physical_lines': 155, 'bytes': 6336}]
Retirement floor: 1 file(s), 100 physical line(s). New runtime/tests/evidence and wrappers receive no credit.
Public workflow/effects: credential-free yui tool replay with fast-read and bounded-long-running tool calls; preserve request-scoped tool classes, timeout bounds, acknowledgement thresholds, validation and browser-tool mapping
Destination: public `go-agent-runtime/services/sessioninteractive/contract.go: per-session tool class, timeout and acknowledgement policy`; private `go-agent-runtime/services/sessioninteractive/internal/service/: policy validation, cloning and runtime compatibility conversion`; Wire `go-agent-runtime/services/sessioninteractive/wire/: injected interactive policy construction and browser tool-name mapping`.
Negative control: accept an unbounded or invalid tool policy; the consumer and replay controls must reject it before tool execution
Open criteria: SERVICE, REPLAY, QUALITY, PARITY

Exact current symbols and callers are in `candidates.json` and the pinned inventory.

## 3. session-room-error-sanitization

Writer paths: `agent-cli/internal/services/internal/agentruntime/session_room_errors.go`
Baseline: [{'path': 'agent-cli/internal/services/internal/agentruntime/session_room_errors.go', 'physical_lines': 114, 'bytes': 2960}]
Retirement floor: 1 file(s), 70 physical line(s). New runtime/tests/evidence and wrappers receive no credit.
Public workflow/effects: credential-free multi-participant yui session failure and disconnect replay; retain participant identity and termination reason while sanitizing secrets, preserving error identity and projecting a stable room result
Destination: public `go-agent-runtime/services/sessionroomerrors/contract.go: participant failure identity, sanitized cause and room result`; private `go-agent-runtime/services/sessionroomerrors/internal/service/: secret-aware error unwrapping and termination mapping`; Wire `go-agent-runtime/services/sessionroomerrors/wire/: room failure projection and credential-free diagnostic construction`.
Negative control: publish a raw secret or erase participant failure identity; the room replay verifier must reject the result
Open criteria: SERVICE, FAILURES, QUALITY, PARITY

Exact current symbols and callers are in `candidates.json` and the pinned inventory.
