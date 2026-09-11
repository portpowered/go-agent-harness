# C46 terminal-policy retirement evidence

This directory contains the standalone provider-terminal-policy consumer and
bounded source/test evidence for `audio-runtime-c46-retire-cli-terminal-policy`.
The consumer is a separate Go module. It imports only the provider contract,
provider Wire entrypoint, and provider-neutral message value; it has no CLI,
flags, terminal state, credentials, device, or ambient configuration import.

The consumer's normal run exercises explicit-field precedence, legacy comma
handling and the 256-byte bound, failed-status normalization, exact
case-sensitive eligibility, cancellation exclusion, default/capped/rounded
delays, nil handling, and a deliberate wrong-oracle failure. The source,
build-input, test, and disk-reserve provenance is recorded alongside the
bounded command results.

This evidence does not claim script CI, independent review, guarded merge,
post-merge vertical acceptance, physical/acoustic behavior, or project
completion.
