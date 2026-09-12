# C107 characterization evidence

This directory is the evidence-only implementation for
`audio-runtime-c107-characterize-post-wave-cli-ownership`. It analyzes the
immutable accepted-main revision
`d4766c3dbbf2c198142047ead4449d58dd47d485` and never edits production code,
tests, shared Wire/architecture files, peer worktrees, or historical evidence.

The analyzer delegates parsing to the read-only C50 standard-library Go AST
census contained in the pinned source archive. The C107 schema adds explicit
classification, caller, uncertainty, service-dependency, and subtraction
evidence. `verify.py` captures the admitted project board and open PR pins,
checks ancestry and scope, runs two clean deterministic analyzer passes,
materializes three disjoint residual candidates, exercises fail-closed
negative controls, and runs bounded software-only public and regression probes.

Run the complete local evidence pass with:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c107-characterize-post-wave-cli-ownership/verify.py --mode all --child-timeout 60 --total-timeout 600
```

`run_public_regressions.py --source d4766c3dbbf2c198142047ead4449d58dd47d485`
is the named public-probe entry point from the admitted PRD. CI and independent
review remain external; this characterization does not claim retirement or
project completion, and software replay is not device or acoustic proof.
