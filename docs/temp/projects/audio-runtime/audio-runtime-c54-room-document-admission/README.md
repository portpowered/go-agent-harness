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

The tested source revision remains explicit in `provenance.json`. When the
required baseline integration advances `origin/main`, the fetched revision is
recorded there and must be an ancestor of final HEAD. Final scope is then
evaluated against that integrated `origin/main` tree, while the admitted source
revision remains pinned for ancestry and baseline checks.
`verification-summary.json` and `provenance.json` are the committed handoff
artifacts; generated binaries, logs, and replay runs remain ignored.
