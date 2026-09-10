# C39 exact-source controller probe

This directory contains the bounded public controller probe for
`audio-runtime-c39-authorized-scope-amendment`.  It is fixture evidence only;
it does not activate the amendment, move canonical Work, or claim project
acceptance.

## Reviewed inputs

The probe requires the exact user authorization anchor and preserved C32 report
named in `factory/scripts/project_scope_amendment.py`.  Their SHA-256 values
are recorded in the amendment and are checked before an append.  The original
manifest and its three authority files are also checked byte-for-byte.  The
C32 report remains `decision=FAILED` with `DEVICE=BLOCKED`.

## Operations

```text
python3 factory/scripts/project_scope_amendment.py --root <root> create --output <contained-record.json>
python3 factory/scripts/project-control.py --root <root> amendment-append --record <contained-record.json>
python3 factory/scripts/project-control.py --root <root> amendment-status --amendment-id user-windows-hardware-scope-20260910
python3 factory/scripts/prepare-validation.py --root <root> <fresh-project-scope-work-name> '<payload-json-with-exact-amendment-reference>'
python3 factory/scripts/project-control.py --root <root> verify-completion --name audio-runtime
```

The append operation is append-only: it writes a bounded canonical record to a
fully fsynced temporary inode and publishes it with an exclusive hard-link. An
exact repeat is `already-present`; a conflicting ID, stale digest, forged
authorization, widened exclusion, traversal, symlink, malformed/oversized
record, changed rubric, or changed Realtime budget fails without changing the
published record.

An amended mission must explicitly be `scope=project`, carry a nonempty
`sourceRevision`, and include the complete status reference. Each fresh
customer and engineering report must match its staged mission digest, source
revision, artifact identity/hash, exact amendment reference, retained evidence
for software/device isolation and consumption semantics, and the two explicit
`OUT_OF_SCOPE` physical subproof entries. Historical vertical reports cannot be
relabelled into completion.

## Probe invocation

```text
python3 docs/temp/projects/audio-runtime/audio-runtime-c39-authorized-scope-amendment/probe.py \
  --source-revision <tested-sha> \
  --yui <same-source-immutable-yui> \
  --output <fresh-owned-evidence-directory>

# Optional existing audio/tool and interruption replay regressions:
python3 docs/temp/projects/audio-runtime/audio-runtime-c39-authorized-scope-amendment/probe.py \
  --source-revision <tested-sha> \
  --yui <same-source-immutable-yui> \
  --output <fresh-owned-evidence-directory> \
  --replay-fixture <audio-tool-fixture> \
  --replay-config <audio-tool-config-directory> \
  --replay-fixture <interruption-fixture> \
  --replay-config <interruption-config-directory>
```

The probe creates an isolated Git/admission repository, uses a local fake
canonical validation responder, exercises append/status/preparation/completion,
checks a widened-exclusion rejection and protected hashes, and runs a
deterministic timeout/TERM/KILL/reap control. The supplied yui is launched
credential-free for `--help`; primary may additionally pass
`--replay-fixture` and `--replay-config` to run the existing audio/tool bundle
replay. Software/file replay is not native Windows endpoint consumption or
physical acoustic proof.

The bounded controller fixture result is `ACCEPTED`: append returned
`appended`, the exact repeat returned `already-present`, and the expanded
exclusion negative control exited `1`. The timeout control exited `-15` after
bounded TERM/reap, and the supplied C32 yui `--help` exited `0` with the
immutable `db1f7de4881d9868859e546a2f66b0cd35e60ac5682c323b445254f3c2aaf628`
SHA-256 unchanged. The full machine-readable result is kept beside this
README under `evidence/probe-report.json`; a run supplied with both replay
pairs records its result under `evidence-replay/probe-report.json`. Each report
records source revision, protected hashes, fresh mission/report completion, and
residual physical/acoustic limits.

The latest repaired-source handoff is archived at
`evidence-repair-4256aa6-run1/probe-report.json` for source
`4256aa62292232b26c8c5d22d543ab90affeca58` (report SHA-256
`f0fa6ce82289494c3733247498ac5069135c49a4f06a08ad7c3b5be5068329cd`). It
also records the required missing-Work-project and mismatched-staged-build
identity negative controls, exact prepared project/build identities, and the
same audio/tool and interruption replay oracles. The rebuilt yui input is
`/tmp/audio-runtime-c39-yui-4256aa6`, SHA-256
`2d48f35c1876d801eabd1fbe0ff8b18c2686e4a947b308fce3a5d1994b61f6db`.

The merged-main follow-up is archived at
`evidence-merge-5671366/probe-report.json` for source
`56713660d2927994bd6757fe22a01e08110563ed` (report SHA-256
`9616a7c4d1d0d04f6c78a4b663bb62a5144981868134ff296a4ca81b78749a11`). It
uses yui `/tmp/audio-runtime-c39-yui-5671366` with SHA-256
`71bb89baf3ebaf10557ca4224165ad70d26cdeb0b5882f4d7a10e65a92d0067b` and
passes the controller, audio/tool replay, interruption replay, negative
controls, protected-hash and bounded child-cleanup checks.

The current-main merge checkpoint is archived at
`evidence-merge-b98e5e7/probe-report.json` for tested source
`b98e5e7c149b3394b520f3af59bf4ebff8f0f77a`. Its yui is
`/tmp/audio-runtime-c39-yui-b98e5e7` (SHA-256
`c99078de28b992b60f1c238376f8dd0ed1207288f7ece732119fd966c5319683`), and
`resource-usage.json` records the bounded storage measurements and provenance.

The latest current-main integration is archived at
`evidence-current-main-ace0fdb-run2/probe-report.json` for tested source
`ace0fdbe7e91685f02a89c0879a7d426b554c311` (report SHA-256
`170f282653f35c4a54718794526ff94b8c74c163fd0112d1a3a6829929119626`). It
uses the freshly built nomicrophone yui
`/tmp/audio-runtime-c39-yui-ace0fdb` (51,154,738 bytes, SHA-256
`3541db580e476effd2d0c18cbba6943a5d8363763a091719e293e74a4f57932c`) and
passes the controller, protected-hash, exact identity, bounded cleanup and
both public replay cases. Audio/tool replay is 3,200 rendered bytes and 4,800
provider bytes; interruption replay is 3,360 rendered bytes and 3,840
provider bytes with the 2,400-byte healthy follow-on tail. Directory replay and
missing-timeline rejection controls pass for both cases. Storage provenance is
in `evidence-current-main-ace0fdb-run2/resource-usage.json`; the probe used
zero Realtime sessions/seconds and the excluded native Windows/physical
acoustic subproofs remain OUT_OF_SCOPE rather than PASS.

The aggregate-deadline repair is archived at
`evidence-repair-6558703-run1/probe-report.json` for committed source
`655870389fea3c86e55da02a68255e213d97fc94` (report SHA-256
`8233bda6135ed50abf12d9fade798a096f1b21d2701f7bc68ebdde75b0f03e3c`). It
uses freshly built nomicrophone yui `/tmp/audio-runtime-c39-yui-6558703`
(51,154,738 bytes, SHA-256
`3541db580e476effd2d0c18cbba6943a5d8363763a091719e293e74a4f57932c`). The
probe passed in 3.060729 seconds with one aggregate monotonic deadline,
controller append/idempotent status, protected hashes, deterministic
TERM/reap, audio/tool replay (3,200 rendered / 4,800 provider bytes), and
interruption replay (3,360 rendered / 3,840 provider bytes, sequence
`[1440, 2400]`, healthy tail 2,400 bytes), plus directory replay and
missing-timeline negatives. All children were reaped and the probe used zero
Realtime sessions/seconds. Resource measurements and the below-reserve
operator condition are recorded in the adjacent `resource-usage.json`;
no cache cleanup or peer artifact deletion was used.

The current-main conflict repair is archived at
`evidence-merge-cab3310-run1/probe-report.json` for exact source
`cab3310c9ec0dc84e64706da87eb4dbf53b963db` (report SHA-256
`0d80272decc1369a7047b1e5c2ebede88ecf0cee4bf589a6f7f1e33774bceb50`). The
fresh nomicrophone yui is `/tmp/audio-runtime-c39-yui-cab3310` (51,171,954
bytes, SHA-256
`a66517529efe174bf550820e16573d3e99c081a13e5a09bc4d7618050fcf9280`). The
probe completed in 3.129179 seconds with zero Realtime sessions/seconds.
Controller append/idempotent status, exact staged Work/project/build identity,
alternate-manifest and mismatched-build negatives, protected hashes,
deterministic TERM/reap, and unchanged yui help hashes passed. Audio/tool
replay passed 3,200 rendered / 4,800 provider bytes with the existing PCM
oracles and tool marker; interruption passed 3,360 rendered / 3,840 provider
bytes with sequence `[1440, 2400]` and the 2,400-byte healthy follow-on tail.
Directory replay and missing-timeline negatives passed for both cases, and all
children were reaped. Storage provenance is in the adjacent
`evidence-merge-cab3310-run1/resource-usage.json`; available space was below
the 2 GiB operator reserve, so no further build or cache mutation is planned
until operator recovery. This remains executor handoff evidence only: script
CI, independent review, guarded merge, the primary-owned exact-artifact
vertical probe, physical/acoustic proof, and project completion remain open.
