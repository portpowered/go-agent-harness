# Release

The repository publishes one GitHub release for prebuilt `yui` binaries and
three Go module tags for customer imports.

## Version Tags

For `v0.0.2`, publish these tags from the same commit:

```text
v0.0.2
agent-cli/v0.0.2
go-agent-loop/v0.0.2
go-llm-gateway/v0.0.2
```

The root `v0.0.2` tag drives the GitHub release and GoReleaser artifacts. The
module-prefixed tags are the Go module versions customers use with `go get` and
`go install`.

## Local Release Flow

```bash
make ci
make release-dry-run
make release-tags RELEASE_VERSION=v0.0.2
make release-push RELEASE_VERSION=v0.0.2
```

Pushing the root `v0.0.2` tag triggers `.github/workflows/release.yml`, which
runs GoReleaser and uploads cross-platform `yui` binaries to the GitHub
release.

## Customer Install Commands

```bash
go get github.com/portpowered/go-agent-harness/go-agent-loop@v0.0.2
go get github.com/portpowered/go-agent-harness/go-llm-gateway@v0.0.2
go install github.com/portpowered/go-agent-harness/agent-cli/cmd/yui@v0.0.2
```
