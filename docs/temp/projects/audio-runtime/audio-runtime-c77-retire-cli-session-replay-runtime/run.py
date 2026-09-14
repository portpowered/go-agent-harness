#!/usr/bin/env python3
"""Run the bounded shipped-executable C77 replay and mutation probes."""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import shutil
import subprocess
import sys
import time
from typing import Any

import verify


def build_yui(run_dir: pathlib.Path) -> pathlib.Path:
    binary = run_dir / "bin" / "yui"
    binary.parent.mkdir(parents=True, exist_ok=True)
    result = verify.run_process(
        "build-yui",
        ["rtk", "proxy", "go", "build", "-tags=nomicrophone", "-trimpath", "-o", str(binary), "./agent-cli/cmd/yui"],
        verify.ROOT,
        run_dir,
        timeout_seconds=180,
        env=verify.safe_environment(),
    )
    verify.require_ok(result)
    if not binary.is_file() or binary.stat().st_size == 0:
        raise verify.EvidenceFailure(f"yui build did not produce {binary}")
    return binary


def fixture(label: str) -> pathlib.Path:
    return verify.FIXTURES / verify.PARITY_EXPECTATIONS[label]["fixture"]


def replay(
    label: str,
    source: pathlib.Path,
    yui: pathlib.Path,
    case_dir: pathlib.Path,
    run_dir: pathlib.Path,
    child_timeout: float,
) -> dict[str, Any]:
    case_dir.mkdir(parents=True, exist_ok=True)
    (case_dir / "evidence" / "runs").mkdir(parents=True, exist_ok=True)
    result = verify.run_process(
        f"yui-{label}-{case_dir.name}",
        [
            "rtk", "proxy", str(yui), "session", "--replay", str(source),
            "--audio-out", "audio.wav", "--record-dir", "record", "--trace-audio",
            "--workdir", ".", "--allow-path", ".",
        ],
        case_dir,
        run_dir,
        timeout_seconds=child_timeout,
        env=verify.safe_environment(),
    )
    verify.require_ok(result)
    stdout = pathlib.Path(result["stdout_path"]).read_text(encoding="utf-8", errors="replace")
    if label == "audio-tool":
        marker = case_dir / "evidence/runs/exec-invocations-v4.log"
        if "PROBE_TOOL_MARKER_9182" not in stdout or "strict replay continuation" not in stdout or not marker.is_file():
            raise verify.EvidenceFailure("audio-tool replay lost the tool marker or continuation")
    elif "replay mismatch" in stdout.lower():
        raise verify.EvidenceFailure(f"{label} replay reported a mismatch")
    record_dir = case_dir / "record"
    audio_out = case_dir / "audio.wav"
    if not (record_dir / "manifest.json").is_file() or not audio_out.is_file():
        raise verify.EvidenceFailure(f"{label} replay did not produce a complete recording")
    parity = verify.validate_parity_artifacts(label, source if source.is_file() else fixture(label), record_dir)
    return {
        "label": label,
        "source": str(source),
        "stdout_path": result["stdout_path"],
        "stderr_path": result["stderr_path"],
        "record_dir": str(record_dir),
        "audio_out_bytes": audio_out.stat().st_size,
        "audio_out_sha256": verify.sha256(audio_out),
        "parity": parity,
    }


def replay_expect_failure(
    label: str,
    source: pathlib.Path,
    yui: pathlib.Path,
    case_dir: pathlib.Path,
    run_dir: pathlib.Path,
    child_timeout: float,
) -> dict[str, Any]:
    case_dir.mkdir(parents=True, exist_ok=True)
    result = verify.run_process(
        f"yui-{label}-{case_dir.name}",
        [
            "rtk", "proxy", str(yui), "session", "--replay", str(source),
            "--audio-out", "audio.wav", "--record-dir", "record", "--trace-audio",
            "--workdir", ".", "--allow-path", ".",
        ],
        case_dir,
        run_dir,
        timeout_seconds=child_timeout,
        env=verify.safe_environment(),
    )
    verify.require_rejected(result)
    return {"label": label, "source": str(source), "stdout_path": result["stdout_path"], "stderr_path": result["stderr_path"], "exit_code": result["exit_code"]}


def run_shipped_raw(yui: pathlib.Path, run_dir: pathlib.Path, child_timeout: float) -> dict[str, Any]:
    return replay("audio-tool", fixture("audio-tool"), yui, run_dir / "shipped-raw-capture", run_dir, child_timeout)


def run_shipped_finalized(yui: pathlib.Path, run_dir: pathlib.Path, child_timeout: float) -> dict[str, Any]:
    raw_dir = run_dir / "finalized-source"
    raw = replay("interruption", fixture("interruption"), yui, raw_dir, run_dir, child_timeout)
    final_dir = run_dir / "shipped-finalized-bundle"
    finalized = replay("interruption", pathlib.Path(raw["record_dir"]), yui, final_dir, run_dir, child_timeout)
    return {"source_capture": raw, "finalized_bundle": finalized}


def run_strict_mutations(yui: pathlib.Path, run_dir: pathlib.Path, child_timeout: float) -> dict[str, Any]:
    baseline_dir = run_dir / "strict-baseline"
    baseline = replay("audio-tool", fixture("audio-tool"), yui, baseline_dir, run_dir, child_timeout)

    reordered = run_dir / "mutated-reordered.session.json"
    capture = verify.load_json(fixture("audio-tool"))
    records = capture["records"]
    records[2], records[3] = records[3], records[2]
    reordered.write_text(json.dumps(capture, indent=2) + "\n", encoding="utf-8")
    order_failure = replay_expect_failure("strict-reordered-capture", reordered, yui, run_dir / "strict-reordered", run_dir, child_timeout)

    tampered_dir = run_dir / "mutated-finalized-bundle"
    shutil.copytree(pathlib.Path(baseline["record_dir"]), tampered_dir)
    pcm = tampered_dir / "audio/out-000.pcm"
    payload = bytearray(pcm.read_bytes())
    payload[0] ^= 0x01
    pcm.write_bytes(payload)
    digest_failure = replay_expect_failure("strict-stale-pcm-digest", tampered_dir, yui, run_dir / "strict-stale-pcm", run_dir, child_timeout)
    return {"baseline": baseline, "reordered_capture": order_failure, "stale_pcm_digest": digest_failure}


def run_non_replay_regression(yui: pathlib.Path, run_dir: pathlib.Path, child_timeout: float) -> dict[str, Any]:
    case_dir = run_dir / "non-replay-regression"
    case_dir.mkdir(parents=True, exist_ok=True)
    result = verify.run_process(
        "yui-help",
        ["rtk", "proxy", str(yui), "--help"],
        case_dir,
        run_dir,
        timeout_seconds=child_timeout,
        env=verify.safe_environment(),
    )
    verify.require_ok(result)
    output = pathlib.Path(result["stdout_path"]).read_text(encoding="utf-8", errors="replace")
    output += pathlib.Path(result["stderr_path"]).read_text(encoding="utf-8", errors="replace")
    if "session" not in output.lower() and "usage" not in output.lower() and "ask" not in output.lower():
        raise verify.EvidenceFailure("yui help output lost the non-replay command surface")
    return {"stdout_path": result["stdout_path"], "stderr_path": result["stderr_path"], "help_bytes": len(output.encode("utf-8"))}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", choices=("shipped-raw-capture", "shipped-finalized-bundle", "strict-mutations", "non-replay-regression"), required=True)
    parser.add_argument("--child-timeout", type=float, default=60)
    parser.add_argument("--aggregate-timeout", type=float, default=300)
    args = parser.parse_args()
    stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
    run_dir = verify.RUNS / f"run-{args.case}-{stamp}-{os.getpid()}"
    run_dir.mkdir(parents=True, exist_ok=True)
    deadline = time.monotonic() + args.aggregate_timeout
    outcome: dict[str, Any] = {"case": args.case, "run_dir": str(run_dir), "decision": "FAILED"}
    try:
        yui = build_yui(run_dir)
        if time.monotonic() > deadline:
            raise verify.EvidenceFailure("aggregate timeout expired after yui build")
        if args.case == "shipped-raw-capture":
            outcome["evidence"] = run_shipped_raw(yui, run_dir, args.child_timeout)
        elif args.case == "shipped-finalized-bundle":
            outcome["evidence"] = run_shipped_finalized(yui, run_dir, args.child_timeout)
        elif args.case == "strict-mutations":
            outcome["evidence"] = run_strict_mutations(yui, run_dir, args.child_timeout)
        else:
            outcome["evidence"] = run_non_replay_regression(yui, run_dir, args.child_timeout)
        if time.monotonic() > deadline:
            raise verify.EvidenceFailure("aggregate timeout expired before evidence completion")
        outcome["source_revision"] = subprocess.run(["rtk", "proxy", "git", "rev-parse", "HEAD"], cwd=verify.ROOT, check=True, capture_output=True, text=True).stdout.strip()
        outcome["yui_sha256"] = verify.sha256(yui)
        outcome["decision"] = "ACCEPTED"
    except (verify.EvidenceFailure, OSError, subprocess.SubprocessError) as exc:
        outcome["error"] = str(exc)
        (run_dir / "outcome.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")
        print(json.dumps(outcome, indent=2), file=sys.stderr)
        return 1
    (run_dir / "outcome.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(outcome, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
