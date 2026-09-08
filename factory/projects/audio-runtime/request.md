# Audio/runtime consolidation request

Consolidate audio parsing, clocks, sampling, DSP and buffer operations in go-audio.
The agent loop exchanges buffers; it never opens, reads or writes devices.
Keep physical device abstractions in adjacent go-device-gateway. Make the reusable
agent runtime embeddable without CLI dependencies. Expose thin services/X contracts,
private services/X/internal implementations and per-service services/X/wire graphs.
The CLI should be a transport/presentation wrapper.

Record correlated device input, consumed device output, Realtime API send/receive,
tools, interruption and terminal state so customer problems can be diagnosed and
replayed offline. Investigate truncated/no playback, failed barge-in, slowdown in
long conversations, and tool timeout/active-response collisions. Fix demonstrated
causes where feasible; retain reproductions and honest residual limitations.

Destructive internal refactoring is acceptable while supported behavior is preserved.
Keep checkpoint commits. Do not increase architecture debt or weaken tests to pass.

Factory policy: one project at a time; Astra medium for project leadership, planning
and meta planning; Luna max for implementation/review/validation. Routine meta
planning runs every four hours. Preserve project progress and exception handling.
The first implementation slice integrates the pinned refactor baseline plus factory
bootstrap into current main through an isolated PR with independent Luna review/CI.
Baseline integration does not complete this migration project.
