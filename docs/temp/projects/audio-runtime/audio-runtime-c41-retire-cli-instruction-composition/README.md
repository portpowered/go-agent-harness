# C41 instruction-composition evidence

This directory is the admitted evidence folder for
`audio-runtime-c41-retire-cli-instruction-composition`. `consumer` is a
separate Go module that uses only the public `session` contract and its Wire
constructor. It supplies normalized prompt/scope values and an injected loader;
it does not import the CLI, private runtime packages, flags, terminal state,
credentials, devices, or ambient filesystem discovery.

The consumer's `expected.json` contains a frozen resolved-string oracle and a
SHA-256 oracle for the complete composed policy. `consumer-negative` mutates
that expected digest and must fail with a policy-oracle mismatch.

`verify.py` runs bounded positive, negative, regression, and process-cleanup
controls and writes JSON reports under `reports/`. It records the source
revision, module/build inputs, executable hash, commands, exit codes, output,
and cleanup observations. It does not use a live provider or claim physical,
acoustic, CI, review, merge, or project acceptance.
