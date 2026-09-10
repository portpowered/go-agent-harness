# C40 interactive policy retirement evidence

This evidence directory contains the standalone policy consumer and bounded
verification runner for `audio-runtime-c40-retire-cli-interactive-tool-policy`.
The consumer is a separate Go module and is always built with `GOWORK=off`.
It imports the public runtime tools contract, tools Wire, and the shared
message value type only. It has no CLI, WebMCP, device, credential, terminal,
or ambient configuration dependency.

The executor commands are:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c40-retire-cli-interactive-tool-policy/run.py inventory
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c40-retire-cli-interactive-tool-policy/run.py consumer
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c40-retire-cli-interactive-tool-policy/run.py wrong-oracle
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c40-retire-cli-interactive-tool-policy/run.py public-policy
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c40-retire-cli-interactive-tool-policy/run.py replay-regression
```

`run.py` records exact child argv, selected environment, cwd, exit status,
elapsed time, and bounded stdout/stderr under `runs/`. It uses the accepted
C16 audio/tool and interruption fixtures read-only, and checks their recorded
SHA-256 values before running the shipped YUI replay. The frozen audio
oracles are 4800 bytes / `0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502`,
3840 bytes / `6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22`,
and the 2400-byte healthy tail at offset 1440 /
`16508b8b42304d49869684c95e47c794b0eb9b54fd9137537dfaa4370097dfbf`.

The replay check is regression evidence only. It does not claim physical
device or acoustic behavior, live Realtime use, CI success, independent
review, merge, or project acceptance.

## Current merged-head checkpoint

The current tested source checkpoint is `ecad8fb9633b5a41bfba8bdeaa0c4b9dd8ed2bc9`,
which merges fetched `origin/main` at `5f14c45313cfdc71e000fda209e3408fcf863faf`;
the evidence-only ledger update is commit `0831db2`.
The fresh inventory, GOWORK=off consumer, wrong-oracle, and public-policy runs
all pass on that head. Focused normal/race policy tests, replay-bundle and
strict allowlist regressions, Wire, architecture, registration, fmt, vet, and
diff checks also pass. The interruption replay is not rerun: C38/task198 still
owns the observed 2400-byte result against the frozen 3840-byte oracle, pending
reviewed repair and primary independent vertical acceptance. The branch remains
local and unsubmitted until that known failing replay can be rerun honestly.
