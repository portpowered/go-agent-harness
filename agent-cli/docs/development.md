# Agent CLI Development Guide

This guide is the package-local contributor guide for `libraries/agent-cli`. Read it before changing the Agent CLI, then apply the shared [Libraries Development Guide](../../../docs/processes/libraries-development.md) and [Library Standards](../../../docs/standards/systems/library-standards.md).

## Purpose

`agent-cli` is the standalone command-line binary for running LLM agents with filesystem, shell, web, skill, multimodal, streaming, and session tools. It wraps `go-agent-loop` with terminal commands and provider configuration.

## Local Architecture

- `cmd/yui/` is the shipped binary entrypoint. `cmd/agent/` remains a source-compatibility entrypoint only.
- `internal/cli/` owns Cobra commands such as `ask`, `chat`, `tool`, and `session`.
- `internal/agent/` bridges CLI config into `go-agent-loop` execution.
- `internal/tools/` owns the shell, filesystem, web, skill, and tool registry implementations.
- `internal/config/` loads YAML config from `~/.agent-cli/config.yaml` and supports CLI overrides.
- `internal/wire/` contains Google Wire dependency injection.
- `docs/session-record-replay.md` owns the session record/replay workflow.

## Development Commands

Run commands from `libraries/agent-cli`.

```bash
make build
make test
make test-timing
make fmt
make vet
make deps
make deps-tidy
make wire
```

Use `make wire` after changing dependency injection providers or constructor wiring. `make build` emits the binary under `bin/yui`; this module forces a full rebuild in git worktrees to avoid stale build cache issues.

### Windows-supported quality path

Use the package Makefile as the supported local quality entrypoint on Windows, macOS, and Linux. Do not replace these targets with shell-specific Go commands during normal contributor verification.

```bash
make build
make test
```

The Makefile exposes these override variables for local diagnostics and slower workstations:

| Variable | Default | Purpose |
| --- | --- | --- |
| `GO` | `go` | Go tool command used by Agent CLI targets |
| `BUILD_CGO_ENABLED` | `0` | `CGO_ENABLED` value exported for `make build` |
| `BUILD_OUTPUT` | `bin/yui` | Binary path written by `make build` |
| `GO_TEST_TIMEOUT` | `10s` | Full-suite timeout passed to `go test ./...` |
| `TEST_TIMEOUT_RUNNER` | `./cmd/testtimeout` | Finite outer boundary that terminates blocked test descendants |

Examples:

```bash
make build BUILD_OUTPUT=bin/yui-local-check.exe
make test GO_TEST_TIMEOUT=180s
```

`make test` uses `cmd/testtimeout` to apply the same finite value to the
test-command process group, so a blocked test child cannot survive or hold
the test output pipe open. The root repository's credential-free integration
targets use the narrower `AGENT_CLI_INTEGRATION_TIMEOUT` contract documented
in the shared testing-tier guide.

`make test-timing` runs a no-test preflight before the full `go test -json -v` suite, then prints sorted package-level and test-level timing evidence. Use it when the default test path is too slow or when you need to separate dependency download, compilation, and cache warmup from product test runtime.

### Windows-Native Quality Gate

Native Windows PowerShell contributors should run the Agent CLI review gate from `libraries/agent-cli`:

```powershell
make build
make test
```

`make build` uses the shared library Makefile portability pattern, so PowerShell runs `go build` through `make` without POSIX inline environment assignment. See the shared [Libraries Development Guide](../../../docs/processes/libraries-development.md) for the cross-library Makefile convention.

`make test` is the standard fast review gate. The default package timeout is `10s`, and the suite is expected to complete under that gate after normal Go cache warmup. Longer timeouts are local diagnostics only, for example:

```powershell
make test GO_TEST_TIMEOUT=120s
```

Do not treat a longer timeout as the standard quality gate.

## Package-Specific Verification

1. Run `make test` for command, config, session, and tool behavior.
2. Run `make test-timing` before changing slow tests or quality-gate timeouts so package and test runtime evidence is captured without committing local diagnostic artifacts.
3. Run `make build` when command wiring, Wire providers, or binary entrypoints change.
4. Run `make wire` before `make build` when Wire provider sets or injected dependencies change.
5. Run the affected session record/replay tests when modifying `yui session --record`, `yui session --replay`, capture files, or replay divergence behavior.
6. If a change touches the shared `messages.Message` model through `go-agent-loop`, also test `go-agent-loop` and `go-llm-gateway` as described in the shared guide.

## Local Gotchas

- CLI flags such as `--api-key`, `--model`, and `--provider` override config file values.
- Session replay reads a capture file and must not make live provider network calls.
- Live `yui session --record` supports Grok realtime captures and OpenAI Realtime captures. OpenAI live session mode routes through the sessional inferencer instead of stateless OpenAI inference.
- Live `yui session --record` for session providers must observe `messages.Session.Done()` separately from Agent Loop deltas so provider-side closes can cancel and join the command loop promptly.
- Raw WebSocket `yui session --replay` fixtures route by capture provider metadata; keep `provider.name` accurate so OpenAI Realtime fixtures exercise the OpenAI session provider instead of the Grok replay path.
- End-to-end session replay smoke fixtures should include a provider close event such as `session.closed` so the public command path proves model output and graceful shutdown, not just response completion.
- `agent-cli/test/integration/testdata` is package-private fixture space. Shared committed `.session.json` replay fixtures belong under `go-llm-gateway/pkg/testing/testdata/session-fixtures`, which is the authoritative repository contract for cross-module replay behavior.
- API keys may live in `~/.agent-cli/config.yaml`; do not commit config files or captured secrets.
- Add new tools through the local `Tool` contract, register them in `internal/tools/`, and update Wire wiring when construction changes.

## Related Docs

- [Agent CLI README](../README.md)
- [Session Record and Replay](session-record-replay.md)
- [Agent CLI Intent](../../../docs/intents/agent-cli.md)
- [Library Standards](../../../docs/standards/systems/library-standards.md)
- [Coding Standards](../../../docs/standards/code/coding-standards.md)
