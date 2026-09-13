# C109 runner-race and final evidence revalidation — 2026-09-13

This checkpoint remains inside the admitted C109 evidence directory. It does
not modify production or test source, the PRD or manifest, C61/C83 preserved
refs or worktrees, C79 shared registries, or the running host checkout.

## Identity and preserved inputs

- Admission verification returned `admitted` for project `audio-runtime` and
  task `audio-runtime-c109-characterize-browser-retirement-integration`.
- `prd.json.branchName` and the isolated branch are
  `codex/audio-runtime-c109-characterize-browser-retirement-integration`.
- The repair checkpoint is `712c4e1cbb9da20705c7f0a00e65dd30b46507a5`.
  Fetched `origin/main` is
  `bd6a1289218d1bef1a3af36e64e9d4496062416f`, already ancestral to the
  checkpoint. Accepted main remains
  `d4766c3dbbf2c198142047ead4449d58dd47d485`; C61 remains
  `8e8177c031a7b3b9322d712af19970e13fa7a1bc`; C83 remains
  `22cc6769aaf06d1e2c1275b064cc7ec29de3e371`.
- The analyzer required and reverse-control rehearsals both passed, with
  unchanged candidate refs and preserved worktrees. Their final synthetic tree
  hash is `4d8e93ae0e74db403fdb02c70db9cf849c590156`.

## Causal repair and focused checks

- The prior bounded-runner review repairs remain present: abort output and
  post-abort cleanliness are retained, caller-owned exception cleanup is
  reported, and child output is streamed/capped with credential-marker
  scanning. The new checkpoint makes the surviving-group cleanup control
  tolerate the macOS `PermissionError` signal race while retaining fail-closed
  group liveness, and adds a caller-tree exception regression.
- `test_analyze.py -v` passes `8/8`; the ten repeated silent-descendant
  controls all returned `timeout` with `process_group_gone=true`.
- `verify.py --mode all` passes all eight accumulated checks and 19 negative
  fixtures, including determinism, caller-tree mutation, exact public-check
  set, source-derived ledger, handoff sequence and public cleanup. The
  verification SHA-256 is
  `03d30f46ee5e3c7ebeb9668f5023e4cbc4f23e4e121d3adae3e542ce41fe07e1`.
- The agent-loop delta-barrier regression passes `3/3`; the nomicrophone
  agentruntime cancellation/terminal coverage command passes. The synthetic
  public workflow supplies the C61/C83 browserrunner/browserscenario normal and
  race suites, both GOWORK=off consumers, and the malformed/canceled controls.

## Final software-only public evidence

- `browser-audio-tool` passes `18/18` in `257.163s` under the admitted
  `90/300s` bounds. Report SHA-256:
  `2d2f6973abf708be7126c0ddd0a8561fe0cdc2f66510bea19a364a80174dfc33`.
- `malformed-or-canceled` passes `3/3` in `29.832s` under `60/180s` bounds.
  Report SHA-256:
  `bd1962cb6806ac3e0225fda7b13766254d634d5f4f99584ad9716fe93c1d572c`.
- Required/control report SHA-256 values are
  `4e6a763adf332a653e233a7017a7e7307fa7bf19968430fab42d1de1df572adc` and
  `4b9ddf2e60be69365337a454fdb92d4398f2197676bc84f7dd1944eb94182d72`.
  The current runner/test source hashes are
  `4cdbbbf41bb66c7e7d65c0d097b64923dec00b5214048a19117c737a559fdb90` and
  `6ed6034e69fd17adc3988c932e9f09ed78e2a3283b755e51bd8c658628958b15`.
- This evidence is credential-free, software/local-process-only, and records
  clean synthetic-tree and process-group shutdown. It does not claim C61/C83
  fixed, merged, probed or accepted, and it is not device or acoustic proof.

## Delivery state

The known hosted provider-audio terminal-drain and other inherited failures
remain preserved under their recorded external owners; C109 does not edit,
duplicate, waive or relabel them. No hosted CI was polled in this revalidation.
The next action is to commit/push this evidence checkpoint, update PR #495,
and return the exact head to script-owned CI without polling.
