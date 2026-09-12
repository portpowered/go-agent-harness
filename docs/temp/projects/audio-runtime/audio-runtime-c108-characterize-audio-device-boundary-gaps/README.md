# C108 audio/device boundary characterization

This directory is the only owned path for `audio-runtime-c108-characterize-audio-device-boundary-gaps`.

The evidence is pinned to accepted/current `origin/main` `d4766c3dbbf2c198142047ead4449d58dd47d485` and retains startup integration `8bdafc7f947a3a2c9856220abdc539437035bd21`. `analyze.py` reads the pinned tree, inventories production paths outside `go-audio` and `go-device-gateway`, and writes deterministic machine-readable source evidence. `run_public_checks.py` runs the shipped credential-free replay and malformed/truncated negative. `verify.py` is fail-closed for stale hashes, missing caller/coverage evidence, proof upgrades, queue/consumption conflation, candidate overlap, ownership drift, and changes outside this directory.

The four proposals in `candidates.json` are characterization-only: device-pump lifecycle, the session audio-rate contract, provider-media normalization, and RTP transport tracks. They receive no repair lease. Preserved mixer/input/output predecessor surfaces are reported as historical findings and deliberately excluded from candidate writer paths. Queue admission, buffer/file receipt, simulated callback consumption, physical-device consumption, and acoustic proof remain separate; native Windows hardware/endpoints and physical acoustics are out of scope and never PASS.

The executor submits the exact committed head to script CI after local focused evidence passes. Script CI owns broad checks; this task does not poll CI or claim project acceptance. After merge, the primary must stage a fresh immutable vertical probe.
