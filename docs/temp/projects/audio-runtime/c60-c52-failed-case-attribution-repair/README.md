# C60 failed-case attribution repair

This is the owned evidence directory for
`audio-runtime-c60-c52-failed-case-attribution-repair`. It retains the
immutable FAILED C52 corrected report by SHA-256, a clearly labelled identity
fixture for the unavailable independent raw transcript, focused attribution
controls, and a bounded software/file replay report.

The fixture is never used as a matrix result. The fresh matrix remains the
retained C52 runner output and is the sole source for the final
`NON_REPRODUCED` classification. The fixture only proves that the repaired
attribution contract accepts the preserved direct `test_outcome` shape when a
single raw Go failed action is bound to the same revision, run, cell, package,
parent, and subtest, while rejecting the declared identity/checkpoint
mutations.

`verify_replay.py --mode all --child-timeout 59 --aggregate-timeout 1800`
replays the immutable staged artifact-0 with the exact credential-free C16
fixture and configuration, checks marker/continuation, transcripts, terminal
manifest, exact PCM/session-log hashes, and clean process groups, then rejects
a real fixture sequence mutation. It is software/file evidence only.
