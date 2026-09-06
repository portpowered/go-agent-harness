# Private runtime compatibility patches

These patches apply to the pinned reference revision in `scripts/build-runtime.py`.
The builder applies them to a clean Git archive and records their hashes alongside
the executable hash. The reference checkout and global `you` remain unchanged.

`0001-current-placement-precedes-history.patch` fixes recovery after an operator
moves existing Work and a later dispatch moves that same Work again. Restoration
previously treated the historical operator destination as another current place,
rejecting a valid recording. Current occupancy and active dispatch inputs now take
precedence; historical state changes remain fallback evidence. Conflicting actual
occupancy still fails validation.

Validation includes the reference runtime package and the harness's native
mock-worker smoke: delivery, review rejection, blocked escalation, stop/resume,
operator correction, and another stop/resume without duplicating project Work.
Remove this patch when the pinned upstream runtime contains an equivalent fix.

`0002-retain-consumed-work-as-history.patch` fixes replay of accepted transitions
that consume an input without emitting it again. The projection retained those
inputs as active despite their removal from the board, preventing resume after
project-cycle recovery. Explicit accepted output sets now retire omitted input
IDs from the active index while retaining their Work and completion history.
Legacy events without an explicit output set keep their existing behavior.
