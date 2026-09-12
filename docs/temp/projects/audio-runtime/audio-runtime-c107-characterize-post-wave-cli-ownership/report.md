# C107 candidate report

Accepted-main source: `d4766c3dbbf2c198142047ead4449d58dd47d485`

These are ordered, pairwise-disjoint future retirement candidates. C107 has no implementation lease and does not edit their source, destination, Wire registry, or architecture baseline.

## 1. session-terminal-diagnostics

Writer paths: `agent-cli/internal/services/internal/agentruntime/session_diagnostics_terminal.go`
Baseline: [{'path': 'agent-cli/internal/services/internal/agentruntime/session_diagnostics_terminal.go', 'physical_lines': 287, 'bytes': 11824}]
Retirement floor: 1 file(s), 180 physical line(s). New runtime/tests/evidence and wrappers receive no credit.
Public workflow/effects: credential-free yui session replay and cancellation/error workflows that publish one terminal diagnostic and metrics record; retain terminal precedence, provider/error classification, output-state semantics, unresolved continuation metadata, cancellation identity and final token/audio accounting
Destination: public `go-agent-runtime/services/sessionterminal/contract.go: terminal classification, output state, cancellation and metrics projections`; private `go-agent-runtime/services/sessionterminal/internal/service/: terminal precedence, failure facts, continuation metadata and final accounting`; Wire `go-agent-runtime/services/sessionterminal/wire/: dedicated terminal-observation construction with typed diagnostics and clock-independent counters`.
Negative control: drop the final failure or mutate cancellation/output-state precedence; the bounded workflow must reject missing terminal evidence and never silently publish success
Open criteria: SERVICE, TRACE, REPLAY, FAILURES, QUALITY, PARITY

Exact current symbols and callers are in `candidates.json` and the pinned inventory.

## 2. session-failure-projection

Writer paths: `agent-cli/internal/services/internal/agentruntime/session_diagnostics_failure.go`
Baseline: [{'path': 'agent-cli/internal/services/internal/agentruntime/session_diagnostics_failure.go', 'physical_lines': 246, 'bytes': 8306}]
Retirement floor: 1 file(s), 160 physical line(s). New runtime/tests/evidence and wrappers receive no credit.
Public workflow/effects: credential-free yui session replay and provider/session failure workflows that publish typed terminal evidence; retain failure classification, terminal reason/provenance, output state, cancellation boundaries and the original provider error
Destination: public `go-agent-runtime/services/sessionfailure/contract.go: typed failure facts, terminal reason and output-state projection`; private `go-agent-runtime/services/sessionfailure/internal/service/: provider/session failure normalization and cancellation-safe publication`; Wire `go-agent-runtime/services/sessionfailure/wire/: dedicated constructor for failure projection and diagnostic sinks`.
Negative control: drop terminal failure facts or convert cancellation into failure; the bounded workflow must reject missing/error-classified terminal evidence
Open criteria: SERVICE, TRACE, REPLAY, FAILURES, QUALITY, PARITY

Exact current symbols and callers are in `candidates.json` and the pinned inventory.

## 3. session-duration-terminal-boundary

Writer paths: `agent-cli/internal/services/internal/agentruntime/session_duration_terminal.go`
Baseline: [{'path': 'agent-cli/internal/services/internal/agentruntime/session_duration_terminal.go', 'physical_lines': 131, 'bytes': 3970}]
Retirement floor: 1 file(s), 90 physical line(s). New runtime/tests/evidence and wrappers receive no credit.
Public workflow/effects: credential-free yui session --max-duration replay and bounded provider-close workflows; preserve provider-terminal versus loop-close precedence, output-state projection, terminal replay artifacts and joined lifecycle errors
Destination: public `go-agent-runtime/services/sessionduration/contract.go: bounded terminal admission, output state and lifecycle result`; private `go-agent-runtime/services/sessionduration/internal/service/: provider-terminal observation, max-duration close and transport errors`; Wire `go-agent-runtime/services/sessionduration/wire/: duration terminal coordinator and replay artifact writer`.
Negative control: admit a loop shutdown close as provider evidence or drop the bounded terminal artifact; the replay verifier must fail closed
Open criteria: SERVICE, REPLAY, FAILURES, QUALITY, PARITY

Exact current symbols and callers are in `candidates.json` and the pinned inventory.
