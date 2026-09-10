# C44 model-admission evidence

This packet verifies the provider-owned realtime model admission boundary for
`audio-runtime-c44-retire-cli-model-admission`.

The external consumer is a separate Go module. It is built with `GOWORK=off`
and imports only the public `providers` and `providers/wire` packages. Its
custom catalog proves that injected catalogs are preserved, supported-model
ordering is deterministic and isolated, typed errors survive the public
boundary, nil catalogs fail closed, and non-OpenAI providers remain
unrestricted.

The public mode also builds the shipped CLI with `-tags=nomicrophone`, checks
that invalid bare-session and self-play models fail before a loopback provider
connection or self-play output creation, and replays the reviewed C21 audio
and interruption fixtures for exact PCM, marker, transcript, terminal, and
clean-shutdown parity. No provider credentials or live network calls are
required.

Run the bounded controls from the repository root:

```text
rtk python3 docs/temp/projects/audio-runtime/audio-runtime-c44-retire-cli-model-admission/verify.py --mode public --child-timeout 60 --total-timeout 600
rtk python3 docs/temp/projects/audio-runtime/audio-runtime-c44-retire-cli-model-admission/verify.py --mode negative-controls --child-timeout 60 --total-timeout 600
```

The negative-controls mode requires the external consumer to reject the
deliberately wrong built-in-model oracle and proves that a forced timeout kills
the complete process group within the child bound.

## Implementation ledger

The candidate was based on reviewed main `926ded7b` and then merged with the
freshly fetched `origin/main` `11b9035b` in the isolated worktree. Required
baseline `3194edd9` and startup integration `8bdafc7f` remain ancestors. The
merge is recorded in the task branch history; the verifier records the exact
source revision used by each public or negative run.

The baseline `session_models.go` had 67 lines. The final adapter is 68 lines:
`lookupOpenAIRealtimeModel`, `unsupportedOpenAIRealtimeModelErrorFor`,
`validateBareSessionModel`, and `validateSelfPlayModel` remain as compatibility
and presentation adapters, while their direct catalog lookup and unsupported
error construction decisions are retired. The exported model aliases,
constants, and error identity remain available to existing callers. The
provider-owned decision is `providers/internal/admission.Admission.Decide`,
with `admission.Service` exposed through `providers/wire.NewModelAdmission`.

The owned verifier records process output hashes, artifact SHA-256 and sizes,
reviewed fixture hashes, source archive and toolchain identity, and canonical
hashes for all tracked files in the external-consumer and shipped-yui build
input scopes. A run with `source_tree_dirty=true` is diagnostic only; final
handoff evidence must be regenerated from the clean committed source. Native
Windows hardware and physical acoustic proof are out of scope under the
current project amendment; Windows software and hermetic checks remain later
project gates.
