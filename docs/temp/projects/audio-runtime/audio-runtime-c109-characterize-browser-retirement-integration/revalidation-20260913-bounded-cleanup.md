# C109 bounded-cleanup review repair — 2026-09-13

The same admitted task was retained after review found that live `origin/main`
had advanced to `09c70f51243caeaf1184c4806b99bbf7749e3044`, conflict abort
results were not retained, caller-owned trees were not reported on exceptions,
and `communicate()` captured child output before applying the 2 MiB cap.

Merge commit `4f913fca8a` integrates the fetched main into the isolated branch.
Repair commit `5eb90be6eeafeb5ea63e3e192ccd0544eed7da0c` changes only the C109
evidence directory. It records `git merge --abort` status/output hashes and
post-abort cleanliness, preserves caller-owned tree identity and pre-existing
status on exception, and streams child output with incremental hashing and
credential scanning. The oversized-output control observed 2,097,153 bytes,
retained 16,384 bytes, failed closed, and left no process group.

Evidence from the repaired source:

- required and reverse analyzer rehearsals: passed; C61/C83 refs and preserved
  worktrees unchanged; current-main compatibility conflicts abort cleanly;
- `test_analyze.py`: 3/3 passed;
- `verify.py --mode all`: all eight checks passed, including determinism, 19
  negative fixtures, caller-tree mutation, and public checks;
- positive public workflow: 18/18, 268.871s under 90/300s;
- malformed/canceled public control: 3/3, 29.092s under 60/180s;
- final evidence source: `5eb90be6eeafeb5ea63e3e192ccd0544eed7da0c`;
- final verification SHA-256:
  `428b51023f41139de5286101084533980de95af8ad768728f8f1b58c111c8f89`.

The evidence remains software-only and does not claim candidate merge,
acceptance, vertical probe, hardware/acoustic proof, or project completion.
