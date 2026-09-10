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
controls and writes JSON reports under `reports/`. The `public-session` mode
also rebuilds the shipped `agent-cli/cmd/yui` binary from the candidate source
and runs it against an in-process deterministic RFC 6455 provider. The
provider observes the initial tool-enabled `session.update` before the first
user turn, drives a `read_file` function call, checks the marker effect and
continuation response, and requires a clean `session.closed`/process shutdown.
This is the handoff's live-host control: the shipped yui command uses the
LiveService bootstrap, and the provider-observed instructions must exactly
match the standalone Wire-composed oracle. The report records the observed and
expected instruction hashes rather than relabeling a standalone result as live
evidence.

`regression` rebuilds the same yui binary and runs the committed offline replay
fixture. Reports record the source revision, module/build inputs,
executable/fixture/capture hashes, commands, exit codes, output, and cleanup
observations. The provider is local and the API key is only a hermetic sentinel;
no live credentials, physical/acoustic result, CI, review, merge, or project
acceptance is claimed.
