#!/usr/bin/env python3
"""Focused C45 controls for the replay negative-oracle false positive."""

from __future__ import annotations

import argparse
import hashlib
import json
import pathlib
import runpy
import subprocess
import tempfile
import time
from typing import Any


HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[4]
VERIFY_RELATIVE = pathlib.Path("docs/temp/projects/audio-runtime/audio-runtime-c44-retire-cli-model-admission/verify.py")
VERIFY = ROOT / VERIFY_RELATIVE
PRESERVED_RAW_FAILURE = pathlib.Path("/tmp/audio-runtime-c44-probe.QccmvA/private-report/run-20260910T-probe/negative-wrong-marker-oracle.stderr")


def sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def sha256(path: pathlib.Path) -> str:
    return sha256_bytes(path.read_bytes())


def git_show(revision: str) -> bytes:
    result = subprocess.run(
        ["rtk", "proxy", "git", "show", f"{revision}:{VERIFY_RELATIVE}"],
        cwd=ROOT,
        check=True,
        capture_output=True,
        timeout=30,
    )
    return result.stdout


def load_verify(path: pathlib.Path) -> dict[str, Any]:
    loaded = runpy.run_path(str(path), run_name="c45_verify_import")
    if "main" not in loaded or "run_wrong_replay_oracles" not in loaded:
        raise AssertionError("verify.py import did not expose the expected controls")
    loaded["_c45_globals"] = loaded["run_wrong_replay_oracles"].__globals__
    return loaded


def write_valid_replay(module: dict[str, Any], root: pathlib.Path) -> tuple[pathlib.Path, pathlib.Path]:
    fixture_root = root / "fixtures"
    fixture_root.mkdir(parents=True)
    fixture = fixture_root / "c16-audio-tool.session.json"
    expectation = module["FIXTURE_EXPECTATIONS"]["audio-tool"]
    fixture.write_text(
        json.dumps({"records": [{"type": event_type} for event_type in expectation["fixture_types"]]}, indent=2) + "\n",
        encoding="utf-8",
    )
    module["_c45_globals"]["FIXTURES"] = fixture_root

    case_dir = root / "replay-audio-tool"
    record_dir = case_dir / "tool-record"
    (record_dir / "audio").mkdir(parents=True)
    artifacts = {
        "client.transcript.jsonl": '{"tick": 1}\n',
        "agent.transcript.jsonl": '{"tick": 1}\n',
        "session-log.jsonl": json.dumps(
            {
                "response": {"text": "strict replay continuation", "complete": True, "audio_bytes": 4800},
                "tool_events": [
                    {"type": "tool_call"},
                    {"type": "tool_result", "content": "PROBE_TOOL_MARKER_9182\n"},
                ],
            }
        )
        + "\n",
        "audio/out-000.pcm": b"\x01" * 4800,
        "provider.json": fixture.read_bytes(),
    }
    for relative, content in artifacts.items():
        path = record_dir / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        if isinstance(content, bytes):
            path.write_bytes(content)
        else:
            path.write_text(content, encoding="utf-8")
    manifest = {
        "terminal": expectation["terminal"],
        "artifacts": [{"path": relative, "sha256": sha256(record_dir / relative)} for relative in artifacts],
    }
    manifest_path = record_dir / "manifest.json"
    manifest_path.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    stdout_path = root / "yui-replay-audio-tool.stdout"
    stdout_path.write_text("PROBE_TOOL_MARKER_9182\nstrict replay continuation\n", encoding="utf-8")
    return manifest_path, stdout_path


def expect_evidence_failure(callable_: Any, label: str) -> str:
    try:
        callable_()
    except Exception as exc:
        evidence_failure = getattr(callable_, "evidence_failure_type", None)
        if evidence_failure is not None and not isinstance(exc, evidence_failure):
            raise AssertionError(f"{label} raised the wrong exception type: {type(exc).__name__}: {exc}") from exc
        return str(exc)
    raise AssertionError(f"{label} unexpectedly passed")


def run_current_controls(module: dict[str, Any], root: pathlib.Path, started: float, total_timeout: float, child_timeout: float) -> dict[str, Any]:
    manifest_path, stdout_path = write_valid_replay(module, root)
    replay_results = [{"label": "audio-tool", "manifest": str(manifest_path), "stdout_path": str(stdout_path)}]
    positive = module["run_wrong_replay_oracles"](root, started, total_timeout, child_timeout, replay_results)
    assert positive["wrong_pcm"]["exit_code"] == 1
    assert positive["wrong_marker"]["exit_code"] == 1
    positive_wrong_pcm_stdout = pathlib.Path(positive["wrong_pcm"]["stdout_path"]).read_text(encoding="utf-8").strip()
    positive_wrong_marker_stdout = pathlib.Path(positive["wrong_marker"]["stdout_path"]).read_text(encoding="utf-8").strip()

    stdout_path.unlink()
    missing_stdout_error = expect_evidence_failure(
        lambda: module["run_wrong_replay_oracles"](root, started, total_timeout, child_timeout, replay_results),
        "missing stdout",
    )
    assert "stdout is missing" in missing_stdout_error
    stdout_path.write_text("PROBE_TOOL_MARKER_9182\nstrict replay continuation\n", encoding="utf-8")

    pcm_path = root / "replay-audio-tool" / "tool-record" / "audio" / "out-000.pcm"
    pcm_bytes = pcm_path.read_bytes()
    pcm_path.unlink()
    missing_pcm_error = expect_evidence_failure(
        lambda: module["run_wrong_replay_oracles"](root, started, total_timeout, child_timeout, replay_results),
        "missing PCM",
    )
    assert "intended bounded rejection" in missing_pcm_error
    pcm_path.write_bytes(pcm_bytes)

    manifest_backup = manifest_path.read_bytes()
    manifest_path.write_text("{", encoding="utf-8")
    malformed_manifest_error = expect_evidence_failure(
        lambda: module["run_wrong_replay_oracles"](root, started, total_timeout, child_timeout, replay_results),
        "malformed manifest",
    )
    assert "intended bounded rejection" in malformed_manifest_error
    manifest_path.write_bytes(manifest_backup)

    stdout_path.write_text("C44_WRONG_MARKER\n", encoding="utf-8")
    accidental_marker_error = expect_evidence_failure(
        lambda: module["run_wrong_replay_oracles"](root, started, total_timeout, child_timeout, replay_results),
        "accidental marker oracle success",
    )
    assert "lost its intended diagnostic" in accidental_marker_error

    verifier_globals = module["_c45_globals"]
    original_inspector = verifier_globals["process_group_pids"]

    def unavailable_inspector(_pgid: int) -> list[int]:
        raise module["EvidenceFailure"]("synthetic process inspection unavailable")

    verifier_globals["process_group_pids"] = unavailable_inspector
    try:
        inspection_error = expect_evidence_failure(
            lambda: module["run_process"](
                "synthetic-process-inspection",
                ["rtk", "proxy", "python3", "-c", "pass"],
                root,
                root,
                child_timeout,
            ),
            "process inspection failure",
        )
    finally:
        verifier_globals["process_group_pids"] = original_inspector
    assert "process inspection unavailable" in inspection_error

    return {
        "positive": {
            "wrong_pcm_exit": positive["wrong_pcm"]["exit_code"],
            "wrong_marker_exit": positive["wrong_marker"]["exit_code"],
            "wrong_pcm_stdout": positive_wrong_pcm_stdout,
            "wrong_marker_stdout": positive_wrong_marker_stdout,
        },
        "fail_closed": {
            "missing_stdout": missing_stdout_error,
            "missing_pcm": missing_pcm_error,
            "malformed_manifest": malformed_manifest_error,
            "accidental_marker_success": accidental_marker_error,
            "process_inspection": inspection_error,
        },
    }


def reproduce_original(revision: str, old_source: bytes, root: pathlib.Path) -> dict[str, Any]:
    with tempfile.TemporaryDirectory(prefix="c45-old-verifier-") as temporary:
        temporary_root = pathlib.Path(temporary)
        old_verify = temporary_root / "verify.py"
        old_verify.write_bytes(old_source)
        old_module = load_verify(old_verify)
        run_dir = temporary_root / "run"
        run_dir.mkdir()
        actual_stdout = run_dir / "yui-replay-audio-tool.stdout"
        actual_stdout.write_text("PROBE_TOOL_MARKER_9182\nstrict replay continuation\n", encoding="utf-8")
        old_result = old_module["run_wrong_replay_oracles"](run_dir, time.monotonic(), 300, 60)
        marker = old_result["wrong_marker"]
        marker_stderr = pathlib.Path(marker["stderr_path"]).read_text(encoding="utf-8")
        if marker["exit_code"] != 1 or marker["timed_out"] or not marker["parent_reaped"] or marker["surviving_process_group_pids"]:
            raise AssertionError(f"preserved verifier did not reproduce the bounded false-positive: {marker}")
        if "FileNotFoundError" not in marker_stderr:
            raise AssertionError(f"preserved verifier did not expose the missing-stdout traceback: {marker_stderr}")
        if (run_dir / "replay-audio-tool" / "yui-replay-audio-tool.stdout").exists():
            raise AssertionError("historical reproduction unexpectedly created the wrong case-dir stdout")
        normalized_stderr = marker_stderr.replace(str(temporary_root), "<private-run>")
        return {
            "original_revision": revision,
            "original_verify_sha256": sha256_bytes(old_source),
            "actual_run_dir_stdout_exists": actual_stdout.is_file(),
            "wrong_case_dir_stdout_exists": False,
            "wrapper_returned_exit_code": marker["exit_code"],
            "wrapper_returned_parent_reaped": marker["parent_reaped"],
            "wrapper_returned_survivors": marker["surviving_process_group_pids"],
            "child_stderr_contains": "FileNotFoundError",
            "child_stderr_normalized_sha256": sha256_bytes(normalized_stderr.encode("utf-8")),
            "child_stderr_normalized": normalized_stderr,
        }


def write_historical_evidence(reproduction: dict[str, Any]) -> None:
    raw = {
        "schema": "audio-runtime-c45-historical-false-positive/v1",
        "description": "The preserved verifier accepted an exit-1 child whose only failure was FileNotFoundError while reading the wrong case-dir stdout path.",
        "reproduction": reproduction,
        "preserved_raw_failure": {
            "path": str(PRESERVED_RAW_FAILURE),
            "exists": PRESERVED_RAW_FAILURE.is_file(),
            "sha256": sha256(PRESERVED_RAW_FAILURE) if PRESERVED_RAW_FAILURE.is_file() else None,
        },
    }
    (HERE / "historical-false-positive.json").write_text(json.dumps(raw, indent=2) + "\n", encoding="utf-8")
    (HERE / "historical-false-positive.stderr").write_text(reproduction["child_stderr_normalized"], encoding="utf-8")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--original-revision", required=True)
    parser.add_argument("--child-timeout", type=float, default=60)
    parser.add_argument("--total-timeout", type=float, default=300)
    args = parser.parse_args()
    if args.child_timeout <= 0 or args.child_timeout > 60 or args.total_timeout <= 0 or args.total_timeout > 300:
        raise SystemExit("C45 focused bounds exceeded")
    started = time.monotonic()
    old_source = git_show(args.original_revision)
    reproduction = reproduce_original(args.original_revision, old_source, HERE)
    module = load_verify(VERIFY)
    with tempfile.TemporaryDirectory(prefix="c45-current-controls-") as temporary:
        controls = run_current_controls(module, pathlib.Path(temporary), started, args.total_timeout, args.child_timeout)
    if time.monotonic() - started > args.total_timeout:
        raise SystemExit("C45 focused aggregate deadline exceeded")
    write_historical_evidence(reproduction)
    print(
        json.dumps(
            {
                "schema": "audio-runtime-c45-oracle-controls/v1",
                "decision": "ACCEPTED",
                "original_revision": args.original_revision,
                "reproduction": reproduction,
                "controls": controls,
                "elapsed_seconds": round(time.monotonic() - started, 6),
            },
            indent=2,
        )
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
