# C67 image-turn runtime extraction

This admitted slice moves reusable image preparation and first-turn publication
behind `go-agent-runtime/services/imageinput`. The CLI retains model/config
admission, filesystem loading, option shaping, audio/device orchestration,
recording-directory composition, duration handling, and the deprecated source
compatibility adapters.

The separate `consumer/` module is intentionally outside the workspace module
graph. It constructs `imageinput/wire.NewService` with an injected loader and
fake provider, then checks normalized PNG/JPEG values, deep-copy ownership,
immediate/deferred publication, capability forwarding, typed load/decode/send
failures, cancellation, and shutdown.

Run bounded owned evidence from the repository root:

```text
python3 docs/temp/projects/audio-runtime/audio-runtime-c67-retire-cli-image-turn-runtime/verify.py --mode baseline-inventory-oracles
python3 docs/temp/projects/audio-runtime/audio-runtime-c67-retire-cli-image-turn-runtime/verify.py --mode publication-negative-mutations
python3 docs/temp/projects/audio-runtime/audio-runtime-c67-retire-cli-image-turn-runtime/verify.py --mode adapter-retirement-scope
python3 docs/temp/projects/audio-runtime/audio-runtime-c67-retire-cli-image-turn-runtime/run.py --case all
```

`verify.py --mode final-scope-provenance` records the exact shared-gate
dependency. `scripts/wire-packages.txt` and the architecture size baseline are
shared paths owned by other admitted work, so C67 does not edit them or claim
their checks green. The `runs/` directory contains bounded, disposable logs
and is ignored by the repository policy.

The accepted-main baseline is `d5d6f84363d8569d5dc1a59985f8d45cf50e1d06`, with
manifest baseline `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad` and startup
integration `8bdafc7f947a3a2c9856220abdc539437035bd21`. The legacy production
file is 703 lines at accepted main and 452 lines after this extraction: 251
physical production lines retired. No CI, independent-review, guarded-merge,
or post-merge vertical-probe result is claimed by these local artifacts.
