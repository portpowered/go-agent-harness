# C107 candidate report

Accepted-main source: `d4766c3dbbf2c198142047ead4449d58dd47d485`

These are ordered, pairwise-disjoint future retirement candidates. C107 has no implementation lease and does not edit their source, destination, Wire registry, or architecture baseline.

## 1. session-instruction-resolution

Writer paths: `agent-cli/internal/services/internal/agentruntime/session_instructions.go`
Baseline: [{'path': 'agent-cli/internal/services/internal/agentruntime/session_instructions.go', 'physical_lines': 277, 'bytes': 9971}]
Retirement floor: 1 file(s), 190 physical line(s). New runtime/tests/evidence and wrappers receive no credit.
Public workflow/effects: yui session instruction/text-seed admission and the existing RunSessionWithInstructions* path; preserve resolved prompt text, tool grounding, loader errors, provider selection and the existing no-credential CLI output
Destination: public `go-agent-runtime/services/sessioninstructions/contract.go: normalized instruction request, loader capability and composition result`; private `go-agent-runtime/services/sessioninstructions/internal/service/: filesystem/config resolution, bounded composition and error attribution`; Wire `go-agent-runtime/services/sessioninstructions/wire/: dedicated constructor for the private service and existing injected loader/config dependencies`.
Negative control: malformed/oversized instruction document and missing loader capability must fail before provider construction; no fallback to an unvalidated prompt
Open criteria: SERVICE, QUALITY, EMBED, PARITY

Exact current symbols and callers are in `candidates.json` and the pinned inventory.

## 2. session-tool-continuation-lifecycle

Writer paths: `agent-cli/internal/services/internal/agentruntime/session_tool_lifecycle.go`
Baseline: [{'path': 'agent-cli/internal/services/internal/agentruntime/session_tool_lifecycle.go', 'physical_lines': 147, 'bytes': 5696}]
Retirement floor: 1 file(s), 90 physical line(s). New runtime/tests/evidence and wrappers receive no credit.
Public workflow/effects: credential-free tool continuation and scheduled/image response lifecycle through yui session replay; retain typed unresolved-result/continuation errors, exact call IDs/statuses, stable metadata ordering and audio-output error identity
Destination: public `go-agent-runtime/services/sessioncontinuation/contract.go: typed continuation obligations, statuses and terminal error identity`; private `go-agent-runtime/services/sessioncontinuation/internal/service/: pending tool/image state, deterministic metadata and terminal joins`; Wire `go-agent-runtime/services/sessioncontinuation/wire/: dedicated service construction with session observation dependencies`.
Negative control: omit a required continuation or corrupt one audio delta; the workflow must fail boundedly with the typed obligation and never silently complete
Open criteria: SERVICE, REPLAY, FAILURES, QUALITY, PARITY

Exact current symbols and callers are in `candidates.json` and the pinned inventory.

## 3. session-trace-evidence

Writer paths: `agent-cli/internal/services/internal/agentruntime/trace.go`
Baseline: [{'path': 'agent-cli/internal/services/internal/agentruntime/trace.go', 'physical_lines': 119, 'bytes': 3888}]
Retirement floor: 1 file(s), 70 physical line(s). New runtime/tests/evidence and wrappers receive no credit.
Public workflow/effects: yui session --record-dir/--trace-audio and the existing credential-free audio/tool replay record; preserve microphone/provider/rendered trace events, credential redaction, atomic destination attachment and retained failure evidence
Destination: public `go-agent-runtime/services/sessiontrace/contract.go: bounded trace request, clock source and publication result`; private `go-agent-runtime/services/sessiontrace/internal/service/: capture taps, credential redaction and atomic trace attachment`; Wire `go-agent-runtime/services/sessiontrace/wire/: dedicated constructor for the private trace service and clock/device observers`.
Negative control: nil clock, duplicate trace destination or same-length trace mutation must fail closed before publication and retain the original error
Open criteria: TRACE, REPLAY, SERVICE, QUALITY, PARITY

Exact current symbols and callers are in `candidates.json` and the pinned inventory.
