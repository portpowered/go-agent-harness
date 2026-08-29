# Conversational customer simulation

`agent probe customer-simulation` is the one opt-in process-boundary harness
for the conversational customer-simulation scenarios. It starts the shipped
`agent` binary, sends ordered 16 kHz PCM16 turns over one continuously open
`--audio-in -` stream, records `--audio-out -` and `--record-dir`, and writes a
finalized evidence bundle for each selected scenario.

The command is intentionally not part of ordinary CI. It can call a billed
realtime provider and an independent stateless validator, and it exits
non-zero for `BROKEN`, invalid, incomplete, inconclusive, or unavailable
results. It never silently skips a missing credential or audio turn.

## Billed run

Build first, then provide a key without putting it on a command line or in a
captured artifact. The following is the supported setup; the `EXIT` trap also
removes the shell variables when the command fails:

```bash
make build

KEY=$(tr -d '\n' < ~/.you-agent-factory/secrets/OPENAPI_API_KEY)
export OPENAI_API_KEY="$KEY"
trap 'unset KEY OPENAI_API_KEY' EXIT

./agent-cli/bin/agent probe customer-simulation \
  --live \
  --required \
  --audio-dir /absolute/path/to/customer-simulation-audio \
  --report /tmp/customer-simulation-report.json
```

The command locates `agent-cli/bin/agent`; if no shipped binary is found, it
builds a temporary copy outside the checkout. The default evidence root is an
OS temporary directory. To retain a named root, pass a fresh directory outside
the checkout with `--run-root /tmp/customer-simulation-run`; existing scenario
directories are never overwritten.

The audio directory accepts any of these layouts, in this order of preference:

```text
<root>/<scenario-id>/<action-id>.wav
<root>/<scenario-id>/<turn-number>.wav
<root>/<scenario-id>-<action-id>.wav
<root>/<action-id>.wav
```

`.pcm` and `.raw` are also accepted. WAV files must be mono 16 kHz PCM16;
raw files must already be even-length PCM16. `--audio file...` can be used
instead, with exactly one file per turn in scenario-selection order. The
required set expands to Family A, Family B, Family D-SIGINT, and Family
D-natural. `--family A` (or `B`, `C`, `D`, `D-SIGINT`, `D-NATURAL`, `E`) runs a
specific built-in selection; `--scenario path.json` loads a versioned custom
scenario.

The output report contains only sanitized scenario identity, timing, process
cleanup facts, action dispositions, mechanical findings, and the parsed
validator verdict. The complete bundle is the review artifact. Inspect it with
the canonical verifier before copying verdicts into a PR description:

```bash
./agent-cli/bin/agent probe customer-simulation --help
go test ./agent-cli/internal/probe ./agent-cli/internal/cli
```

For a review-ready live evidence set, retain the four report entries and copy
each `validator-verdict.json` object verbatim into the PR body along with the
scenario ID/family, elapsed time, turn/action dispositions, signal and exit
facts, artifact hashes, side-effect observations, and any defects. Do not copy
raw audio, transcript payloads containing private data, authorization headers,
or provider responses outside the typed verdict object.

## Hermetic validation and focused reruns

Ordinary validation uses fake providers and the typed oracle/validator seams;
it does not call a provider or require a key:

```bash
make test-hermetic
go test ./agent-cli/internal/probe ./agent-cli/internal/cli
```

A hermetic rerun is for code, schema, process-plumbing, and negative-control
changes. A billed rerun is only for fresh live evidence and must use a fresh
`--run-root`, the final pushed binary, the explicit `--live` flag, the key
setup above, and the same audio inputs. Never treat a missing key, missing
audio file, validator timeout, or provider refusal as a skipped passing run;
the command records the failure and returns non-zero.
