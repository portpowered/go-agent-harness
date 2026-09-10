## Summary

- Add literal codec `PacketEnergy` coverage for 3+ channels, padded reduced-valid PCM16, PCM24/PCM32/float padded packets, zero-frame trailing nonfinite data, typed nonfinite/layout/truncation errors, true accumulation overflow, input immutability, mutation oracles, and zero-allocation success paths.
- Add the direct Windows `wasapiCapturePacketEnergy` entry point required by the existing portable Windows test workflow, including real storage, silent/zero/nil semantics, unsupported format/layout errors, nonfinite/overflow controls, and caller-byte preservation.
- Add a separate `GOWORK=off` consumer module, retained 40-case C32 oracle, 13-case C42 oracle, credential-free YUI audio/tool and interruption replay, strict replay negative control, and capped bounded process cleanup.

## Verification

The committed local evidence reports 53 consumer cases, exact rendered/provider PCM and marker hashes, clean recording timelines, successful directory replay, and negative rejection of both a wrong energy oracle and a missing timeline. Focused codec normal/race/vet, device tests/vet, consumer-module test, Windows PE32+ cross-compile, and architecture-size-check all pass.

The exact commands and hashes are in `docs/temp/projects/audio-runtime/audio-runtime-c42-capture-energy-software-coverage/README.md`, `coverage-map.md`, and `verification-report.json`. The C32 original oracle and original failed vertical report remain byte-for-byte preserved.

## Review repair

The repaired candidate adds a literal finite-square control proving
`0x1p+511` squared is finite `0x1p+1022`, while retaining the square-overflow
and true four-channel/four-frame accumulation-overflow controls. The verifier
requires exactly 13 disjoint C42 cases plus the retained 40 C32 cases, pins the
historical C32 failed-report hash to
`0345a628a6038e18bf7c7a59016e7a0002ae89734c6ff59aa541642f34b3d960`, and checks
unsupported/malformed Windows adapter paths preserve caller bytes and
sentinels.

The exact repaired software evidence is sourced from
`52a951a878fb0602f492af0206fbb6d72001420b`, with consumer SHA-256
`9aadbc5a767584e2f77c3c3b0fcfd8128c0f277d620d17d609374ca85cd908ac`, pinned
YUI SHA-256
`9d3c0d812e7027f3d3ace0cd7148efad3d83c09d17f3f7ec281fe2e922824c50`, positive
and negative run records under the evidence directory, and exact fixture
replay hashes. The prior native Windows software result is recorded at
`.github/workflows/ci.yml`, run
`34487167972`, job `102904301028`, head
`d301084027c8669f4af5b15c86a77cb847e21d6b`; the repaired head is submitted for
a new script-CI run and no current-head CI result is claimed.

## Scope and handoff

This PR is limited to the admitted `audio-runtime` task and its owned paths. No production repair was demonstrated as necessary, and no workflow, root module, policy, baseline, or peer-review files were changed. Native Windows WASAPI endpoint execution and physical/acoustic evidence are not claimed; under the effective 2026-09-10 user scope amendment they are `OUT OF SCOPE`, not a prerequisite for software delivery. This PR is ready for changed-head script CI handoff; it does not claim current-head CI green, independent review, merge, or project acceptance.
