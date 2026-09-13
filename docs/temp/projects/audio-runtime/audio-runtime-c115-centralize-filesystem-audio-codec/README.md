# C115 filesystem audio codec handoff

This directory is the executor-owned evidence for
`audio-runtime-c115-centralize-filesystem-audio-codec`. The implementation
checkpoint injects the composed runtime tool service into the shipped CLI and
room/session capability paths; the private codec now reports temporary-file
cleanup failures and terminates its decoder as soon as bounded stdout or
stderr overflows.

Run the bounded evidence driver from the repository root:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c115-centralize-filesystem-audio-codec/run.py \
  --case shipped-audio-tool \
  --case malformed-truncated \
  --case fake-output-overflow \
  --case c21-consumption-replay \
  --case c50-public-replay \
  --child-timeout 60 \
  --aggregate-timeout 360
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c115-centralize-filesystem-audio-codec/verify.py \
  --mode public-and-accumulated-regressions
```

The run builds `artifacts/yui` with `-tags=nomicrophone -trimpath`, records its
hash and every child process under a unique `runs/<timestamp>-<pid>/` directory,
and writes `runs/latest.json`. The shipped cases prove a valid WAV read and a
truncated-WAV diagnostic through the public `yui tool read_file` command. The
C21 case also builds the external consumer with `GOWORK=off`, runs the C21
consumption boundary tests, and replays the pinned credential-free fixture.
The C50 case checks public help/tool discovery and the same offline replay
surface. No live provider, credential, physical device, or acoustic proof is
used or claimed.

`verify.py` is fail-closed on admission, branch, current-head identity,
`origin/main` ancestry, artifact hash, process-group cleanup, the accumulated
regressions, and the explicit production-service-construction audit. It is an
executor handoff only: script CI, independent review, guarded merge, and the
post-merge vertical probe remain separate gates.
