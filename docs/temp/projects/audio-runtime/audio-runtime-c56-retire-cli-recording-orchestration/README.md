# C56 recording-session extraction evidence

This directory is the task-local evidence surface for
`audio-runtime-c56-retire-cli-recording-orchestration`.

The source identity is `origin/main=904e1f4c3be6c1e629138632573bd2fb55d50938`,
with startup ancestry `8bdafc7f947a3a2c9856220abdc539437035bd21` and the
recording baseline manifest `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`.

`verify.py` contains the bounded source, boundary, artifact, consumer, format,
and scope controls. `run.py` contains the bounded executable record/finalize/
replay, non-recording, and process-group/output negative controls. The shipped
runner uses the immutable credential-free provider capture from C50 only as an
input fixture, records into a fresh directory, and then replays that newly
finalized directory.

The evidence is software/file replay only. It makes no physical-device,
acoustic, realtime-credential, or whole-project completion claim.
