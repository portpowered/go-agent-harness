# C87 session-turn retirement evidence

This directory records the bounded verification surface for
`audio-runtime-c87-retire-cli-session-turns`. The public contract is
`go-agent-runtime/services/sessionturns`; its state machine is private and is
constructed by `sessionturns/wire`. The former CLI file is a deprecated
40-line adapter.

The standalone consumer is a separate Go module and is run with `GOWORK=off`:

```text
cd docs/temp/projects/audio-runtime/audio-runtime-c87-retire-cli-session-turns/external-consumer
rtk proxy env GOWORK=off go test ./...
```

The focused evidence commands are:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c87-retire-cli-session-turns/verify.py --mode mutations
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c87-retire-cli-session-turns/verify.py --mode retirement-and-adapter
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c87-retire-cli-session-turns/verify.py --mode owned-and-excluded-paths
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c87-retire-cli-session-turns/run.py --case credential-free-audio-tool-or-ask --child-timeout 60 --aggregate-timeout 300
```

`run.py` removes credential-shaped environment variables, bounds each child at
60 seconds and the aggregate at 300 seconds, and exercises only the local
consumer. These checks are executor evidence; they do not claim script CI,
independent review, merge, vertical acceptance, or project acceptance.
