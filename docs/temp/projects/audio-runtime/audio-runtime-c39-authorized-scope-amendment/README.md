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
