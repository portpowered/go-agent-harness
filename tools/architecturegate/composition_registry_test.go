package main

import (
	"context"
	"testing"
)

func TestExternalServicesPathIsNotTreatedAsLocalPeer(t *testing.T) {
	module := &Module{Dir: "/repo", Path: "example.com/app"}
	policy := fixturePolicy()
	file := sourceFixture(t, "external.go", `package other
import "example.com/vendor/services/third/internal/impl"
`, false)
	pkg := &Package{ImportPath: "example.com/app/other", Dir: "/repo/other", Module: module, Files: []*SourceFile{file}}
	issues := architectureIssues(pkg, module, classifyService(pkg, module, policy), policy)
	if hasRule(issues, "peer-private-import") || hasRule(issues, "wire-import") {
		t.Fatalf("external service-shaped dependency was treated as local: %#v", issues)
	}
}

func TestCompositionRegistryAllowsOnlyExactHostTestSource(t *testing.T) {
	module := &Module{Dir: "/repo", Path: "example.com/app"}
	policy := fixturePolicy()
	policy.CompositionRegistry = []CompositionEntry{{
		Module: module.Path, Package: module.Path + "/services/host",
		File: "services/host/host_test.go", Reason: "host assembles the public service wire",
	}}
	file := sourceFixtureAt(t, "/repo/services/host/host_test.go", `package host
import "example.com/app/services/peer/wire"
var _ = wire.NewService
`, true)
	file.RelPath = "services/host/host_test.go"
	pkg := &Package{ImportPath: module.Path + "/services/host", Dir: "/repo/services/host", Module: module, Files: []*SourceFile{file}}
	service := classifyService(pkg, module, policy)
	if service.Role != roleRoot {
		t.Fatalf("file-scoped composition entry elevated package: %#v", service)
	}
	issues := importIssues(pkg, module, service, file, "example.com/app/services/peer/wire", policy)
	if hasRule(issues, "wire-import") {
		t.Fatalf("registered host test could not assemble peer wire: %#v", issues)
	}

	file.RelPath = "services/host/other_test.go"
	issues = importIssues(pkg, module, service, file, "example.com/app/services/peer/wire", policy)
	if !hasRule(issues, "wire-import") {
		t.Fatalf("unregistered test source was allowed to import peer wire: %#v", issues)
	}
}

func TestCompositionRegistryMarksExternalConsumerPackage(t *testing.T) {
	module := &Module{Dir: "/repo/tests/embedding", Path: "example.com/agent-runtime-consumer"}
	pkg := &Package{ImportPath: module.Path, Dir: module.Dir, Module: module}
	policy := fixturePolicy()
	policy.CompositionRegistry = []CompositionEntry{{
		Module: module.Path, Package: module.Path,
		Reason: "external consumer owns application composition",
	}}
	if got := classifyService(pkg, module, policy); got.Role != roleComposition {
		t.Fatalf("external consumer was not classified as composition: %#v", got)
	}
}

func TestCompositionRegistryRejectsWildcardsAndNonTestFiles(t *testing.T) {
	for _, entry := range []CompositionEntry{
		{Module: "example.com/app/*", Package: "example.com/app/host", Reason: "wildcard module"},
		{Module: "example.com/app", Package: "example.com/app/*", Reason: "wildcard package"},
		{Module: "example.com/app", Package: "example.com/app/host", File: "host.go", Reason: "production file"},
		{Module: "example.com/app", Package: "example.com/app/host", File: "../host_test.go", Reason: "path traversal"},
		{Module: "example.com/app", Package: "example.com/app/host", File: "/host_test.go", Reason: "absolute path"},
	} {
		policy := fixturePolicy()
		policy.CompositionRegistry = []CompositionEntry{entry}
		if err := validatePolicy(policy); err == nil {
			t.Fatalf("composition entry %#v was accepted", entry)
		}
	}
}

func TestInventoryIncludesUntrackedSourceViolations(t *testing.T) {
	// The inventory reads the selected module source through go list; it must
	// inspect a newly created file even when no Git index entry exists yet.
	root := t.TempDir()
	writeFixture(t, root, "go.mod", "module example.com/untracked\n\ngo 1.26.7\n")
	writeFixture(t, root, "services/session/helpers/new.go", "package helpers\n")
	modules, err := discoverModules(context.Background(), "go", root, []string{"."}, []string{"./..."})
	if err != nil {
		t.Fatal(err)
	}
	result, err := evaluate(context.Background(), modules, fixturePolicy(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !hasRule(result.Issues, "service-shape") {
		t.Fatalf("untracked source was not checked: %#v", result.Issues)
	}
}
