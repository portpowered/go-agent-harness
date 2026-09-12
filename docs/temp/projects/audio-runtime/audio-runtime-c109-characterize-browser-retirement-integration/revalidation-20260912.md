# C109 fresh focused revalidation

Timestamp: `2026-09-12T21:04:53Z`

This is a fresh software-only revalidation of the already committed C109
evidence. It does not change C61, C83, C79-owned shared files, or any
production/test source.

Both bounded runner invocations used the required disposable tree:

- base `d4766c3dbbf2c198142047ead4449d58dd47d485`
- order `main -> C61 -> C83`
- final synthetic tree `4d8e93ae0e74db403fdb02c70db9cf849c590156`
- process groups clean, credential-free, and synthetic tree clean

## `browser-audio-tool`

Command:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c109-characterize-browser-retirement-integration/run_public_checks.py --case browser-audio-tool --aggregate-timeout 300 --child-timeout 90 --output /tmp/c109-browser-audio-tool-current.json
```

Result: `passed`; elapsed `244.658s`; output SHA-256
`2d45612350736a27281a323a3be1b3cddbad3cdb1676d434de5dd14956ce47af`.

All twelve checks passed with exit code 0: browserrunner/browserscenario
normal and race; C61 and C83 external consumers normal/race with `GOWORK=off`;
the accepted-main delta barrier regression; CLI cancellation normal/race;
nomicrophone cross-module coverpkg cancellation; accumulated session CI
regressions; and the shipped credential-free browser/audio/tool workflow.

## `malformed-or-canceled`

Command:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c109-characterize-browser-retirement-integration/run_public_checks.py --case malformed-or-canceled --aggregate-timeout 300 --child-timeout 90 --output /tmp/c109-malformed-canceled-current.json
```

Result: `passed`; elapsed `26.544s`; output SHA-256
`22b7ac717f10cf5c348a22a6df1f40dc6dd27a8c5fff08f1ece01cf919702d53`.

All three checks passed with exit code 0: malformed credential overflow,
timeout and cleanup; canceled browser scenario normal; and canceled browser
scenario race.

No CI, review, merge, vertical acceptance, hardware, acoustic, or project
acceptance claim is made by this revalidation. The existing C109 report keeps
C61/C83 delivery claims false, the historical C61 rewritten-test finding open,
and the C79/provider-audio loss outside C109 ownership.
