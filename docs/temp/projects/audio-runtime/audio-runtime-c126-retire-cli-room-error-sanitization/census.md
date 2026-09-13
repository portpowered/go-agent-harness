# C126 pre-edit room-error census

The admitted source at `09c70f51243caeaf1184c4806b99bbf7749e3044` was 114
physical lines and SHA-256
`7547e6b2c7a77ea34aeea3029221a26145a5269e65704fe1d74bd7244d25b750`.

## Policy symbols

The source-defined policy symbols were:

- `roomParticipantFailure` — reusable runtime behavior; exact static callers in
  `session_room_coordinator.go`, `session_room_orchestration.go`,
  `session_room_planning.go`, and `session_room_run.go`.
- `roomParticipantFailureReason` — reusable runtime behavior; exact static
  callers at coordinator participant-failure notifications.
- `roomParticipantFailureCause` — reusable runtime behavior used by the reason
  projection.
- `roomFailureResult` — reusable runtime behavior; exact static callers in
  room orchestration failure exits.
- `roomSafeError.Error` and `roomSafeError.Unwrap` — dynamic/interface error
  projections; no direct static caller was inferred, but interface dispatch,
  function values, and reflection remain explicit uncertainty.
- `sanitizeRoomError` — reusable runtime behavior; exact static callers in
  coordinator, evidence, orchestration, and run.
- `secretsForPlan` — reusable runtime behavior; exact static callers in
  coordinator, orchestration, planning, and run.

All exact line/column edges remain source-pinned in the accepted C107
`analysis/inventory.md` and `candidates.json`; no dynamic edge was promoted to
an inferred caller. C126 changes only the five named caller files plus the
explicit adapter and coordinator identity projection. C124 lifecycle/tracked
session paths and active C109-C112/C115-C119 paths remain excluded.

## Candidate-independent oracles

- `errors.Is` and `errors.As` reach the original wrapped cause and typed cause;
  participant identity is separately observable.
- Supplied, nested, marker-form, and close-reason credentials never appear in
  diagnostics or failed-room results.
- Cause, close reason, transport disconnect, participant disconnect, and
  participant failure use deterministic precedence.
- First failure remains latched; duplicate/late observations do not replace it.
- Failed-room results have both failed reason fields and a non-nil empty
  participant map.
- GOWORK=off consumer constructs two Wire services and proves isolation.
- Existing room evidence and bounded-shutdown tests remain unchanged.
