# C22 public ask/replay harness provenance

The C22 ask/replay runner is a bounded local-only adaptation of the `ask_case`
in C18's `verify-public.py`. It retains that harness's two-phase contract:

1. run a recorded ask against a loopback OpenAI-compatible SSE fixture;
2. stop and join the fixture; and
3. replay the capture after the helper has stopped, requiring identical output.

The C22 copy is self-contained, uses an isolated HOME/config directory, removes
inherited credential-like environment variables, records complete child output,
and owns no C18 files or runtime changes. The C18 source remains only provenance;
the C22 evidence is produced from the C22 candidate executable.
