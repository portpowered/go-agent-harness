# C109 browser-retirement integration characterization

This directory is the complete owned surface for `audio-runtime-c109-characterize-browser-retirement-integration`.
It is evidence-only.  The analyzer makes disposable detached worktrees, records
the C61/C83 provenance and merge-order behavior, and removes those worktrees
before returning.  It never edits either predecessor worktree, the host
checkout, shared registries, or candidate source files.

The accepted main revision is `d4766c3dbbf2c198142047ead4449d58dd47d485`.
The preserved candidate revisions are C61
`8e8177c031a7b3b9322d712af19970e13fa7a1bc` and C83
`22cc6769aaf06d1e2c1275b064cc7ec29de3e371`.

Run the analyzer from the repository root, writing outside the checkout while
iterating:

```text
python3 docs/temp/projects/audio-runtime/audio-runtime-c109-characterize-browser-retirement-integration/analyze.py \
  --main d4766c3dbbf2c198142047ead4449d58dd47d485 \
  --c61 8e8177c031a7b3b9322d712af19970e13fa7a1bc \
  --c83 22cc6769aaf06d1e2c1275b064cc7ec29de3e371 \
  --order c61-c83 --output-dir /tmp/c109-required
```

The reverse control is run with `--order c83-c61 --expect-control`.  The
verifier accepts the two output directories and deliberately fails closed for
changed SHAs, reversed sequence ownership, unowned paths, missing merge
evidence, and prohibited candidate-acceptance claims.  `run_public_checks.py`
is the bounded software-only test runner; it records command, timeout,
credential-scrub, process-group, and effect-cleanup evidence without claiming
hardware or acoustic coverage.

The C79-owned `scripts/wire-packages.txt` and
`docs/architecture/architecture-size-baseline.json` remain untouched.  The
6,400-sample integration loss remains assigned to C79/provider audio and is
not duplicated, repaired, or relabeled here.  C61 and C83 remain unmerged and
unaccepted until their own task/review/CI gates are resolved.
