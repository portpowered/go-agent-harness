## Summary

- Add literal codec `PacketEnergy` coverage for 3+ channels, padded reduced-valid PCM16, PCM24/PCM32/float padded packets, zero-frame trailing nonfinite data, typed nonfinite/layout/truncation errors, true accumulation overflow, input immutability, mutation oracles, and zero-allocation success paths.
- Add the direct Windows `wasapiCapturePacketEnergy` entry point required by the existing portable Windows test workflow, including real storage, silent/zero/nil semantics, unsupported format/layout errors, nonfinite/overflow controls, and caller-byte preservation.
- Add a separate `GOWORK=off` consumer module, retained 40-case C32 oracle, 13-case C42 oracle, credential-free YUI audio/tool and interruption replay, strict replay negative control, and capped bounded process cleanup.

## Verification

The committed local evidence reports 53 consumer cases, exact rendered/provider PCM and marker hashes, clean recording timelines, successful directory replay, and negative rejection of both a wrong energy oracle and a missing timeline. Focused codec normal/race/vet, device tests/vet, consumer-module test, Windows PE32+ cross-compile, and architecture-size-check all pass.

The exact commands and hashes are in `docs/temp/projects/audio-runtime/audio-runtime-c42-capture-energy-software-coverage/README.md`, `coverage-map.md`, and `verification-report.json`. The C32 original oracle and original failed vertical report remain byte-for-byte preserved.

## Scope and handoff

This PR is limited to the admitted `audio-runtime` task and its owned paths. No production repair was demonstrated as necessary, and no workflow, root module, policy, baseline, or peer-review files were changed. Native Windows WASAPI execution and physical/acoustic evidence are not claimed; they remain a Windows-host prerequisite. This PR is ready for script CI handoff; it does not claim CI green, independent review, merge, or project acceptance.
