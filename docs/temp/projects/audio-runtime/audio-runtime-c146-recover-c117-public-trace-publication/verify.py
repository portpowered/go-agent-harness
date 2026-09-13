#!/usr/bin/env python3
"""Fail-closed admission, provenance, scope, and C146 evidence verifier."""

from __future__ import annotations

import hashlib
import json
import os
import pathlib
import subprocess
import time
from typing import Any


HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[4]
FACTORY_ROOT = pathlib.Path(os.environ.get("FACTORY_ROOT", str(ROOT))).resolve()
FACTORY_SERVER_URL = os.environ.get("FACTORY_SERVER_URL", "")

WORK = "audio-runtime-c146-recover-c117-public-trace-publication"
PROJECT = "audio-runtime"
CONTRACT = "audio-runtime-v1"
ADOPTED_BRANCH = "codex/audio-runtime-c117-repair-c113-public-trace-publication"
BASELINE = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
STARTUP_INTEGRATION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
ACCEPTED_C112 = "b8650efd95f6a2e2e675a3dfd0969a9d311a877b"
PLANNING_MAIN = "4a1c399ccbb3d780be95eb04316e84b8f11a6646"
PRESERVED_C117_HEAD = "ea30e51ed43376e8aea28ae9b741e5d6f9e7adbc"
PULL_REQUEST = 503

C117_REL = "docs/temp/projects/audio-runtime/audio-runtime-c117-repair-c113-public-trace-publication"
C108_REL = "docs/temp/projects/audio-runtime/audio-runtime-c108-characterize-audio-device-boundary-gaps"
C146_REL = str(HERE.relative_to(ROOT))
C108_PROVENANCE = f"{C108_REL}/provenance.json"
C108_VERIFIER = f"{C108_REL}/verify.py"
C108_SUMS = f"{C108_REL}/SHA256SUMS"
C113_REPORT = f"{C117_REL}/runs/c113-reproduction.json"
C141_REPORT = FACTORY_ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c141-c112-session-trace-vertical-probe.json"

C141_REPORT_SHA256 = "de7ed946c447340c60e0092f49d97f28477a2d316973604e1028d4a9dd44a8f9"
C113_FIXTURE_SHA256 = "38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169"
EXPECTED_TOOL_PCM_SHA256 = "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502"

SOURCE_CODE_PATHS = {
    # The livehost package is capped at 15 maintained files and 400 production
    # lines per file. These existing same-package files are structural parts of
    # the C117 host seam, not additional behavior owners; keeping them explicit
    # preserves the fail-closed path check without hiding the architecture split.
    "agent-cli/internal/transport/cli/internal/livehost/events.go",
    "agent-cli/internal/transport/cli/internal/livehost/request_policy.go",
    "agent-cli/internal/transport/cli/internal/livehost/run.go",
    "agent-cli/internal/transport/cli/internal/livehost/run_trace_test.go",
    "agent-cli/internal/transport/cli/internal/livehost/run_helpers.go",
    "agent-cli/internal/transport/cli/internal/livehost/trace_test.go",
    "agent-cli/internal/transport/cli/internal/livehost/trace.go",
    "agent-cli/internal/transport/cli/session_observability.go",
    "agent-cli/internal/transport/cli/session_observability_test.go",
    "agent-cli/internal/wire/wire.go",
    "agent-cli/internal/wire/wire_gen.go",
}
C117_PRESERVED_PATHS = {
    "README.md",
    "admission.json",
    "census.json",
    "c108-source-base-and-checksums.json",
    "pre-c112-scope.json",
    "reproduce_c113.py",
    "verify.py",
    "runs/c113-reproduction.json",
    "runs/ci-rejection-34740329453.json",
    "runs/package-budget-repair.json",
    "runs/pre-c112-test.json",
}


class EvidenceFailure(RuntimeError):
    pass


def sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def sha256_file(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def rel(path: pathlib.Path) -> str:
    try:
        return str(path.resolve().relative_to(HERE.resolve()))
    except ValueError:
        try:
            return str(path.resolve().relative_to(ROOT.resolve()))
        except ValueError:
            return str(path)


def require(condition: bool, message: str) -> None:
    if not condition:
        raise EvidenceFailure(message)


def read_json(path: pathlib.Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise EvidenceFailure(f"missing evidence: {path}") from exc
    except json.JSONDecodeError as exc:
        raise EvidenceFailure(f"invalid JSON evidence {path}: {exc}") from exc


def command(argv: list[str], cwd: pathlib.Path = ROOT, timeout: int = 60) -> dict[str, Any]:
    started = time.monotonic()
    try:
        result = subprocess.run(argv, cwd=cwd, text=True, capture_output=True, timeout=timeout)
    except subprocess.TimeoutExpired as exc:
        return {
            "argv": argv,
            "cwd": rel(cwd),
            "exit_code": None,
            "timed_out": True,
            "elapsed_seconds": round(time.monotonic() - started, 3),
            "stdout": exc.stdout or "",
            "stderr": exc.stderr or "",
        }
    return {
        "argv": argv,
        "cwd": rel(cwd),
        "exit_code": result.returncode,
        "timed_out": False,
        "elapsed_seconds": round(time.monotonic() - started, 3),
        "stdout": result.stdout,
        "stderr": result.stderr,
    }


def git(*args: str) -> str:
    result = command(["git", *args], ROOT, 60)
    require(result["exit_code"] == 0, f"git {' '.join(args)} failed: {result['stderr'].strip()}")
    return result["stdout"].strip()


def git_bytes(revision: str, path: str) -> bytes:
    result = subprocess.run(["git", "show", f"{revision}:{path}"], cwd=ROOT, capture_output=True)
    require(result.returncode == 0, f"git tree lacks {path} at {revision}")
    return result.stdout


def exact_ancestor(ancestor: str, descendant: str) -> bool:
    return subprocess.run(["git", "merge-base", "--is-ancestor", ancestor, descendant], cwd=ROOT).returncode == 0


def status_paths() -> list[str]:
    result = command(["git", "status", "--porcelain=v1", "--untracked-files=all"], ROOT, 30)
    require(result["exit_code"] == 0, "git status failed")
    paths = []
    for line in result["stdout"].splitlines():
        value = line[3:] if len(line) >= 4 else ""
        if " -> " in value:
            value = value.split(" -> ", 1)[1]
        if value:
            paths.append(value.strip().strip('"'))
    return sorted(set(paths))


def changed_paths(first: str, second: str) -> list[str]:
    output = git("diff", "--name-only", first, second)
    return sorted({line.strip() for line in output.splitlines() if line.strip()})


def verify_admission() -> dict[str, Any]:
    result = command(
        [
            "python3",
            str(FACTORY_ROOT / "factory/scripts/project-control.py"),
            "verify-work",
            "--type",
            "task",
            "--name",
            WORK,
        ],
        FACTORY_ROOT,
        60,
    )
    require(result["exit_code"] == 0, f"project-control admission failed: {result['stderr'].strip()}")
    try:
        payload = json.loads(result["stdout"])
    except json.JSONDecodeError as exc:
        raise EvidenceFailure(f"project-control admission was not JSON: {exc}") from exc
    require(payload.get("status") == "admitted" and payload.get("project") == PROJECT, f"unexpected admission: {payload}")

    manifest_path = FACTORY_ROOT / "factory/projects/audio-runtime/manifest.json"
    manifest = read_json(manifest_path)
    require(manifest.get("project") == PROJECT, "admitted manifest selected a different project")
    require(manifest.get("contractRevision") == CONTRACT, "admitted manifest contract drifted")
    require(manifest.get("baselineRevision") == BASELINE, "admitted manifest baseline drifted")
    require(not manifest.get("acceptanceWaiver", False) and not manifest.get("acceptance_waiver", False), "acceptance waiver is not permitted")

    status = command(["python3", str(FACTORY_ROOT / "factory/scripts/project-control.py"), "status", "--root", str(FACTORY_ROOT)], FACTORY_ROOT, 60)
    require(status["exit_code"] == 0, f"project-control status failed: {status['stderr'].strip()}")
    require(STARTUP_INTEGRATION in status["stdout"], "project-control status does not expose the required startup integration")
    require(CONTRACT in status["stdout"] and PROJECT in status["stdout"], "project-control status identity is incomplete")
    return {
        "command": result["argv"],
        "payload": payload,
        "manifest": {
            "path": str(manifest_path),
            "sha256": sha256_file(manifest_path),
            "project": manifest["project"],
            "contractRevision": manifest["contractRevision"],
            "baselineRevision": manifest["baselineRevision"],
            "acceptance_waiver": False,
            "sole_project": True,
        },
        "status": {key: value for key, value in status.items() if key not in {"stdout", "stderr"}},
    }


def verify_board() -> dict[str, Any]:
    require(FACTORY_SERVER_URL, "FACTORY_SERVER_URL is unavailable")
    result = command(
        ["you", "--server", FACTORY_SERVER_URL, "--json", "work", "list", "--session", "~default", "--max-results", "500", "--all"],
        ROOT,
        60,
    )
    require(result["exit_code"] == 0, f"canonical board query failed: {result['stderr'].strip()}")
    try:
        rows = json.loads(result["stdout"]).get("results")
    except json.JSONDecodeError as exc:
        raise EvidenceFailure(f"canonical board was not JSON: {exc}") from exc
    require(isinstance(rows, list), "canonical board has no results list")
    selected = [row for row in rows if row.get("name") == WORK and row.get("workTypeName") == "task"]
    require(len(selected) == 1, f"canonical board does not have exactly one C146 task row: {len(selected)}")
    row = selected[0]
    feedback = row.get("_rejection_feedback")
    require(not feedback, "canonical board contains actionable C146 rejection feedback")
    return {
        "command": result["argv"],
        "payload_sha256": sha256_bytes(result["stdout"].encode()),
        "row": {
            "workId": row.get("workId"),
            "name": row.get("name"),
            "workTypeName": row.get("workTypeName"),
            "state": row.get("state"),
            "worktree": row.get("worktree"),
            "branch": row.get("branch"),
            "baseRevision": row.get("baseRevision"),
            "last_output": row.get("_last_output"),
            "rejection_feedback": feedback,
        },
    }


def verify_branch_and_ancestry() -> dict[str, Any]:
    branch = git("rev-parse", "--abbrev-ref", "HEAD")
    worktree = pathlib.Path(git("rev-parse", "--show-toplevel")).resolve()
    prd = read_json(ROOT / "prd.json")
    require(branch == ADOPTED_BRANCH, f"adopted branch mismatch: {branch}")
    require(prd.get("branchName") == branch, f"prd.branchName does not match adopted branch: {prd.get('branchName')}")
    require(prd.get("project") == PROJECT and prd.get("contractRevision") == CONTRACT, "adopted PRD identity drifted")
    require(worktree == ROOT, f"isolated worktree mismatch: {worktree} != {ROOT}")

    head = git("rev-parse", "HEAD")
    origin_main = git("rev-parse", "origin/main")
    for revision in (BASELINE, STARTUP_INTEGRATION, ACCEPTED_C112, PRESERVED_C117_HEAD, PLANNING_MAIN, origin_main):
        require(exact_ancestor(revision, head), f"candidate is missing required ancestry {revision}")
    require(exact_ancestor(PLANNING_MAIN, origin_main), "fetched origin/main regressed behind the pinned planning main")
    return {
        "branch": branch,
        "worktree": str(worktree),
        "head": head,
        "origin_main": origin_main,
        "required_ancestry": {revision: True for revision in (BASELINE, STARTUP_INTEGRATION, ACCEPTED_C112, PRESERVED_C117_HEAD, PLANNING_MAIN, origin_main)},
    }


def verify_predecessors() -> dict[str, Any]:
    progress = ROOT / "progress.txt"
    require(progress.is_file(), "predecessor progress.txt is missing")
    require(progress.read_bytes() == git_bytes(PRESERVED_C117_HEAD, "progress.txt"), "predecessor progress.txt changed")
    preserved = []
    for relative in sorted(C117_PRESERVED_PATHS):
        path = ROOT / C117_REL / relative
        require(path.is_file(), f"preserved C117 evidence is missing: {relative}")
        expected = git_bytes(PRESERVED_C117_HEAD, f"{C117_REL}/{relative}")
        require(path.read_bytes() == expected, f"preserved C117 evidence changed: {relative}")
        preserved.append({"path": f"{C117_REL}/{relative}", "sha256": sha256_bytes(expected)})

    require(C141_REPORT.is_file(), f"C141 report is missing: {C141_REPORT}")
    require(sha256_file(C141_REPORT) == C141_REPORT_SHA256, "C141 FAILED report changed")
    c141 = read_json(C141_REPORT)
    require(c141.get("decision") == "FAILED", "C141 historical failure was relabeled")

    c113 = read_json(ROOT / C113_REPORT)
    require(c113.get("passes") is True and c113.get("trace", {}).get("present") is False, "C113 missing-trace reproduction was changed")
    require(c113.get("fixture_sha256") == C113_FIXTURE_SHA256, "C113 fixture identity changed")
    require(c113.get("positive_effects") == [
        "PROBE_TOOL_MARKER_9182",
        "strict replay continuation",
        "nonempty provider PCM",
        "nonempty output WAV",
        "five-artifact recording manifest",
        "fixture_complete",
        "provider_close",
    ], "C113 positive defect reproduction effects changed")
    return {
        "progress_sha256": sha256_file(progress),
        "preserved_c117_evidence": preserved,
        "c113": {"path": C113_REPORT, "sha256": sha256_file(ROOT / C113_REPORT), "missing_trace": True},
        "c141": {"path": str(C141_REPORT), "sha256": C141_REPORT_SHA256, "decision": "FAILED"},
    }


def verify_scope() -> dict[str, Any]:
    head = git("rev-parse", "HEAD")
    origin_main = git("rev-parse", "origin/main")
    paths = changed_paths(origin_main, head)
    allowed = SOURCE_CODE_PATHS | {C108_VERIFIER, C108_PROVENANCE, C108_SUMS}
    outside = [
        path
        for path in paths
        if path not in allowed and not path.startswith(C117_REL + "/") and not path.startswith(C146_REL + "/")
    ]
    require(not outside, "candidate changed unowned paths: " + ", ".join(outside))
    current = status_paths()
    require(not current, "candidate is not clean: " + ", ".join(current))
    # The adopted C117 checkpoint contains a preserved README blank line in
    # committed ancestry. Check the candidate working-tree diff here so that
    # inherited evidence is not rewritten or misattributed to C146.
    diff_check = command(["git", "diff", "--check"], ROOT, 60)
    require(diff_check["exit_code"] == 0, "candidate has whitespace errors")
    host_sources = "\n".join(
        (ROOT / path).read_text(encoding="utf-8")
        for path in SOURCE_CODE_PATHS
        if path.startswith("agent-cli/internal/transport/cli/internal/livehost/") and (ROOT / path).is_file()
    )
    require("go-agent-runtime/services/sessiontrace/internal" not in host_sources, "livehost imports a private sessiontrace implementation")
    require("RenderedSamplesUnavailable" in host_sources, "render-unavailable boundary is not explicit")
    return {"changed_paths": paths, "diff_check": {key: value for key, value in diff_check.items() if key not in {"stdout", "stderr"}}}


def verify_c108() -> dict[str, Any]:
    provenance_path = ROOT / C108_PROVENANCE
    sums_path = ROOT / C108_SUMS
    provenance = read_json(provenance_path)
    head = git("rev-parse", "HEAD")
    origin_main = git("rev-parse", "origin/main")
    require(provenance.get("accepted_source_revision") == "d4766c3dbbf2c198142047ead4449d58dd47d485", "C108 immutable source drifted")
    require(provenance.get("integrated_base_revision") == "3963bc3566da24f8214634c17a9d0f79a6724171", "C108 integrated base drifted")
    require(provenance.get("startup_integration_revision") == STARTUP_INTEGRATION, "C108 startup ancestry drifted")
    require(provenance.get("origin_main_revision") == origin_main, "C108 origin/main provenance is stale")
    current_descendant = provenance.get("current_descendant_revision")
    require(isinstance(current_descendant, str) and exact_ancestor(current_descendant, head), "C108 current descendant is not an ancestor of C146")
    require(provenance.get("successor_unexpected_paths") == [], "C108 successor scope contains unexpected paths")
    equivalence = provenance.get("source_equivalence", {})
    require(equivalence.get("immutable_source_to_integrated_base_production_diff") == [], "C108 source/base production equivalence changed")
    require(equivalence.get("analyzed_candidate_to_integrated_base") is True, "C108 analyzed candidate equivalence is not explicit")
    checksums = command(["shasum", "-a", "256", "-c", sums_path.name], sums_path.parent, 120)
    require(checksums["exit_code"] == 0 and not checksums["timed_out"], "C108 SHA256SUMS does not validate")
    return {
        "provenance_sha256": sha256_file(provenance_path),
        "checksums": {key: value for key, value in checksums.items() if key not in {"stdout", "stderr"}},
        "current_descendant": current_descendant,
    }


def process_clean(process: dict[str, Any], expected_exit: int | None = 0) -> None:
    if expected_exit is not None:
        require(process.get("exit_code") == expected_exit, f"unexpected process exit: {process.get('exit_code')}")
    require(not process.get("timed_out") and not process.get("output_limited"), "bounded process timed out or exceeded output cap")
    require(process.get("process_group_gone") is True, "process group survived the bounded run")


def verify_runtime() -> dict[str, Any]:
    report_path = HERE / "runs/report.json"
    report = read_json(report_path)
    head = git("rev-parse", "HEAD")
    require(report.get("task") == WORK and report.get("passes") is True, "C146 aggregate runtime report is not passing")
    require(report.get("candidate_revision") == head, "C146 runtime evidence is stale for the candidate head")
    require(report.get("source_revision") == ACCEPTED_C112, "C146 runtime source pin drifted")
    required_cases = {"c141-simulated-duplex-four-tap", "render-tap-unavailable", "malformed-odd-pcm", "stale-provenance", "healthy-audio-tool", "interruption"}
    require(set(report.get("cases", {})) == required_cases, "C146 runtime matrix is incomplete")
    binary = HERE / "artifacts/yui"
    server_binary = HERE / "artifacts/audio-device-server"
    require(binary.is_file() and server_binary.is_file(), "C146 executable artifacts are missing")
    require(sha256_file(binary) == report.get("artifact_sha256"), "C146 yui artifact hash is stale")
    require(sha256_file(server_binary) == report.get("device_server_sha256"), "C146 device-server artifact hash is stale")

    positive = report["cases"]["c141-simulated-duplex-four-tap"]
    process_clean(positive["process"], 0)
    process_clean(positive["preflight"], None)
    require(positive["preflight"].get("exit_code") != 0, "C141 source preflight unexpectedly accepted the incompatible text-only fixture")
    require(positive.get("credential_free") is True, "positive replay is not credential-free")
    require(positive.get("acoustic_proof") == "OUT_OF_SCOPE_AND_NEVER_PASS" and positive.get("native_hardware") == "OUT_OF_SCOPE_AND_NEVER_PASS", "positive proof-level limits drifted")
    fixture = positive.get("source_fixture", {})
    require(fixture.get("sha256") == "38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169", "positive source fixture hash drifted")
    trace = positive.get("trace", {})
    required_taps = {"microphone_pre_gate", "microphone_uploaded", "speaker_enqueued", "speaker_rendered"}
    require(set(trace.get("taps", [])) == required_taps, "positive trace does not publish all four public taps")
    require(not any(kind == "audio_render_tap_unavailable" for kind in trace.get("runtime_kinds", [])), "callback-capable positive trace reports render unavailable")
    require(trace.get("timing_domains") == {
        "elapsed_ns": "monotonic_session_clock",
        "timestamp": "UTC_wall_clock",
        "audio_samples": "device_or_provider_sample_clock",
    }, "positive trace timing domains are incomplete")
    require({"provider_wire_receive", "provider_wire_send", "audio_input", "audio_output", "terminal"}.issubset(set(trace.get("runtime_kinds", []))), "positive trace correlation kinds are incomplete")
    callback = positive.get("device_callback", {})
    require(callback.get("proof_level") == "SIMULATED_DEVICE_CALLBACK" and callback.get("rendered_samples", 0) > 0 and callback.get("render_event_count", 0) > 0, "simulated render callback evidence is missing")
    recording = positive.get("recording", {})
    artifacts = recording.get("artifacts", [])
    require(len(artifacts) >= 5 and all(len(str(item.get("sha256", ""))) == 64 for item in artifacts), "positive finalized manifest hashes are incomplete")

    render = report["cases"]["render-tap-unavailable"]
    process_clean(render["process"], 0)
    malformed = report["cases"]["malformed-odd-pcm"]
    process_clean(malformed["process"], None)
    require(malformed["process"].get("exit_code") != 0 and malformed.get("accepted_pcm_receipt") is False, "odd-byte PCM negative was accepted")
    stale = report["cases"]["stale-provenance"]
    require(stale.get("rejected") is True and stale.get("passes") is True, "stale provenance negative was accepted")
    healthy = report["cases"]["healthy-audio-tool"]
    process_clean(healthy["process"], 0)
    require(healthy.get("raw_pcm", {}).get("bytes") == 4800 and healthy.get("raw_pcm", {}).get("sha256") == EXPECTED_TOOL_PCM_SHA256, "healthy audio/tool PCM receipt changed")
    interruption = report["cases"]["interruption"]
    process_clean(interruption["process"], 0)
    terminal = interruption.get("recording", {}).get("terminal", {})
    require(terminal.get("reason") == "replay_complete" and terminal.get("output_state") == "complete", "interruption terminal evidence changed")

    forbidden = (b"AGENT_MODEL__OPENAI__API_KEY", b"AGENT_MODEL__GROK__API_KEY", b"OPENAI_API_KEY", b"Authorization: Bearer", b"sk-")
    for path in (HERE / "runs").rglob("*"):
        if not path.is_file() or path.suffix.lower() not in {".json", ".jsonl", ".stdout", ".stderr", ".txt"}:
            continue
        data = path.read_bytes()
        require(not any(marker in data for marker in forbidden), f"credential material or configured credential name leaked into {rel(path)}")
    return {
        "report_sha256": sha256_file(report_path),
        "candidate_revision": head,
        "cases": sorted(required_cases),
        "artifact_sha256": report["artifact_sha256"],
        "proof_limits": {"simulated_callback": "software-only", "native_hardware": "OUT_OF_SCOPE_AND_NEVER_PASS", "acoustics": "OUT_OF_SCOPE_AND_NEVER_PASS"},
    }


def verify_focused_gates() -> dict[str, Any]:
    commands = [
        ["go", "test", "./agent-cli/internal/transport/cli/internal/livehost", "-run", "TestPublicSessionTrace(PublishesRedactedRuntimeAndAudioAfterRecording|MarksUnsupportedRenderBoundary)", "-count=1", "-timeout=120s"],
        ["go", "test", "-race", "./agent-cli/internal/transport/cli/internal/livehost", "-run", "TestPublicSessionTrace(PublishesRedactedRuntimeAndAudioAfterRecording|MarksUnsupportedRenderBoundary)", "-count=1", "-timeout=120s"],
        ["python3", "scripts/check-wire.py", "--go", "go", "agent-cli"],
        ["go", "vet", "./agent-cli/internal/transport/cli/internal/livehost", "./agent-cli/internal/transport/cli"],
    ]
    results = []
    for argv in commands:
        result = command(argv, ROOT, 240)
        require(result["exit_code"] == 0 and not result["timed_out"], "focused gate failed: " + " ".join(argv))
        results.append({key: value for key, value in result.items() if key not in {"stdout", "stderr"}} | {
            "stdout_sha256": sha256_bytes(result["stdout"].encode()),
            "stderr_sha256": sha256_bytes(result["stderr"].encode()),
        })
    return {"commands": results}


def verify_c108_negative_controls() -> dict[str, Any]:
    result = command(["python3", str(ROOT / C108_VERIFIER), "--mode", "negative-controls"], ROOT, 180)
    require(result["exit_code"] == 0 and not result["timed_out"], "C108 negative controls failed")
    return {key: value for key, value in result.items() if key not in {"stdout", "stderr"}} | {
        "stdout_sha256": sha256_bytes(result["stdout"].encode()),
        "stderr_sha256": sha256_bytes(result["stderr"].encode()),
    }


def main() -> int:
    import argparse

    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True, choices=("provenance", "negative-controls", "all"))
    args = parser.parse_args()
    try:
        admission = verify_admission()
        board = verify_board()
        ancestry = verify_branch_and_ancestry()
        predecessors = verify_predecessors()
        scope = verify_scope()
        c108 = verify_c108()
        result: dict[str, Any] = {
            "schema_version": "audio-runtime-c146-verification-v1",
            "task": WORK,
            "project": PROJECT,
            "contract_revision": CONTRACT,
            "mode": args.mode,
            "admission": admission,
            "board": board,
            "ancestry": ancestry,
            "predecessors": predecessors,
            "scope": scope,
            "c108": c108,
        }
        if args.mode in {"negative-controls", "all"}:
            result["runtime"] = verify_runtime()
            result["c108_negative_controls"] = verify_c108_negative_controls()
        if args.mode == "all":
            result["focused_gates"] = verify_focused_gates()
        result["passes"] = True
        output = HERE / "runs/verification.json"
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(json.dumps({"status": "ok", "mode": args.mode, "passes": True, "head": ancestry["head"]}, sort_keys=True))
        return 0
    except (EvidenceFailure, OSError, subprocess.SubprocessError) as exc:
        print(json.dumps({"status": "failed", "mode": args.mode, "passes": False, "diagnostic": str(exc)}, sort_keys=True))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
