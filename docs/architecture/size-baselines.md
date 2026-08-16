# Go size-lint baselines

The root `.golangci.yml` applies three maintainability ceilings to production
Go files in all three workspace modules. Test files are validated by the
repository's separate Go test targets; the size-lint run uses `run.tests: false`.
The values below are baselines measured with the pinned
golangci-lint v2.3.0 binary; they describe the current admissible maximum,
not a target to grow toward.

| Rule | Configured maximum | Counting semantics | Holder |
| --- | ---: | --- | --- |
| Revive `file-length-limit` | 1,307 physical lines | Every physical line counts. Comments and whitespace-only lines are included (`skip-comments: false`, `skip-blank-lines: false`). | `go-agent-loop/pkg/probe/scenario.go` |
| `funlen` lines | 296 lines | Function lines include comments (`ignore-comments: false`). The analyzer counts the source lines between the function declaration and its closing line. The independent statement dimension is disabled with `statements: -1`. | `defaultModelsConfig` in `agent-cli/internal/config/models.go` |
| `gocognit` | 124 cognitive-complexity points | Uses the pinned gocognit implementation and reports only scores greater than `min-complexity: 124`. | `runAgentLoopSessionWithDurationAdmissionClock` in `agent-cli/internal/services/session_duration.go` |

## Ratchet policy

These values are ceilings, not allocations. A decomposition or cleanup lane may
lower a passing limit after its changes make the lower value pass. No lane may
raise a passing limit, add a path exemption, or add a `nolint` directive to
preserve an oversized file, function, or control-flow graph.
