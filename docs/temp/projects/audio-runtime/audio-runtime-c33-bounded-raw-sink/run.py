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
GIT_TIMEOUT = 15
TERM_GRACE_SECONDS = 2
KILL_GRACE_SECONDS = 2
REAP_GRACE_SECONDS = 2
CLEANUP_CONTROL_TIMEOUT = 1
CLEANUP_DESCENDANT_TIMEOUT = 3
SCRATCH_BYTES = 64 * 1024
ALLOCATION_BUDGET = 256 * 1024
TOOL_FIXTURE = REPO_ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures/c16-audio-tool.session.json"
EXPECTED_RENDERED = (3200, "7d2d8221eb8ec0be3e1da4a3ed518e1e183aa56e4ac0140ca0cf761068555805")
EXPECTED_PROVIDER = (4800, "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502")
EXPECTED_RAW = (6, "d68491246ad239fd17f4917785722e6cab3280a1dc3239a5372adacd3981d2b1")
EXPECTED_RAW_HEX = "0080ff7fffff"
CONSUMER_SOURCE = EVIDENCE / "consumer/main.go"
CONSUMER_SOURCE_ROOTS = ("go-audio",)
YUI_SOURCE_ROOTS = (
    "agent-cli",
    "go-agent-runtime",
    "go-agent-loop",
    "go-audio",
    "go-device-gateway",
    "go-llm-gateway",
    "go.work",
    "go.work.sum",
)


class EvidenceError(RuntimeError):
    pass


class ProcessTimeout(EvidenceError):
    def __init__(self, message: str, cleanup: dict, stdout: str, stderr: str) -> None:
        super().__init__(message)
        self.cleanup = cleanup
        self.stdout = stdout
        self.stderr = stderr


def sha256_bytes(payload: bytes) -> str:
    return hashlib.sha256(payload).hexdigest()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def text_output(value: str | bytes | None) -> str:
    if value is None:
        return ""
    if isinstance(value, bytes):
        return value.decode("utf-8", errors="replace")
    return value


def close_pipe(pipe: object | None) -> None:
    if pipe is None:
        return
    try:
        pipe.close()  # type: ignore[attr-defined]
    except OSError:
        pass


def send_group_signal(process: subprocess.Popen, sig: signal.Signals, cleanup: dict, key: str) -> None:
    try:
        os.killpg(process.pid, sig)
        cleanup[key] = True
    except ProcessLookupError:
        cleanup[f"{key}_process_missing"] = True


def append_timeout_output(outputs: list[str], value: str | bytes | None) -> None:
    rendered = text_output(value)
    if rendered and (not outputs or rendered != outputs[-1]):
        outputs.append(rendered)


def bounded_timeout_cleanup(process: subprocess.Popen, command: list[str], timeout_error: subprocess.TimeoutExpired) -> ProcessTimeout:
    cleanup_started = time.monotonic()
    cleanup = {
        "term_sent": False,
        "kill_sent": False,
        "term_communicate_completed": False,
        "kill_communicate_completed": False,
        "post_kill_communicate_timed_out": False,
        "pipes_closed": False,
        "parent_reaped": False,
        "reap_timeout": False,
    }
    stdout_parts: list[str] = []
    stderr_parts: list[str] = []
    append_timeout_output(stdout_parts, timeout_error.output)
    append_timeout_output(stderr_parts, timeout_error.stderr)
    send_group_signal(process, signal.SIGTERM, cleanup, "term_sent")
    try:
        stdout, stderr = process.communicate(timeout=TERM_GRACE_SECONDS)
        cleanup["term_communicate_completed"] = True
        append_timeout_output(stdout_parts, stdout)
        append_timeout_output(stderr_parts, stderr)
    except subprocess.TimeoutExpired as term_error:
        append_timeout_output(stdout_parts, term_error.output)
        append_timeout_output(stderr_parts, term_error.stderr)
        send_group_signal(process, signal.SIGKILL, cleanup, "kill_sent")
        try:
            stdout, stderr = process.communicate(timeout=KILL_GRACE_SECONDS)
            cleanup["kill_communicate_completed"] = True
            append_timeout_output(stdout_parts, stdout)
            append_timeout_output(stderr_parts, stderr)
        except subprocess.TimeoutExpired as kill_error:
            cleanup["post_kill_communicate_timed_out"] = True
            append_timeout_output(stdout_parts, kill_error.output)
            append_timeout_output(stderr_parts, kill_error.stderr)
    finally:
        try:
            process.wait(timeout=REAP_GRACE_SECONDS)
            cleanup["parent_reaped"] = process.returncode is not None
        except subprocess.TimeoutExpired:
            cleanup["reap_timeout"] = True
        close_pipe(process.stdout)
        close_pipe(process.stderr)
        cleanup["pipes_closed"] = True
    cleanup["bounded_cleanup_seconds"] = round(time.monotonic() - cleanup_started, 6)
    rendered_cleanup = json.dumps(cleanup, sort_keys=True)
    stdout = "\n".join(stdout_parts)
    stderr = "\n".join(stderr_parts)
    message = f"timeout after {timeout_error.timeout}s: {' '.join(command)}\ncleanup={rendered_cleanup}\n{stdout}\n{stderr}"
    return ProcessTimeout(message, cleanup, stdout, stderr)


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
        timeout_error = bounded_timeout_cleanup(process, command, error)
        raise timeout_error from error
    return {
        "argv": command,
        "cwd": str(cwd),
        "duration_seconds": round(time.monotonic() - started, 6),
        "exit_code": process.returncode,
        "stdout": stdout,
        "stderr": stderr,
    }


def git_bytes(source_root: Path, arguments: list[str]) -> bytes:
    result = subprocess.run(
        ["rtk", "proxy", "git", *arguments],
        cwd=source_root,
        check=True,
        capture_output=True,
        timeout=GIT_TIMEOUT,
    )
    return result.stdout


def git_output(source_root: Path, arguments: list[str]) -> str:
    return git_bytes(source_root, arguments).decode("utf-8").strip()


def git_revision(source_root: Path) -> str:
    return git_output(source_root, ["rev-parse", "HEAD"])


def tracked_manifest(source_root: Path, roots: tuple[str, ...]) -> dict:
    raw_paths = git_bytes(source_root, ["ls-files", "-z", "--", *roots])
    paths = [path.decode("utf-8") for path in raw_paths.split(b"\0") if path]
    entries = []
    for relative in paths:
        path = source_root / relative
        if not path.is_file():
            raise EvidenceError(f"tracked source input is not a file: {path}")
        entries.append((relative, sha256_file(path)))
    manifest = "\n".join(f"{relative}\0{digest}" for relative, digest in entries).encode("utf-8")
    return {
        "roots": list(roots),
        "file_count": len(entries),
        "sha256": sha256_bytes(manifest),
    }


def source_identity(source_root: Path, roots: tuple[str, ...]) -> dict:
    status = git_bytes(source_root, ["status", "--porcelain=v1", "--untracked-files=no", "--", *roots])
    manifest = tracked_manifest(source_root, roots)
    return {
        "root": str(source_root),
        "revision": git_revision(source_root),
        "tree": git_output(source_root, ["rev-parse", "HEAD^{tree}"]),
        "tracked": manifest,
        "tracked_status": status.decode("utf-8"),
        "clean": not status,
    }


def consumer_build_inputs(source_root: Path, identity: dict) -> tuple[dict, bytes, bytes]:
    source_mod = source_root / "go-audio/go.mod"
    source_sum = source_root / "go-audio/go.sum"
    generated_mod = (
        "module c33consumer\n\ngo 1.26.7\n\n"
        "require github.com/portpowered/go-agent-harness/go-audio v0.0.0\n\n"
        f"replace github.com/portpowered/go-agent-harness/go-audio => {source_root / 'go-audio'}\n"
    ).encode("utf-8")
    generated_sum = source_sum.read_bytes() if source_sum.is_file() else b""
    inputs = {
        "source_revision": identity["revision"],
        "source_tree": identity["tree"],
        "source_manifest_sha256": identity["tracked"]["sha256"],
        "source_go_mod_sha256": sha256_file(source_mod),
        "source_go_sum_sha256": sha256_file(source_sum) if source_sum.is_file() else None,
        "consumer_source_sha256": sha256_file(CONSUMER_SOURCE),
        "runner_sha256": sha256_file(Path(__file__).resolve()),
        "generated_go_mod_sha256": sha256_bytes(generated_mod),
        "generated_go_sum_sha256": sha256_bytes(generated_sum),
    }
    return inputs, generated_mod, generated_sum


def build_manifest_path(binary: Path) -> Path:
    return binary.with_name(binary.name + ".inputs.json")


def load_build_manifest(binary: Path) -> dict:
    manifest_path = build_manifest_path(binary)
    if not manifest_path.is_file():
        raise EvidenceError(f"reused consumer has no input manifest: {manifest_path}")
    try:
        return json.loads(manifest_path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as error:
        raise EvidenceError(f"invalid consumer input manifest: {manifest_path}") from error


def build_consumer(
    source_root: Path,
    output: Path,
    identity: dict,
    inputs: dict,
    generated_mod: bytes,
    generated_sum: bytes,
) -> dict:
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c33-build-") as temp:
        build_root = Path(temp)
        (build_root / "main.go").write_bytes(CONSUMER_SOURCE.read_bytes())
        (build_root / "go.mod").write_bytes(generated_mod)
        if inputs["generated_go_sum_sha256"]:
            (build_root / "go.sum").write_bytes(generated_sum)
        command = ["rtk", "proxy", "go", "build", "-mod=mod", "-trimpath", "-o", str(output), "."]
        environment = os.environ.copy()
        environment["GOWORK"] = "off"
        result = run_process(command, build_root, BUILD_TIMEOUT, environment)
    if result["exit_code"] != 0:
        raise EvidenceError(f"consumer build failed: {result}")
    binary_sha256 = sha256_file(output)
    manifest = {
        "source": identity,
        "build_inputs": inputs,
        "binary_sha256": binary_sha256,
    }
    build_manifest_path(output).write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    result["input_manifest"] = str(build_manifest_path(output))
    result["build_inputs"] = inputs
    return result


def reuse_consumer(binary: Path, identity: dict, inputs: dict) -> dict:
    manifest = load_build_manifest(binary)
    if manifest.get("source") != identity or manifest.get("build_inputs") != inputs:
        raise EvidenceError(f"consumer input manifest does not match tested source: {build_manifest_path(binary)}")
    actual_sha256 = sha256_file(binary)
    if manifest.get("binary_sha256") != actual_sha256:
        raise EvidenceError(f"consumer binary hash does not match input manifest: {binary}")
    return {
        "reused": True,
        "input_manifest": str(build_manifest_path(binary)),
        "build_inputs": inputs,
    }


def run_consumer(
    binary: Path,
    mode: str,
    report_path: Path,
    raw_file: Path,
    source_revision: str,
    negative: bool = False,
) -> dict:
    command = [
        "rtk",
        "proxy",
        str(binary),
        "--mode",
        mode,
        "--raw-file",
        str(raw_file),
        "--output",
        str(report_path),
    ]
    if negative:
        command.append("--negative-control")
    environment = os.environ.copy()
    environment["C33_SOURCE_REVISION"] = source_revision
    result = run_process(command, REPO_ROOT, RUN_TIMEOUT, environment)
    if report_path.is_file():
        result["report"] = json.loads(report_path.read_text(encoding="utf-8"))
    return result


def require_success(result: dict, label: str) -> None:
    if result["exit_code"] != 0:
        raise EvidenceError(f"{label} exited {result['exit_code']}: {result['stderr']}")


def observe_raw_file(path: Path, existed_before: bool) -> dict:
    if not path.is_file():
        raise EvidenceError(f"public raw file missing after consumer exit: {path}")
    payload = path.read_bytes()
    tail = payload[-EXPECTED_RAW[0] :]
    observation = {
        "path": str(path),
        "existed_before": existed_before,
        "exists_after": True,
        "bytes": len(payload),
        "sha256": sha256_bytes(payload),
        "hex": payload.hex(),
        "final_tail_hex": tail.hex(),
        "expected_bytes": EXPECTED_RAW[0],
        "expected_sha256": EXPECTED_RAW[1],
        "expected_hex": EXPECTED_RAW_HEX,
    }
    if existed_before or (observation["bytes"], observation["sha256"]) != EXPECTED_RAW or observation["hex"] != EXPECTED_RAW_HEX:
        raise EvidenceError(f"public raw file observation mismatch: {observation}")
    if observation["final_tail_hex"] != EXPECTED_RAW_HEX:
        raise EvidenceError(f"public raw file tail mismatch: {observation}")
    return observation


def wait_for_path(path: Path, timeout: float) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if path.is_file():
            return True
        time.sleep(0.02)
    return path.is_file()


def process_exists(pid: int) -> bool:
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def wait_for_process_exit(pid: int, timeout: float) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if not process_exists(pid):
            return True
        time.sleep(0.02)
    return not process_exists(pid)


def timeout_cleanup_control(run_dir: Path) -> dict:
    pid_path = run_dir / "timeout-control-descendant.pid"
    stop_path = run_dir / "timeout-control-stop"
    descendant_code = f"import time\nstop = {str(stop_path)!r}\nwhile not __import__('os').path.exists(stop):\n    time.sleep(0.01)\n"
    parent_code = (
        "import signal, subprocess, time\n"
        f"descendant = subprocess.Popen([{sys.executable!r}, '-c', {descendant_code!r}], start_new_session=True)\n"
        f"open({str(pid_path)!r}, 'w', encoding='utf-8').write(str(descendant.pid))\n"
        "signal.signal(signal.SIGTERM, signal.SIG_IGN)\n"
        "while True:\n"
        "    time.sleep(0.01)\n"
    )
    command = [sys.executable, "-c", parent_code]
    control = {
        "argv": command,
        "expected_timeout": True,
        "timeout_seconds": CLEANUP_CONTROL_TIMEOUT,
        "descendant_started": False,
        "descendant_stopped": False,
        "passed": False,
    }
    try:
        run_process(command, run_dir, CLEANUP_CONTROL_TIMEOUT)
        control["unexpected_completion"] = True
    except ProcessTimeout as error:
        control["timeout_observed"] = True
        control["cleanup"] = error.cleanup
    except (OSError, subprocess.SubprocessError, EvidenceError) as error:
        control["error"] = str(error)
        return control

    if not wait_for_path(pid_path, CLEANUP_DESCENDANT_TIMEOUT):
        control["error"] = f"timeout control did not start descendant: {pid_path}"
        return control
    control["descendant_started"] = True
    try:
        descendant_pid = int(pid_path.read_text(encoding="utf-8"))
    except (OSError, ValueError) as error:
        control["error"] = f"timeout control wrote an invalid descendant pid: {error}"
        return control
    stop_path.touch()
    control["descendant_stopped"] = wait_for_process_exit(descendant_pid, CLEANUP_DESCENDANT_TIMEOUT)
    control["descendant_pid"] = descendant_pid
    cleanup = control.get("cleanup", {})
    control["parent_reaped"] = cleanup.get("parent_reaped", False)
    control["passed"] = all(
        (
            control.get("timeout_observed", False),
            cleanup.get("term_sent", False),
            cleanup.get("kill_sent", False),
            cleanup.get("post_kill_communicate_timed_out", False),
            cleanup.get("pipes_closed", False),
            control["parent_reaped"],
            control["descendant_stopped"],
        )
    )
    if not control["passed"]:
        control["error"] = "timeout cleanup control did not prove bounded group cleanup"
    return control


def run_public_mode(
    source_root: Path,
    mode: str,
    build: bool,
    negative: bool,
    expect_resource_failure: bool,
    run_dir: Path,
) -> dict:
    identity = source_identity(source_root, CONSUMER_SOURCE_ROOTS)
    if not identity["clean"]:
        raise EvidenceError(f"consumer source inputs are dirty: {identity['tracked_status']}")
    inputs, generated_mod, generated_sum = consumer_build_inputs(source_root, identity)
    revision = identity["revision"]
    binary = EVIDENCE / "artifacts" / f"raw-sink-consumer-{revision[:12]}"
    binary.parent.mkdir(parents=True, exist_ok=True)
    if build or not binary.is_file():
        build_result = build_consumer(source_root, binary, identity, inputs, generated_mod, generated_sum)
    else:
        build_result = reuse_consumer(binary, identity, inputs)
    report_path = run_dir / f"consumer-{mode}.json"
    raw_file = run_dir / f"raw-output-{mode}.pcm"
    existed_before = raw_file.exists()
    if existed_before:
        raise EvidenceError(f"public raw output unexpectedly exists before consumer: {raw_file}")
    execution = run_consumer(binary, mode, report_path, raw_file, revision, negative)
    execution["raw_file_observation"] = observe_raw_file(raw_file, existed_before)
    child_report = execution.get("report", {})
    child_control = child_report.get("raw_file_control", {})
    if child_control.get("passed") is not True:
        raise EvidenceError(f"consumer did not pass raw file control: {child_report}")
    for field in ("path", "bytes", "sha256", "hex", "final_tail_hex"):
        if child_control.get(field) != execution["raw_file_observation"].get(field):
            raise EvidenceError(f"consumer/raw observation mismatch for {field}: {child_report}")
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
        "tested_source": identity,
        "build_inputs": inputs,
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


def runtime_regression(yui: Path, run_dir: Path, source: dict, yui_source_root: Path) -> dict:
    if not yui.is_file():
        raise EvidenceError(f"runtime regression yui is unavailable: {yui}")
    if not TOOL_FIXTURE.is_file():
        raise EvidenceError(f"runtime regression fixture is unavailable: {TOOL_FIXTURE}")
    yui_source = source_identity(yui_source_root, YUI_SOURCE_ROOTS)
    if not yui_source["clean"]:
        raise EvidenceError(f"yui build inputs are dirty: {yui_source['tracked_status']}")
    if yui_source["revision"] != source["revision"]:
        raise EvidenceError(
            f"yui source revision {yui_source['revision']} differs from tested source {source['revision']}"
        )
    work = Path(tempfile.mkdtemp(prefix="c33-runtime-", dir=run_dir))
    config = work / "config"
    config.mkdir()
    (work / "evidence/runs").mkdir(parents=True)
    output = work / "rendered.pcm"
    bundle = work / "bundle"
    capture = run_process(
        [
            str(yui),
            "-C",
            str(config),
            "session",
            "--replay",
            str(TOOL_FIXTURE),
            "--audio-out",
            str(output),
            "--record-dir",
            str(bundle),
            "--trace-audio",
            "--max-duration",
            "60s",
            "--workdir",
            str(work),
            "--allow-path",
            str(work),
        ],
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
        "yui_source": yui_source,
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
    parser.add_argument("--yui-source-root", type=Path)
    parser.add_argument("--report", type=Path)
    args = parser.parse_args()
    run_dir = EVIDENCE / "runs" / f"run-{time.strftime('%Y%m%dT%H%M%SZ', time.gmtime())}-{os.getpid()}"
    run_dir.mkdir(parents=True, exist_ok=True)
    result: dict = {
        "run_dir": str(run_dir),
        "argv": sys.argv,
        "runner_sha256": sha256_file(Path(__file__).resolve()),
        "passed": False,
    }
    try:
        source_root = args.source_root.resolve()
        result["timeout_cleanup"] = timeout_cleanup_control(run_dir)
        if not result["timeout_cleanup"]["passed"]:
            raise EvidenceError(result["timeout_cleanup"].get("error", "timeout cleanup control failed"))
        if args.runtime_regression:
            if args.yui is None or args.yui_source_root is None:
                raise EvidenceError("--runtime-regression requires --yui and --yui-source-root")
            source = source_identity(source_root, CONSUMER_SOURCE_ROOTS)
            if not source["clean"]:
                raise EvidenceError(f"runtime source inputs are dirty: {source['tracked_status']}")
            result["source_root"] = str(source_root)
            result["source_revision"] = source["revision"]
            result["tested_source"] = source
            result["runtime_regression"] = runtime_regression(
                args.yui.resolve(), run_dir, source, args.yui_source_root.resolve()
            )
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
