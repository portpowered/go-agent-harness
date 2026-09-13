# C136 session-turn recovery evidence

This directory records the C136 recovery surface for
`audio-runtime-c136-recover-c87-session-turns`. The candidate starts from
accepted `origin/main` `bd6a1289218d1bef1a3af36e64e9d4496062416f` and
transplants only the preserved C87 session-turn implementation from immutable
PR 476/head `28b5a9b18f67a4343ef9e12141ad5e5fc84ef18f`. The historical C87
evidence remains under its original path and is provenance only.

The public `sessionturns` package is host-neutral; state and protocol policy
remain private and construction goes through `sessionturns/wire`. The former
CLI file is a 40-line Deprecated adapter. The standalone consumer is a
separate Go module and is run with `GOWORK=off`.

The focused commands are:

```text
rtk proxy env GOWORK=off go test ./...
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c136-recover-c87-session-turns/verify.py --mode mutations
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c136-recover-c87-session-turns/verify.py --mode retirement-and-adapter
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c136-recover-c87-session-turns/verify.py --mode owned-and-excluded-paths
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c136-recover-c87-session-turns/run.py --case credential-free-audio-tool --child-timeout 60 --aggregate-timeout 300
```

These checks are executor evidence only. They do not claim script CI,
independent review, guarded merge, vertical acceptance, or project
acceptance. Shared Wire/architecture registrations remain deferred until the
active dependency owners release those paths, and C127's accepted repair must
be integrated before final broad CI.

