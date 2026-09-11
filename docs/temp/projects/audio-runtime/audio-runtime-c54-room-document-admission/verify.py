#!/usr/bin/env python3
"""Verify the committed C54 evidence without rerunning the expensive cases."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import sys
from typing import Any, Iterator


HERE = Path(__file__).resolve().parent
ROOT = Path(subprocess.run(["git", "rev-parse", "--show-toplevel"], cwd=HERE, check=True, capture_output=True, text=True).stdout.strip())
SUMMARY = HERE / "verification-summary.json"
PROVENANCE = HERE / "provenance.json"
INVENTORY = HERE / "admission-inventory.json"
BASE_REVISION = "2456a5d1594e73faf85e1132d6050735bc3e4710"
INTEGRATION_REVISION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
BASELINE_REVISION = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
BRANCH = "codex/audio-runtime-c54-room-document-admission"
MAX_CHILD_SECONDS = 60.0
MAX_AGGREGATE_SECONDS = 600.0
MAX_CHILD_OUTPUT_BYTES = 64 * 1024
MAX_RETAINED_DISK_BYTES = 8 * 1024 * 1024
EXPECTED_PCM_SHA = "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502"
EXPECTED_FIXTURE_SHA = "38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169"


class VerificationError(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationError(message)


def load(path: Path) -> dict[str, Any]:
    require(path.is_file() and not path.is_symlink(), f"missing committed evidence: {path}")
    value = json.loads(path.read_text(encoding="utf-8"))
    require(isinstance(value, dict), f"evidence is not a JSON object: {path}")
    return value


def git(*args: str) -> str:
    return subprocess.run(["git", *args], cwd=ROOT, check=True, capture_output=True, text=True, timeout=20).stdout.strip()


def ancestor(older: str, newer: str) -> bool:
    return subprocess.run(["git", "merge-base", "--is-ancestor", older, newer], cwd=ROOT, check=False, timeout=20).returncode == 0


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def executions(value: Any) -> Iterator[dict[str, Any]]:
    if isinstance(value, dict):
        execution = value.get("execution")
        if isinstance(execution, dict) and "label" in execution:
            yield execution
        for child in value.values():
            yield from executions(child)
    elif isinstance(value, list):
        for child in value:
            yield from executions(child)


def check_execution(value: dict[str, Any], label: str, *, success: bool, output_cap: bool = True, timeout_expected: bool = False) -> None:
    require(value.get("label") == label, f"execution label mismatch: {label}")
    require(int(value.get("elapsed_ms", 10**9)) <= MAX_CHILD_SECONDS * 1000, f"{label} exceeded the child budget")
    require(value.get("cleanup", {}).get("parent_reaped") is True, f"{label} did not reap its parent")
    require(value.get("cleanup", {}).get("reader_threads_joined") is True, f"{label} left an output reader")
    require(value.get("cleanup", {}).get("group_alive_after") is False, f"{label} left a process-group survivor")
    require(value.get("disk_bounded") is True and int(value.get("disk_bytes", 10**18)) <= MAX_RETAINED_DISK_BYTES, f"{label} exceeded the retained-disk cap")
    if timeout_expected:
        require(value.get("timed_out") is True and value.get("returncode") != 0, f"{label} did not fail by timeout")
        require(value.get("output_bounded") is True, f"{label} exceeded the output cap")
    elif success:
        require(value.get("returncode") == 0 and value.get("timed_out") is False, f"{label} did not succeed")
        if output_cap:
            require(value.get("output_bounded") is True, f"{label} exceeded the output cap")
            require(int(value.get("stdout_bytes", 10**18)) <= MAX_CHILD_OUTPUT_BYTES and int(value.get("stderr_bytes", 10**18)) <= MAX_CHILD_OUTPUT_BYTES, f"{label} exceeded the observed output cap")
    else:
        require(value.get("returncode") != 0 and value.get("timed_out") is False, f"{label} did not fail causally")
        if output_cap:
            require(value.get("output_bounded") is True, f"{label} failed by overflowing output")


def check_scope(provenance: dict[str, Any], final: bool) -> None:
    head = git("rev-parse", "HEAD")
    require(git("branch", "--show-current") == BRANCH, "current branch is not the admitted C54 branch")
    candidate = provenance.get("candidate_revision")
    require(isinstance(candidate, str) and ancestor(candidate, head), "final HEAD does not descend from the recorded candidate revision")
    require(provenance.get("source_revision") == BASE_REVISION, "source revision is not the admitted baseline")
    require(provenance.get("admitted_base_revision") == BASE_REVISION, "admitted baseline changed")
    require(provenance.get("integration_revision") == INTEGRATION_REVISION, "required integration revision changed")
    require(provenance.get("baseline_revision") == BASELINE_REVISION, "required predecessor baseline changed")
    require(ancestor(BASE_REVISION, candidate), "candidate is not an admitted-baseline descendant")
    require(ancestor(INTEGRATION_REVISION, candidate), "candidate does not preserve required startup integration ancestry")
    require(ancestor(BASELINE_REVISION, candidate), "candidate does not preserve required predecessor ancestry")
    origin = provenance.get("fresh_fetch_origin_main_revision")
    require(isinstance(origin, str) and ancestor(BASE_REVISION, origin), "freshly fetched origin/main is not a recorded baseline descendant")
    require(provenance.get("source_tree_status") == "", "candidate source tree was not clean when evidence ran")
    require(provenance.get("ancestry") == {"base": True, "integration": True, "baseline": True}, "recorded ancestry is incomplete")
    scope = provenance.get("scope", {})
    require(scope.get("wire_gen_unchanged") is True, "generated room wire changed")
    wire = ROOT / "go-agent-runtime/services/rooms/wire/wire_gen.go"
    require(sha256(wire) == scope.get("wire_gen_baseline_sha256"), "generated room wire hash changed")
    owned = {
        "agent-cli/internal/room/manifest.go",
        "agent-cli/internal/room/manifest_test.go",
        "agent-cli/internal/room/browser_tools.go",
        "agent-cli/internal/room/manifest_browser_tools_test.go",
        "go-agent-runtime/services/rooms/manifest.go",
        "go-agent-runtime/services/rooms/browser.go",
        "go-agent-runtime/services/rooms/internal/manifest/decoder.go",
        "go-agent-runtime/services/rooms/internal/manifest/browser.go",
        "go-agent-runtime/services/rooms/wire/manifest.go",
        "go-agent-runtime/services/rooms/wire/manifest_test.go",
        "docs/architecture/architecture-size-baseline.json",
    }
    evidence_prefix = "docs/temp/projects/audio-runtime/audio-runtime-c54-room-document-admission/"
    changed = [line for line in git("diff", f"{BASE_REVISION}...{head}", "--name-only").splitlines() if line]
    require(changed, "candidate diff is empty")
    require(all(path in owned or path.startswith(evidence_prefix) for path in changed), f"candidate diff escaped admitted ownership: {changed}")
    require("go-agent-runtime/services/rooms/wire/wire_gen.go" not in changed, "generated room wire is in the candidate diff")
    if final:
        require(git("status", "--porcelain", "--untracked-files=all") == "", "final source tree is not clean")


def check_inputs(provenance: dict[str, Any]) -> None:
    for item in provenance.get("build", {}).get("inputs", []):
        path = ROOT / item["path"]
        require(path.is_file(), f"build input disappeared: {item['path']}")
        require(path.stat().st_size == item["bytes"] and sha256(path) == item["sha256"], f"build input changed: {item['path']}")


def check_consumer(cases: dict[str, Any], source: str, candidate: str) -> None:
    check_execution(cases["consumer"]["execution"], "build-external-consumer", success=True)
    check_execution(cases["consumer_positive"]["execution"], "public-external-consumer", success=True)
    report = cases["consumer_positive"]["report"]
    require(report["schema"] == "audio-runtime.c54.public-admission/v1", "consumer schema changed")
    require(report["candidate_revision"] == candidate and report["source_revision"] == source, "consumer provenance changed")
    require(report["constructed_via"] == "rooms/wire.NewManifestProviderFromRegistry", "consumer bypassed the public wire provider")
    require(all(report[name] for name in ("json_yaml_equal", "file_admission", "admit_aliases", "registry_snapshot_isolated")), "consumer positive matrix changed")
    normalized = report["normalized"]
    require(normalized["max_turns"] == 3 and normalized["max_duration"] == "30s" and normalized["recording_directory"] == "/tmp/c54-evidence", "consumer room normalization changed")
    require(normalized["participant_kinds"] == ["agent", "agent"] and normalized["participant_ids"] == ["alpha", "beta"], "consumer participant normalization changed")
    require(normalized["provider"] == "openai" and normalized["model"] == "gpt-realtime" and normalized["tools"] == ["sleep"], "consumer registry normalization changed")
    require(normalized["browser_cdp_url"] == "http://127.0.0.1:9222/json/version" and normalized["browser_ws_path"] == "ws://127.0.0.1:9222/%3Credacted%3E", "consumer endpoint redaction changed")
    require(report["typed_errors"]["missing_credential_value"] == "" and report["typed_errors"]["missing_credential_field"] == "participants[1].api_key_env", "consumer credential error changed")
    require(report["typed_errors"]["unknown_nested_field"] == "document" and report["typed_errors"]["unsafe_endpoint_field"] == "participants[0].browserTools.connection.cdp_url", "consumer browser error attribution changed")
    require(all(report["redaction"].values()) and report["lifecycle"] == {"validated": True, "closed": True}, "consumer redaction/lifecycle matrix changed")
    for name, execution in cases["consumer_tests"].items():
        check_execution(execution, f"external-consumer-{name}", success=True)
    for control in cases["negative_controls"]["controls"]:
        check_execution(control["execution"], f"consumer-wrong-{control['name']}-oracle", success=False)


def check_replay(cases: dict[str, Any]) -> None:
    replay = cases["replay"]
    check_execution(replay["execution"], "shipped-yui-audio-tool-replay", success=True)
    require(replay["fixture"]["sha256"] == EXPECTED_FIXTURE_SHA and replay["record"]["pcm_sha256"] == EXPECTED_PCM_SHA, "shipped replay fixture/effect hash changed")
    require(replay["record"]["pcm_bytes"] == 4800 and replay["record"]["artifact_count"] == 5, "shipped replay artifact bounds changed")
    require(replay["effects"] == {"tool_marker": "PROBE_TOOL_MARKER_9182", "response_text": "strict replay continuation", "clean_shutdown": True, "physical_device": "not_attempted", "acoustic": "not_attempted"}, "shipped replay effects changed")


def check_all(cases: dict[str, Any], source: str, candidate: str) -> None:
    check_execution(cases["baseline"]["execution"], "baseline-without-c54-provider", success=False)
    check_consumer(cases, source, candidate)
    check_execution(cases["yui"]["execution"], "build-shipped-yui", success=True)
    admission = cases["yui_admission"]
    check_execution(admission["execution"], "shipped-yui-room-admission", success=False)
    require(admission["expected_error"] == "field unknown not found in a participant's browserTools", "YUI admission error changed")
    require(admission["delegation"] == {"source_has_runtime_provider": True, "cli_parser_absent": True, "runtime_raw_decoder_present": True, "observable_typed_error_surface": "participant-qualified strict browser field error"}, "CLI/runtime delegation proof changed")
    check_replay(cases)
    for name, execution in cases["focused"].items():
        check_execution(execution, f"focused-{name}", success=True)
    cleanup = cases["cleanup_controls"]
    timeout = cleanup["forced_timeout"]["execution"]
    check_execution(timeout, "forced-timeout-process-group", success=False, timeout_expected=True)
    require(timeout["timed_out"] and timeout["cleanup"]["term_sent"] and timeout["cleanup"]["kill_sent"], "forced timeout did not prove escalation")
    overflow = cleanup["overflow"]["execution"]
    check_execution(overflow, "bounded-output-overflow", success=True, output_cap=False)
    require(overflow["output_bounded"] is False and overflow["stdout_bytes"] > MAX_CHILD_OUTPUT_BYTES, "overflow control did not cross the output cap")
    require(sum(execution["elapsed_ms"] for execution in executions(cases)) <= MAX_AGGREGATE_SECONDS * 1000, "child execution sum exceeded the aggregate budget")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=["evidence", "final-scope-provenance-and-budget"], default="final-scope-provenance-and-budget")
    args = parser.parse_args()
    summary = load(SUMMARY)
    provenance = load(PROVENANCE)
    inventory = load(INVENTORY)
    require(summary.get("schema") == "audio-runtime.c54.verification-summary.v1" and summary.get("passed") is True, "verification summary is not a passing C54 summary")
    require(provenance.get("schema") == "audio-runtime.c54.provenance.v1", "provenance schema changed")
    require(inventory.get("task") == "audio-runtime-c54-room-document-admission" and inventory.get("branch") == BRANCH, "admission inventory does not identify C54")
    require(summary.get("candidate_revision") == provenance.get("candidate_revision") and summary.get("source_revision") == provenance.get("source_revision"), "summary/provenance revision mismatch")
    require(provenance.get("bounds", {}).get("child_timeout_seconds") == MAX_CHILD_SECONDS and provenance.get("bounds", {}).get("aggregate_timeout_seconds") == MAX_AGGREGATE_SECONDS, "recorded evidence bounds changed")
    require(provenance.get("bounds", {}).get("max_child_output_bytes") == MAX_CHILD_OUTPUT_BYTES and provenance.get("bounds", {}).get("max_retained_disk_bytes") == MAX_RETAINED_DISK_BYTES, "recorded output/disk caps changed")
    check_scope(provenance, args.mode == "final-scope-provenance-and-budget")
    check_inputs(provenance)
    check_all(summary["cases"], provenance["source_revision"], provenance["candidate_revision"])
    encoded = json.dumps({"summary": summary, "provenance": provenance, "inventory": inventory}, sort_keys=True).lower()
    for marker in ("c54-secret-value-must-not-escape", "cdp-secret", "ws-secret", "browser-secret", "openai_api_key", "authorization:"):
        require(marker not in encoded, f"committed evidence contains forbidden secret marker: {marker}")
    print(json.dumps({"schema": "audio-runtime.c54.final-gate/v1", "mode": args.mode, "passed": True, "candidate_revision": provenance["candidate_revision"], "head": git("rev-parse", "HEAD")}, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (VerificationError, subprocess.SubprocessError, OSError, json.JSONDecodeError) as error:
        print(f"verification failure: {error}", file=sys.stderr)
        raise SystemExit(1)
