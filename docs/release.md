# Release

The repository publishes one GitHub release for prebuilt `agent` binaries and
three Go module tags for customer imports.

## Version Tags

For `v0.0.1`, publish these tags from the same commit:

```text
v0.0.1
agent-cli/v0.0.1
go-agent-loop/v0.0.1
go-llm-gateway/v0.0.1
```

The root `v0.0.1` tag drives the GitHub release and GoReleaser artifacts. The
module-prefixed tags are the Go module versions customers use with `go get` and
`go install`.

## Local Release Flow

```bash
make ci
make release-dry-run
make release-tags RELEASE_VERSION=v0.0.1
make release-push RELEASE_VERSION=v0.0.1
```

Pushing the root `v0.0.1` tag triggers `.github/workflows/release.yml`, which
runs GoReleaser and uploads cross-platform `agent` binaries to the GitHub
release.

## Customer Install Commands

```bash
go get github.com/portpowered/go-agent-harness/go-agent-loop@v0.0.1
go get github.com/portpowered/go-agent-harness/go-llm-gateway@v0.0.1
go install github.com/portpowered/go-agent-harness/agent-cli/cmd/agent@v0.0.1
```
