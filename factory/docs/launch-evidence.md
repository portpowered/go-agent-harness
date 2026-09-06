# Factory restart evidence — 2026-09-06

The owned factory resumed at 09:08 UTC on `http://127.0.0.1:7439`.
Session: `9d5bbd41-a4b3-438a-9e30-2818eaa4d69c`.
The original admitted project Work remains
`batch-admit-audio-runtime-audio-runtime-v1-audio-runtime`; no replacement project
was submitted. Admission remains `audio-runtime` / `audio-runtime-v1`.

The bootstrap integration revision is
`8bdafc7f947a3a2c9856220abdc539437035bd21`, including the original refactor baseline
`3194edd97aed588f7cdf2f8c58a69ac21da4c9ad` in its history. This pin is the first
delivery slice's input, not evidence that the baseline has merged to main.
The project leader ran Astra medium after the existing blocked project was moved
back to `init` with request ID `runtime-recovery-audio-runtime-20260906`. It emitted
the first slice, `audio-runtime-c01-baseline`, through request
`audio-runtime-c01-baseline-integration`. The idea passed admission and reached
the planner; the parent project is waiting on its dependent cycle. This is live
provider execution, separate from the mock-worker regression evidence below.

## Runtime and recovery

The private runtime uses source revision
`a82f2e5a532a25b3e163014b28e72190ac28c354`, plus the checked-in current-placement
recovery patch. Executable SHA-256:
`eb8c402b547531b9722f948a8a25c652f1297f14c2c8a10b75491a9850d617ed`.
The private Codex wrapper executes the app's CLI 0.153.4 from its original resource
directory. Astra medium and Luna max each passed a real read-only tool-use probe.

A post-launch build correction adds `-trimpath`. Two clean archive builds produced
the same hash, `6a0ac763891eaf32ae8bc36fa1c9527c023ce901d7d5705fae484d3d14f018fb`.
That binary is installed for subsequent invocations; the already-running process
retains the launch hash above. This documentation and build correction follow the
first slice's frozen bootstrap pin and should be included in a subsequent slice.

The successor preserves the recording `factory-runs/d0f6da3f4f28479eaa59d0ce5c515457.json`
under the shared Git directory, SHA-256
`19890e9880c13cab4d9a8faef340644b85d74d6b189edd5b53345e3e929ffa34`.
The launcher recorded the predecessor proof and atomically transferred admission
from session `d1219015-9002-4e73-a1bf-7293debe0cb9`.
Current recordings, process identity, build hashes, and baseline-pin audit records
remain in the private Git directory; use `run-factory.py status` for live state.

## Verification and scope

- 62 factory tests and 4 command-contract tests passed.
- 418 reference runtime package tests passed with the compatibility patch.
- Native mock-worker tests covered delivery, review rejection, escalation,
  failed-start recovery, repeated resume, and an operator move followed by resume.
- Authored graph checks enforce Astra medium leadership/planning, Luna max
  execution/review/validation, and routine meta planning every four hours.

The audio/runtime migration remains open under its immutable acceptance contract.
This restart does not establish customer audio replay, Realtime parity, baseline
merge, or completed independent validation.

The resumed runtime logs `session response event store publication is complete`
for provider progress publication. Project-lead completion and child admission
were observed despite that warning. Live provider-progress visibility remains a
known follow-up; it must not be represented as fully repaired by the Work-placement
compatibility patch.
