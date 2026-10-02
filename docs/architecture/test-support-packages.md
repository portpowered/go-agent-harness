# Test-support packages

Test fakes, scripted runtimes and fixture helpers live where only tests can
link them:

- in `_test.go` files, when one package uses them;
- in a package whose name marks it as test support, when several packages
  share them, and only `_test.go` files import that package.

## Naming rule

Name a test-support package so that one element of its import path, below
the module directory, marks it. An element marks test support when it:

- is `test`, `tests`, `testdata`, `testing`, `testkit`, `testutil`,
  `fixture`, `fixtures` or `harness`;
- starts with `test`, `mock`, `fake` or `stub` (`testcover`, `mocktool`,
  `fakebrowser`, `stubprovider`);
- ends with `test`, `stub`, `stubs`, `fixture`, `fixtures` or `harness`
  (`clitest`, `webmcptest`, `providerstubs`, `audiofixture`, `timeharness`).

Production packages must not use these forms. `sessionfixturevalidator` is
fine because `fixture` sits inside the element. Simulation is not on the list:
`probe/customersim` is the production customer-simulation probe.

## The check

`make prod-deps-check` (run by `make architecture-check`, so by CI's static
job and the prepush gate) runs `scripts/prod-deps-check.py`. For every main
package that is not itself test support, it lists the linked packages with
`go list -deps` in each build configuration: GOOS linux, darwin and windows
without cgo, the native GOOS with cgo, and linux with each CI tag set
(`wireinject`, `nomicrophone`, `e2e`). It fails on any linked repository
package that is test support, printing the import chain that pulls it in, and
on any main or dependency that does not resolve. It runs in about 3 seconds.

`go-llm-gateway/pkg/testing` is the one allowed exception. It is the public,
documented session-capture fixture contract, and the production replay and
recording services read and write provider captures through it.

## Shared packages

| Package | Holds |
| --- | --- |
| `agent-cli/internal/webmcp/webmcptest` | `ScriptedBrowserRuntime` and its target sessions, and `Broker`, the scripted, recording `webmcp.Broker` the doctor, operations, session-broker and CLI tests share |
| `agent-cli/internal/probe/faulttest` | transport and RTC fault decorators for probe tests |
| `agent-cli/internal/roomtest` | the CLI room-document aliases, mixer and mesh that only tests use |
| `agent-cli/internal/transport/cli/clitest`, `agent-cli/internal/services/servicetest` | CLI and service test harnesses |
| `go-llm-gateway/pkg/providers/openaichatgpt/fakechatgpt` | the scripted fake ChatGPT Codex backend (`POST /responses` as SSE, `GET /models`) that the `openaichatgpt`, provider-service and CLI ask/chat tests use |
| `go-llm-gateway/pkg/providers/openailive/fakelive` | the scripted fake GPT-Live server (in-process dialer and `httptest` handler) that the `openailive` codec and runtime admission tests use |
| `go-llm-gateway/pkg/providers/openailive/codexrtc/fakecodex` | the fake ChatGPT-credential GPT-Live backend (`httptest` call creation answered by an in-process pion peer on a virtual network, and the sideband WebSocket) that the `codexrtc` transport tests use |

`agent-cli/internal/webmcp/hermetic` is production code: the probe's default
hermetic browser executor replays browser-script fixtures through it and
records redacted browser evidence with it.
