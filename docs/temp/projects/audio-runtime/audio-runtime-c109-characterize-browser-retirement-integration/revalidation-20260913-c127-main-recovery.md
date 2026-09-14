# C109 C127 accepted-main recovery — 2026-09-13

This checkpoint remains inside the admitted C109 evidence directory. The
running host checkout, C61/C83 preserved branches and worktrees, C79 shared
registries, production/test source, and unrelated owner paths were not changed.

## Dependency and exact rejection

- Admission remains `admitted` for the sole `audio-runtime` project, and the
  isolated branch still exactly matches `prd.json.branchName`.
- The previous C109 CI rejection is retained in
  `ci-rejection-34762923229.json`. Run `34762923229` tested C109 head
  `64613f656f642b26bc09f670a3f677239ac74364`; eight lanes passed and
  `CI (integration)` failed only in
  `TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio/test48_matched_healthy_control/provider_burst`.
  The full failed-job log was read from job `103738782236`; its raw metadata
  and log SHA-256 values are recorded in the JSON record.
- The failure was the known provider-audio terminal-drain signature:
  `114395/120795` compared samples, exactly `6400` lost, zero queued/dropped/
  overflow/discarded samples, and `19` underflow events. It remained assigned
  to C127/provider-audio and was not repaired or relabeled by C109.
- C127's reviewed delivery and C138's independent vertical repro released
  the dependency at accepted `origin/main=915ed982d23f2e549e529ff43c4f370b4b51e394`.
  C109 integrated that main as `ca618fe68ef181b2c75ccf7efaada0cf2b328760`.
  Accepted main, startup integration, C61 and C83 remain ancestors, and the
  branch diff against current main contains only the C109 evidence directory.

## Fresh owned evidence

- Required `main -> C61 -> C83` and reverse analyzer rehearsals pass, with
  `reviewMain=915ed982`, exact candidate refs, and preserved worktrees
  unchanged.
- `test_analyze.py -v` passes `8/8`; `verify.py --mode all` passes all eight
  accumulated checks, deterministic reruns, the caller-tree mutation control,
  and `20` negative fixtures.
- The credential-free browser/audio/tool public matrix passes `18/18` under
  the admitted `90/300` second bounds. The malformed/canceled matrix passes
  `3/3` under `60/180` seconds. Both report clean process groups, positive
  discovery, unchanged synthetic trees, and software-only local effects.

This is executor evidence only. The changed exact head is ready for the
script-owned CI gate; no CI-green result, independent review, guarded merge,
C61/C83 acceptance, C109 vertical probe, project acceptance, or physical/
acoustic proof is claimed. AUDIO, DEVICE, EMBED, SERVICE, TRACE, REPLAY,
FAILURES, QUALITY and PARITY remain open.
