#!/usr/bin/env python3
"""Fail-closed verification for the C115 public and accumulated evidence."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import pathlib
import subprocess
import sys
from typing import Any


HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[4]
TASK = "audio-runtime-c115-centralize-filesystem-audio-codec"
BRANCH = "codex/audio-runtime-c115-centralize-filesystem-audio-codec"
REQUIRED_CASES = {
    "shipped-audio-tool",
    "malformed-truncated",
    "fake-output-overflow",
    "c21-consumption-replay",
    "c50-public-replay",
}


class EvidenceFailure(RuntimeError):
    pass


def sha256(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def read_json(path: pathlib.Path) -> Any:
    if not path.is_file():
        raise EvidenceFailure(f"required evidence is missing: {path}")
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        raise EvidenceFailure(f"invalid JSON evidence {path}: {exc}") from exc


def path_from_reference(value: str) -> pathlib.Path:
    path = pathlib.Path(value)
    return path if path.is_absolute() else HERE / path


def git_value(*args: str) -> str:
    result = subprocess.run(
        ["git", *args],
        cwd=ROOT,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        check=False,
    )
    if result.returncode != 0:
        raise EvidenceFailure(f"git {' '.join(args)} failed: {result.stderr.strip()}")
    return result.stdout.strip()


def require_command(result: dict[str, Any], label: str) -> None:
    for key in ("stdout_path", "stderr_path"):
        path = path_from_reference(str(result.get(key, "")))
        if not path.is_file():
            raise EvidenceFailure(f"{label} omitted {key}: {path}")
    if result.get("timed_out") or result.get("exit_code") != 0 or not result.get("process_group_gone"):
        raise EvidenceFailure(f"{label} did not prove a successful reaped child: {result}")
    for path_key, hash_key in (("stdout_path", "stdout_sha256"), ("stderr_path", "stderr_sha256")):
        path = path_from_reference(str(result[path_key]))
        if sha256(path) != result.get(hash_key):
            raise EvidenceFailure(f"{label} {path_key} hash does not match its recorded digest")


def source_checks() -> list[str]:
    tool = (ROOT / "agent-cli/internal/transport/cli/tool.go").read_text(encoding="utf-8")
    session = (ROOT / "agent-cli/internal/transport/cli/session_capabilities.go").read_text(encoding="utf-8")
    room = (ROOT / "agent-cli/internal/transport/cli/room_browser_capabilities.go").read_text(encoding="utf-8")
    room_command = (ROOT / "agent-cli/internal/transport/cli/room.go").read_text(encoding="utf-8")
    adapter = (ROOT / "go-agent-runtime/services/tools/internal/filesystem/tool_file_tools.go").read_text(encoding="utf-8")
    wire = (ROOT / "agent-cli/internal/wire/wire.go").read_text(encoding="utf-8")
    generated = (ROOT / "agent-cli/internal/wire/wire_gen.go").read_text(encoding="utf-8")
    codec = (ROOT / "go-agent-runtime/services/audiocodec/internal/service/service.go").read_text(encoding="utf-8")
    process = (ROOT / "go-agent-runtime/services/audiocodec/internal/service/process.go").read_text(encoding="utf-8")
    checks = [
        ("tool command has explicit runtime-service constructor", "NewToolCommandWithRuntimeService" in tool),
        ("tool transport has no uncomposed runtime fallback", "runtimeToolsWire.NewService()" not in tool),
        ("session capability service is injected", "runtimeService runtimeTools.Service" in session and "servicewire.NewToolCapabilitiesService" in session),
        ("session transport has no uncomposed runtime construction", "runtimeToolsWire.NewService()" not in session),
        ("room browser adapter forwards runtime service", "runtimeService runtimeTools.Service" in room and "runtimeService," in room),
        ("room command retains runtime service seam", "NewRoomRunCommandWithToolService" in room_command and "c.runtimeToolService" in room_command),
        ("filesystem adapter keeps one injected codec caller", adapter.count("audioToPCM16k(ctx, t.audioCodec, content)") == 1 and adapter.count("func audioToPCM16k(") == 1),
        ("Wire source selects explicit constructors", "cli.NewToolCommandWithRuntimeService" in wire and "cli.NewRoomRunCommandWithToolService" in wire),
        ("generated Wire passes composed service", "cli.NewToolCommandWithRuntimeService(globalFlags, service)" in generated and "cli.NewRoomRunCommandWithToolService(globalFlags, roomsService, service)" in generated),
        ("cleanup errors are returned", "removeTemp func(string) error" in codec and "errors.Join" in codec and "remove temporary input" in codec),
        ("bounded decoder terminates on overflow", "terminate() error" in process and "onOverflow" in process and "cmd.terminate()" in process),
    ]
    failed = [label for label, passed in checks if not passed]
    if failed:
        raise EvidenceFailure("source repair checks failed: " + "; ".join(failed))
    production_roots = [ROOT / "agent-cli/internal/transport/cli", ROOT / "agent-cli/internal/wire"]
    for source_root in production_roots:
        for path in source_root.rglob("*.go"):
            if path.name.endswith("_test.go"):
                continue
            if "runtimeToolsWire.NewService()" in path.read_text(encoding="utf-8"):
                raise EvidenceFailure(f"uncomposed runtime service construction remains in production file {path}")
    return [label for label, _ in checks] + ["production NewService audit is clean"]


def verify() -> dict[str, Any]:
    latest = read_json(HERE / "runs/latest.json")
    if latest.get("schema_version") != "c115-run-v1" or latest.get("task") != TASK or latest.get("decision") != "ACCEPTED":
        raise EvidenceFailure(f"latest run is not an accepted C115 run: {latest.get('decision')}")
    if set(latest.get("cases", {})) != REQUIRED_CASES:
        raise EvidenceFailure(f"latest run cases = {sorted(latest.get('cases', {}))}, want {sorted(REQUIRED_CASES)}")
    current_head = git_value("rev-parse", "HEAD")
    current_branch = git_value("branch", "--show-current")
    if current_branch != BRANCH or latest.get("preflight", {}).get("branch") != BRANCH:
        raise EvidenceFailure(f"branch evidence mismatch: current={current_branch!r}, recorded={latest.get('preflight', {}).get('branch')!r}")
    candidate_revision = latest.get("preflight", {}).get("candidate_revision")
    if candidate_revision != current_head:
        descendant = subprocess.run(["git", "merge-base", "--is-ancestor", str(candidate_revision), "HEAD"], cwd=ROOT, stdin=subprocess.DEVNULL, check=False)
        if descendant.returncode != 0:
            raise EvidenceFailure(f"evidence candidate {candidate_revision} is not an ancestor of current HEAD {current_head}")
        evidence_prefix = str(HERE.relative_to(ROOT)).rstrip("/") + "/"
        later_paths = [path for path in git_value("diff", "--name-only", f"{candidate_revision}..HEAD").splitlines() if path]
        if any(not path.startswith(evidence_prefix) for path in later_paths):
            raise EvidenceFailure(f"current head moved beyond the evidence-only checkpoint: {later_paths}")
    origin_main = git_value("rev-parse", "origin/main")
    if latest.get("preflight", {}).get("origin_main") != origin_main:
        raise EvidenceFailure(f"origin/main moved since evidence run: recorded={latest.get('preflight', {}).get('origin_main')}, current={origin_main}")
    ancestry = subprocess.run(["git", "merge-base", "--is-ancestor", "origin/main", "HEAD"], cwd=ROOT, stdin=subprocess.DEVNULL, check=False)
    if ancestry.returncode != 0:
        raise EvidenceFailure("current origin/main is not an ancestor of current HEAD")
    if latest.get("preflight", {}).get("admission_document", {}).get("status") != "admitted":
        raise EvidenceFailure("latest run omitted admitted task status")
    require_command(latest["preflight"]["admission"], "admission verification")
    require_command(latest["preflight"]["diff_check"], "diff check")

    artifact = latest.get("artifact")
    if not isinstance(artifact, dict):
        raise EvidenceFailure("latest run omitted the shipped artifact record")
    artifact_path = path_from_reference(str(artifact.get("path", "")))
    if not artifact_path.is_file() or sha256(artifact_path) != artifact.get("sha256") or artifact_path.stat().st_size != artifact.get("bytes"):
        raise EvidenceFailure(f"shipped artifact does not match its recorded hash/size: {artifact_path}")
    manifest = read_json(HERE / "artifacts/manifest.json")
    if manifest.get("sha256") != artifact.get("sha256") or manifest.get("source_revision") != candidate_revision or artifact.get("source_revision") != candidate_revision:
        raise EvidenceFailure("artifact manifest is not bound to the current candidate")
    require_command(artifact["build"], "shipped artifact build")

    all_commands: list[tuple[str, dict[str, Any]]] = []
    for case_name in sorted(REQUIRED_CASES):
        case = latest["cases"].get(case_name, {})
        if case.get("passed") is not True:
            raise EvidenceFailure(f"case {case_name} was not marked passed")
        for command in case.get("commands", []):
            all_commands.append((f"{case_name}/{command.get('label', 'command')}", command))
    for label, command in all_commands:
        require_command(command, label)

    shipped = latest["cases"]["shipped-audio-tool"]
    if not shipped.get("positive_audio_read") or not shipped.get("positive_text_marker") or not shipped.get("composed_service_diagnostic_absent"):
        raise EvidenceFailure("shipped positive audio/text regression is incomplete")
    malformed = latest["cases"]["malformed-truncated"]
    if not malformed.get("rejection_observed") or malformed.get("accepted_pcm_receipt"):
        raise EvidenceFailure("malformed/truncated negative regression is incomplete")
    overflow = latest["cases"]["fake-output-overflow"]
    if not overflow.get("cleanup_error_identity_asserted") or not overflow.get("stdout_overflow_termination_asserted") or not overflow.get("stderr_overflow_termination_asserted"):
        raise EvidenceFailure("cleanup/overflow causal assertions are incomplete")
    c21 = latest["cases"]["c21-consumption-replay"]
    if not c21.get("external_consumer", {}).get("sha256") or c21.get("replay", {}).get("raw_pcm", {}).get("sha256") != "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502":
        raise EvidenceFailure("C21 public consumer or replay evidence is incomplete")
    c50 = latest["cases"]["c50-public-replay"]
    if not c50.get("top_level_help", {}).get("required_literals") or not c50.get("session_help", {}).get("required_literals") or not c50.get("public_replay", {}).get("session_log", {}).get("markers_present"):
        raise EvidenceFailure("C50 public help/replay evidence is incomplete")

    checks = source_checks()
    summary = {
        "schema_version": "c115-verification-v1",
        "mode": "public-and-accumulated-regressions",
        "task": TASK,
        "project": "audio-runtime",
        "candidate_revision": current_head,
        "evidence_source_revision": candidate_revision,
        "branch": current_branch,
        "origin_main": origin_main,
        "admission": "admitted",
        "cases": sorted(REQUIRED_CASES),
        "command_count": len(all_commands) + 3,
        "source_checks": checks,
        "changed_paths_forbidden": [],
        "credential_free": True,
        "physical_device_consumption": "UNKNOWN_AND_OUT_OF_SCOPE",
        "acoustic_proof": "OUT_OF_SCOPE_AND_NEVER_PASS",
        "script_ci": "ready for exact current HEAD; CI was not polled or duplicated",
        "independent_review": "not performed by executor",
        "project_acceptance": "not claimed",
        "passes": True,
    }
    return summary


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True, choices=("public-and-accumulated-regressions",))
    args = parser.parse_args()
    try:
        summary = verify()
    except (EvidenceFailure, OSError, subprocess.SubprocessError) as exc:
        summary = {
            "schema_version": "c115-verification-v1",
            "mode": args.mode,
            "task": TASK,
            "passes": False,
            "diagnostic": str(exc),
        }
        (HERE / "verification-summary.json").write_text(json.dumps(summary, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(json.dumps(summary, sort_keys=True))
        return 1
    (HERE / "verification-summary.json").write_text(json.dumps(summary, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps({"status": "ok", "mode": args.mode, "passes": True, "candidate_revision": summary["candidate_revision"]}, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
