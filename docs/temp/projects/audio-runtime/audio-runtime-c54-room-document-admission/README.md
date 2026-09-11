# C54 room-document admission evidence

This directory is the admitted evidence scope for
`audio-runtime-c54-room-document-admission`. The public admission contract is
implemented in `go-agent-runtime/services/rooms` and
`go-agent-runtime/services/rooms/wire`; the CLI room package is a compatibility
adapter.

Run the bounded candidate evidence from the repository root with:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c54-room-document-admission/run.py --case all --child-timeout 60 --aggregate-timeout 600
```

Then run the non-rerunning final gate:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c54-room-document-admission/verify.py --mode final-scope-provenance-and-budget
```

The runner records an external `GOWORK=off` consumer, wrong-oracle controls,
the shipped `nomicrophone` YUI invalid-admission surface, focused normal/race
tests, process-group timeout/output-cap cleanup, and the read-only C21
credential-free audio/tool replay. The replay proves deterministic fixture
effects; it does not claim physical-device or acoustic validation, and this
task does not use Realtime or credentials.

The candidate is pinned to the admitted `origin/main` revision recorded in
`provenance.json`. A later fetched `origin/main` descendant is recorded for
freshness only; it is not merged into or rebased onto this isolated candidate.
`verification-summary.json` and `provenance.json` are the committed handoff
artifacts; generated binaries, logs, and replay runs remain ignored.
