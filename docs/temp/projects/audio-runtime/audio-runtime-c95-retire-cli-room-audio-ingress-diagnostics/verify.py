#!/usr/bin/env python3
"""Verify the C95 public room-audio diagnostics boundary and causal oracles."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile


EVIDENCE = Path(__file__).resolve().parent
REPO_ROOT = Path(
    subprocess.run(
        ["rtk", "proxy", "git", "rev-parse", "--show-toplevel"],
        cwd=EVIDENCE,
        check=True,
        capture_output=True,
        text=True,
    ).stdout.strip()
).resolve()
RUNTIME_ROOT = REPO_ROOT / "go-agent-runtime"
PUBLIC_ROOT = RUNTIME_ROOT / "services" / "roomaudiodiagnostics"
INTERNAL_SOURCE = PUBLIC_ROOT / "internal" / "service" / "service.go"
LEGACY_SOURCE = REPO_ROOT / "agent-cli/internal/services/internal/agentruntime/session_room_audio_diagnostics.go"
BASELINE = "3d3e72786ac6fc1fd47c7e029589e5117674b035"
CURRENT_MAIN = "origin/main"
STARTUP = "8bdafc7f947a3a2c9856220abdc539437035bd21"
MANIFEST_BASELINE = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
LEGACY_PATH = "agent-cli/internal/services/internal/agentruntime/session_room_audio_diagnostics.go"
LEGACY_BASELINE_LINES = 472
LEGACY_BASELINE_SHA256 = "02a7b14d7104329fc71f8f8f97dc59fa26830d2f48dc29283a1a933455dff9c9"
MAX_ADAPTER_LINES = 172


class VerificationError(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationError(message)


def run(argv: list[str], cwd: Path, *, expected: int = 0, env: dict[str, str] | None = None) -> str:
    result = subprocess.run(
        argv,
        cwd=cwd,
        env=env,
        check=False,
        capture_output=True,
        text=True,
        timeout=180,
    )
    output = result.stdout + result.stderr
    if result.returncode != expected:
        raise VerificationError(f"{argv!r} returned {result.returncode}, want {expected}: {output[-4000:]}")
    return output


def sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def git(*args: str) -> str:
    return run(["rtk", "proxy", "git", *args], REPO_ROOT).strip()


def verify_scope() -> dict[str, object]:
    branch = git("branch", "--show-current")
    prd = json.loads((REPO_ROOT / "prd.json").read_text(encoding="utf-8"))
    require(branch == prd["branchName"], f"branch mismatch: {branch!r} != {prd['branchName']!r}")
    require(branch == "codex/audio-runtime-c95-retire-cli-room-audio-ingress-diagnostics", "unexpected admitted branch")
    candidate = git("rev-parse", "HEAD")
    require(run(["rtk", "proxy", "git", "merge-base", "--is-ancestor", CURRENT_MAIN, candidate], REPO_ROOT, expected=0) == "", "current origin/main ancestry missing")
    require(run(["rtk", "proxy", "git", "merge-base", "--is-ancestor", STARTUP, candidate], REPO_ROOT, expected=0) == "", "startup ancestry missing")
    require(run(["rtk", "proxy", "git", "merge-base", "--is-ancestor", BASELINE, candidate], REPO_ROOT, expected=0) == "", "planning-main ancestry missing")
    baseline = run(["rtk", "proxy", "git", "show", f"{BASELINE}:{LEGACY_PATH}"], REPO_ROOT)
    require(len(baseline.splitlines()) == LEGACY_BASELINE_LINES, "admitted legacy line count changed")
    require(sha256_bytes(baseline.encode()) == LEGACY_BASELINE_SHA256, "admitted legacy source hash changed")
    current = LEGACY_SOURCE.read_text(encoding="utf-8")
    require(len(current.splitlines()) <= MAX_ADAPTER_LINES, f"legacy adapter is {len(current.splitlines())} lines")
    retired = (
        "roomAudioIngressAdmission",
        "roomAudioIngressContribution",
        "roomAudioIngressEntry",
        "roomAudioIngressTotals",
        "consumeFrame",
        "recordLocked",
    )
    require(all(symbol not in current for symbol in retired), "private ledger implementation remains in the CLI adapter")
    require("runtimeDiagnosticsWire.NewService" in current, "CLI adapter is not wired to the public service")
    require((PUBLIC_ROOT / "wire" / "wire_gen.go").is_file(), "generated public Wire graph is missing")
    public_sources = [path for path in PUBLIC_ROOT.rglob("*.go") if "_test.go" not in path.name]
    public_text = "\n".join(path.read_text(encoding="utf-8") for path in public_sources)
    require("agent-cli/internal" not in public_text and "internal/room" not in public_text, "public package imports CLI or mixer internals")
    # The task must include the fetched main, but the ownership audit should
    # inspect only the task delta after that merge rather than replaying all
    # unrelated paths introduced by main since the planning snapshot.
    changed = [path for path in git("diff", f"{CURRENT_MAIN}...{candidate}", "--name-only").splitlines() if path]
    owned = {
        LEGACY_PATH,
        "agent-cli/internal/services/internal/agentruntime/session_room_audio_diagnostics_test.go",
        "go-agent-runtime/services/roomaudiodiagnostics/service.go",
        "go-agent-runtime/services/roomaudiodiagnostics/errors.go",
        "go-agent-runtime/services/roomaudiodiagnostics/service_test.go",
        "go-agent-runtime/services/roomaudiodiagnostics/internal/service/service.go",
        "go-agent-runtime/services/roomaudiodiagnostics/internal/service/service_test.go",
        "go-agent-runtime/services/roomaudiodiagnostics/wire/providers.go",
        "go-agent-runtime/services/roomaudiodiagnostics/wire/wire_gen.go",
        "go-agent-runtime/services/roomaudiodiagnostics/wire/wire_test.go",
        "coverage-manifest/go-agent-runtime/services/roomaudiodiagnostics/package.json",
        "coverage-manifest/go-agent-runtime/services/roomaudiodiagnostics/internal/service/package.json",
        "coverage-manifest/go-agent-runtime/services/roomaudiodiagnostics/wire/package.json",
    }
    evidence_prefix = "docs/temp/projects/audio-runtime/audio-runtime-c95-retire-cli-room-audio-ingress-diagnostics/"
    require(changed and all(path in owned or path.startswith(evidence_prefix) for path in changed), f"diff escaped C95 ownership: {changed}")
    require("scripts/wire-packages.txt" not in changed, "C79-owned Wire registry was changed")
    require("docs/architecture/architecture-size-baseline.json" not in changed, "C79-owned architecture baseline was changed")
    return {
        "candidate": candidate,
        "branch": branch,
        "legacy_baseline_lines": len(baseline.splitlines()),
        "legacy_baseline_sha256": LEGACY_BASELINE_SHA256,
        "adapter_lines": len(current.splitlines()),
        "changed_paths": changed,
        "manifest_baseline": MANIFEST_BASELINE,
    }


MUTATION_TEST = r'''package service

import (
	"os"
	"testing"

	rd "example.com/c95/roomaudiodiagnostics"
)

type oracleSink struct{ records []rd.Record }

func (s *oracleSink) Record(record rd.Record) { s.records = append(s.records, record) }

func oracleSummary(sink *oracleSink) map[string]string {
	for _, record := range sink.records {
		if record.Event == rd.EventRoomAudioIngressSummary {
			return record.Fields
		}
	}
	return nil
}

func TestCausalOracle(t *testing.T) {
	mode := os.Getenv("C95_MUTATION")
	sink := &oracleSink{}
	service := New(rd.Options{ParticipantID: "oracle", Sink: sink})
	switch mode {
	case "pending-loss":
		if err := service.Admit("alice", rd.Delivered, "admitted", 4, true); err != nil { t.Fatal(err) }
		if err := service.Admit("alice", rd.Delivered, "silent", 3, false); err != nil { t.Fatal(err) }
		service.Finish()
		fields := oracleSummary(sink)
		if fields[rd.FieldContentfulBytes] != "4" || fields[rd.FieldRejectedBytes] != "4" { t.Fatalf("pending oracle = %v", fields) }
	case "source-collapse":
		if err := service.Admit("alice", rd.Delivered, "admitted", 3, true); err != nil { t.Fatal(err) }
		if err := service.Admit("bob", rd.Delivered, "admitted", 3, true); err != nil { t.Fatal(err) }
		service.ResolveFrame([]string{"bob", "alice"}, 3, rd.ReasonParticipantOutputRejected)
		service.Finish()
		seen := map[string]bool{}
		for _, record := range sink.records { if record.Event == rd.EventRoomAudioIngress { seen[record.Fields[rd.FieldSourcePeer]] = true } }
		if !seen["alice"] || !seen["bob"] { t.Fatalf("source oracle = %v", seen) }
	case "rejected-counted-accepted":
		if err := service.Record("alice", rd.Rejected, "rejected", 3); err != nil { t.Fatal(err) }
		service.Finish()
		fields := oracleSummary(sink)
		if fields[rd.FieldAcceptedBytes] != "0" || fields[rd.FieldRejectedBytes] != "3" { t.Fatalf("rejection oracle = %v", fields) }
	default:
		t.Fatalf("unknown mutation mode %q", mode)
	}
}
'''


def mutation_candidate(source: str, mode: str) -> str:
    replacements = {
        "pending-loss": ("if !item.contentful {", "if false {"),
        "source-collapse": (
            "fields[roomaudiodiagnostics.FieldSourcePeer] = entryKey.sourcePeer",
            "fields[roomaudiodiagnostics.FieldSourcePeer] = roomaudiodiagnostics.MixedSource",
        ),
        "rejected-counted-accepted": (
            "s.totals.rejected.bytes += uint64(byteCount)\n\t\ts.totals.rejected.frames++",
            "s.totals.delivered.bytes += uint64(byteCount)\n\t\ts.totals.delivered.frames++",
        ),
    }
    old, new = replacements[mode]
    require(source.count(old) == 1, f"mutation target for {mode} is not unique")
    return source.replace(old, new, 1)


def run_mutations() -> list[dict[str, object]]:
    public_service = (PUBLIC_ROOT / "service.go").read_text(encoding="utf-8")
    public_errors = (PUBLIC_ROOT / "errors.go").read_text(encoding="utf-8")
    internal_service = INTERNAL_SOURCE.read_text(encoding="utf-8")
    results: list[dict[str, object]] = []
    for mode in ("pending-loss", "source-collapse", "rejected-counted-accepted"):
        with tempfile.TemporaryDirectory(prefix="audio-runtime-c95-") as directory:
            root = Path(directory)
            package_root = root / "roomaudiodiagnostics"
            internal_root = package_root / "internal" / "service"
            internal_root.mkdir(parents=True)
            (root / "go.mod").write_text("module example.com/c95\n\ngo 1.26.7\n", encoding="utf-8")
            (package_root / "service.go").write_text(public_service, encoding="utf-8")
            (package_root / "errors.go").write_text(public_errors, encoding="utf-8")
            original = internal_service.replace(
                "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomaudiodiagnostics",
                "example.com/c95/roomaudiodiagnostics",
            )
            (internal_root / "service.go").write_text(original, encoding="utf-8")
            (internal_root / "mutation_test.go").write_text(MUTATION_TEST, encoding="utf-8")
            env = os.environ.copy()
            env["GOWORK"] = "off"
            env["C95_MUTATION"] = mode
            positive = run(
                ["rtk", "proxy", "env", "GOWORK=off", "go", "test", "./roomaudiodiagnostics/internal/service", "-run", "TestCausalOracle", "-count=1"],
                root,
                env=env,
            )
            require("ok" in positive, f"positive causal oracle did not pass for {mode}: {positive[-4000:]}")
            mutated = mutation_candidate(internal_service, mode).replace(
                "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomaudiodiagnostics",
                "example.com/c95/roomaudiodiagnostics",
            )
            (internal_root / "service.go").write_text(mutated, encoding="utf-8")
            result = subprocess.run(
                ["rtk", "proxy", "env", "GOWORK=off", "go", "test", "./roomaudiodiagnostics/internal/service", "-run", "TestCausalOracle", "-count=1"],
                cwd=root,
                env=env,
                check=False,
                capture_output=True,
                text=True,
                timeout=180,
            )
            output = result.stdout + result.stderr
            require(result.returncode != 0 and "--- FAIL: TestCausalOracle" in output, f"{mode} did not compile then fail its oracle: {output[-4000:]}")
            results.append({"mutation": mode, "positive": "passed", "mutated": "compiled_and_failed_oracle"})
    return results


def positive_and_mutations() -> dict[str, object]:
    runtime_test = run(
        ["rtk", "proxy", "env", "GOWORK=off", "go", "test", "./services/roomaudiodiagnostics/...", "-count=1", "-timeout=180s"],
        RUNTIME_ROOT,
    )
    consumer_dir = EVIDENCE / "external-consumer"
    consumer_test = run(
        ["rtk", "proxy", "env", "GOWORK=off", "go", "test", "./...", "-count=1", "-timeout=180s"],
        consumer_dir,
    )
    return {
        "runtime_tests": "passed" if "FAIL" not in runtime_test else "unexpected failure",
        "external_consumer": "passed" if "FAIL" not in consumer_test else "unexpected failure",
        "mutations": run_mutations(),
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True, choices=("positive-and-three-mutations", "retirement-and-owned-paths"))
    args = parser.parse_args()
    try:
        if args.mode == "positive-and-three-mutations":
            result = positive_and_mutations()
        else:
            result = verify_scope()
        print(json.dumps({"schema": "audio-runtime.c95.verification.v1", "mode": args.mode, "passed": True, "result": result}, sort_keys=True))
        return 0
    except (OSError, subprocess.SubprocessError, VerificationError) as error:
        print(json.dumps({"schema": "audio-runtime.c95.verification.v1", "mode": args.mode, "passed": False, "error": str(error)}, sort_keys=True))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
