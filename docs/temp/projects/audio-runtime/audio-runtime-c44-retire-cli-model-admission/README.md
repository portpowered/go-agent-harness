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

The candidate was based on reviewed main `926ded7b`, first integrated with
fetched `origin/main` `11b9035b`, and then conflict-repaired against current
`origin/main` `d6efc88d` in the isolated worktree as merge `d6e4f9c`. Required
baseline `3194edd9` and startup integration `8bdafc7f` remain ancestors. The
merges are recorded in the task branch history; the verifier records the exact
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

The historical merged-head public run `verify-20260910T175919Z-21784` and
negative-controls run `verify-20260910T175942Z-21950` remain retained from
clean source `d6e4f9c`. After the review identified that the test-only lint
repair changed the verifier input manifests, fresh clean-source runs
`verify-20260910T193922Z-16355` (public) and
`verify-20260910T193938Z-16512` (negative-controls) were run at exact source
`54b920136220b9e9fe1c027104addf05d6b1cee6`; both returned `ACCEPTED` with
`source_tree_dirty=false`. Both runs record external consumer artifact
`6bba2d1f914ed50e54c12df8e4e827809e86d5916e872225eb10990b82bdf3d3`, shipped
`yui` artifact
`fb72039514a2721bcd04fa171c6361906d949950272e2c26c0bd7418f6b99255`, and
input manifests `5ae620e01eb64745dfc036d945fb8d6ad65495dd4e6c7fdff2b134b52f74741e`
(consumer) and
`28058a55b6f7cb97dcdb23b8f0f2103b15b6b8325b09a56546c9607394509528` (shipped
CLI). They preserve the external consumer normal/race passes, invalid CLI
admission side-effect controls, exact replay fixture/PCM controls,
wrong-oracle rejection, and bounded timeout TERM/reap with no survivors.
The merged-tree structural gates remain `182` packages, `1,881` files, and
`27,721` functions; coverage registration remains `172` workspace packages
across six modules, including the leased private-admission manifest. This
evidence update is documentation-only and outside both verifier build-input
prefixes; the recorded manifest hashes are therefore the explicit compiled-
input identity proof for its descendant.
