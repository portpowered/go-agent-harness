# C86 retry-policy retirement evidence

This directory records the bounded evidence for
`audio-runtime-c86-retire-cli-rate-limit-retry-policy`. The separate consumer
imports only the public retry-policy contract and its dedicated Wire package;
it is run with `GOWORK=off` and has no CLI, credential, device, terminal, or
network dependency.

The immutable 103-line planning baseline is the accepted `origin/main`
revision `84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f`, with startup integration
ancestor `8bdafc7f947a3a2c9856220abdc539437035bd21`. Shared Wire registry and
architecture-baseline edits remain open until C79's reviewed guarded merge
and explicit ownership release; this task does not edit either file.

The evidence runner keeps one bounded aggregate deadline, caps child output,
removes credentials from child environments, and records exact source/branch
identity. Its public replay mode is a regression check only: software replay
does not prove device or acoustic behavior, CI success, independent review,
merge, or project completion.
