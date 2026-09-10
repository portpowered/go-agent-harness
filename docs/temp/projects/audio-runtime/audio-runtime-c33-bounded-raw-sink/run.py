#!/usr/bin/env python3
"""Build and run the C33 public raw-sink consumer with bounded evidence."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time


EVIDENCE = Path(__file__).resolve().parent
REPO_ROOT = EVIDENCE.parents[4]
FACTORY_ROOT = Path(os.environ.get("FACTORY_ROOT", REPO_ROOT)).resolve()
BUILD_TIMEOUT = 60
RUN_TIMEOUT = 60
SCRATCH_BYTES = 64 * 1024
ALLOCATION_BUDGET = 256 * 1024
TOOL_FIXTURE = REPO_ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures/c16-audio-tool.session.json"
EXPECTED_RENDERED = (3200, "7d2d8221eb8ec0be3e1da4a3ed518e1e183aa56e4ac0140ca0cf761068555805")
EXPECTED_PROVIDER = (4800, "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502")


class EvidenceError(RuntimeError):
    pass


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def run_process(command: list[str], cwd: Path, timeout: int, environment: dict[str, str] | None = None) -> dict:
    started = time.monotonic()
    process = subprocess.Popen(
        command,
        cwd=cwd,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        start_new_session=True,
        env=environment,
    )
    try:
        stdout, stderr = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired as error:
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        try:
            stdout, stderr = process.communicate(timeout=5)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            stdout, stderr = process.communicate()
        raise EvidenceError(f"timeout after {timeout}s: {' '.join(command)}\n{stdout}\n{stderr}") from error
    return {
        "argv": command,
        "cwd": str(cwd),
        "duration_seconds": round(time.monotonic() - started, 6),
        "exit_code": process.returncode,
        "stdout": stdout,
        "stderr": stderr,
    }


def git_revision(source_root: Path) -> str:
    result = subprocess.run(
        ["rtk", "proxy", "git", "rev-parse", "HEAD"],
        cwd=source_root,
        check=True,
        capture_output=True,
        text=True,
    )
    return result.stdout.strip()


def build_consumer(source_root: Path, output: Path) -> dict:
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c33-build-") as temp:
        build_root = Path(temp)
        (build_root / "main.go").write_bytes((EVIDENCE / "consumer/main.go").read_bytes())
        (build_root / "go.mod").write_text(
            "module c33consumer\n\ngo 1.26.7\n\n"
            "require github.com/portpowered/go-agent-harness/go-audio v0.0.0\n\n"
            f"replace github.com/portpowered/go-agent-harness/go-audio => {source_root / 'go-audio'}\n",
            encoding="utf-8",
        )
        source_sum = source_root / "go-audio/go.sum"
        if source_sum.is_file():
            (build_root / "go.sum").write_bytes(source_sum.read_bytes())
        command = ["rtk", "proxy", "go", "build", "-mod=mod", "-o", str(output), "."]
        environment = os.environ.copy()
        environment["GOWORK"] = "off"
        result = run_process(command, build_root, BUILD_TIMEOUT, environment)
    if result["exit_code"] != 0:
        raise EvidenceError(f"consumer build failed: {result}")
    return result


def run_consumer(binary: Path, mode: str, report_path: Path, negative: bool = False) -> dict:
    command = ["rtk", "proxy", str(binary), "--mode", mode, "--output", str(report_path)]
    if negative:
        command.append("--negative-control")
    result = run_process(command, REPO_ROOT, RUN_TIMEOUT)
    if report_path.is_file():
        result["report"] = json.loads(report_path.read_text(encoding="utf-8"))
    return result


def require_success(result: dict, label: str) -> None:
    if result["exit_code"] != 0:
        raise EvidenceError(f"{label} exited {result['exit_code']}: {result['stderr']}")


def run_public_mode(
    source_root: Path,
    mode: str,
    build: bool,
    negative: bool,
    expect_resource_failure: bool,
    run_dir: Path,
) -> dict:
    revision = git_revision(source_root)
    binary = EVIDENCE / "artifacts" / f"raw-sink-consumer-{revision[:12]}"
    binary.parent.mkdir(parents=True, exist_ok=True)
    build_result = build_consumer(source_root, binary) if build or not binary.is_file() else {"reused": True}
    report_path = run_dir / f"consumer-{mode}.json"
    execution = run_consumer(binary, mode, report_path, negative)
    if negative:
        if execution["exit_code"] == 0 or "oracle" not in execution["stderr"].lower():
            raise EvidenceError(f"negative oracle control unexpectedly passed: {execution}")
    elif expect_resource_failure:
        resource_words = ("allocation", "scratch", "writer request")
        if execution["exit_code"] == 0 or not any(word in execution["stderr"].lower() for word in resource_words):
            raise EvidenceError(f"expected old whole-buffer implementation to fail allocation budget: {execution}")
    else:
        require_success(execution, f"consumer {mode}")
        report = execution.get("report", {})
        if report.get("mode") != mode or report.get("scratch_bytes") != SCRATCH_BYTES:
            raise EvidenceError(f"consumer report has unexpected mode/budget: {report}")
    return {
        "source_root": str(source_root),
        "source_revision": revision,
        "binary": str(binary),
        "binary_sha256": sha256_file(binary),
        "build": build_result,
        "execution": execution,
    }


def require_pcm(path: Path, expected: tuple[int, str], label: str) -> dict:
    if not path.is_file():
        raise EvidenceError(f"{label} missing: {path}")
    size, digest = expected
    actual_size = path.stat().st_size
    actual_hash = sha256_file(path)
    if (actual_size, actual_hash) != expected:
        raise EvidenceError(f"{label} got {(actual_size, actual_hash)}, want {expected}")
    return {"path": str(path), "bytes": actual_size, "sha256": actual_hash}


def runtime_regression(yui: Path, run_dir: Path) -> dict:
    if not yui.is_file():
        raise EvidenceError(f"runtime regression yui is unavailable: {yui}")
    if not TOOL_FIXTURE.is_file():
        raise EvidenceError(f"runtime regression fixture is unavailable: {TOOL_FIXTURE}")
    work = Path(tempfile.mkdtemp(prefix="c33-runtime-", dir=run_dir))
    config = work / "config"
    config.mkdir()
    output = work / "rendered.pcm"
    bundle = work / "bundle"
    capture = run_process(
        [str(yui), "-C", str(config), "session", "--replay", str(TOOL_FIXTURE), "--audio-out", str(output), "--record-dir", str(bundle), "--trace-audio", "--max-duration", "60s"],
        work,
        RUN_TIMEOUT,
    )
    require_success(capture, "public audio/tool replay")
    rendered = require_pcm(output, EXPECTED_RENDERED, "rendered PCM")
    provider = require_pcm(bundle / "audio/out-000.pcm", EXPECTED_PROVIDER, "provider PCM")
    session_log = bundle / "session-log.jsonl"
    if not session_log.is_file():
        raise EvidenceError(f"session log missing: {session_log}")
    session_text = session_log.read_text(encoding="utf-8")
    if "PROBE_TOOL_MARKER_9182" not in session_text or "strict replay continuation" not in session_text:
        raise EvidenceError("tool marker or continuation missing from session log")
    replay_config = work / "replay-config"
    replay_config.mkdir()
    replay = run_process([str(yui), "-C", str(replay_config), "session", "replay", str(bundle)], work, RUN_TIMEOUT)
    require_success(replay, "directory replay")
    if "Replay verified: 18 wire events, 1 tool calls" not in f"{replay['stdout']}\n{replay['stderr']}":
        raise EvidenceError("directory replay did not report the expected wire/tool counts")
    return {
        "yui": str(yui),
        "yui_sha256": sha256_file(yui),
        "fixture": str(TOOL_FIXTURE),
        "fixture_sha256": sha256_file(TOOL_FIXTURE),
        "capture": capture,
        "replay": replay,
        "rendered": rendered,
        "provider": provider,
        "clean_shutdown": capture["exit_code"] == 0 and replay["exit_code"] == 0,
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--source-root", type=Path, default=REPO_ROOT)
    parser.add_argument("--build", action="store_true")
    parser.add_argument("--mode", choices=("characterize", "verify"))
    parser.add_argument("--negative-control", action="store_true")
    parser.add_argument("--expect-resource-failure", action="store_true")
    parser.add_argument("--runtime-regression", action="store_true")
    parser.add_argument("--yui", type=Path)
    parser.add_argument("--report", type=Path)
    args = parser.parse_args()
    run_dir = EVIDENCE / "runs" / f"run-{time.strftime('%Y%m%dT%H%M%SZ', time.gmtime())}-{os.getpid()}"
    run_dir.mkdir(parents=True, exist_ok=True)
    result: dict = {"run_dir": str(run_dir), "argv": sys.argv, "passed": False}
    try:
        source_root = args.source_root.resolve()
        if args.runtime_regression:
            if args.yui is None:
                raise EvidenceError("--runtime-regression requires --yui")
            result["runtime_regression"] = runtime_regression(args.yui.resolve(), run_dir)
        elif args.mode is not None:
            result["public"] = run_public_mode(
                source_root,
                args.mode,
                args.build,
                args.negative_control,
                args.expect_resource_failure,
                run_dir,
            )
        else:
            raise EvidenceError("one of --mode or --runtime-regression is required")
        result["passed"] = True
    except (OSError, subprocess.SubprocessError, EvidenceError, json.JSONDecodeError) as error:
        result["error"] = str(error)
    rendered = json.dumps(result, indent=2, sort_keys=True) + "\n"
    (run_dir / "report.json").write_text(rendered, encoding="utf-8")
    if args.report:
        args.report.parent.mkdir(parents=True, exist_ok=True)
        args.report.write_text(rendered, encoding="utf-8")
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0 if result["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
