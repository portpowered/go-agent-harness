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
