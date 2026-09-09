#!/usr/bin/env python3
"""Exercise the shipped public ``yui config add-local`` workflow.

Each invocation has an isolated HOME/config directory, a local-only models
fixture, and a child-process umask. Existing files are explicitly chmoded after
creation so the workflow evidence does not repeat the fixture bug that C22
repairs. The stale case mutates the config during the probe and requires the
typed revision conflict to leave the newer bytes and mode untouched.
"""

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
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any


SEED_CONFIG = """model:
  provider: openrouter
  openrouter:
    model: c22-seed-model
    api_key: \"\"
    base_url: https://example.test/v1
"""
NEWER_CONFIG = """model:
  provider: local
  local:
    model: c22-newer-model
    api_key: \"\"
    base_url: https://example.test/v1
"""


class ProbeFailure(Exception):
    """An observed public workflow result did not match its oracle."""


def sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def sha256_file(path: Path) -> str:
    return sha256_bytes(path.read_bytes())


def git_root() -> Path:
    completed = subprocess.run(
        ["git", "rev-parse", "--show-toplevel"],
        cwd=Path(__file__).resolve().parent,
        check=True,
        capture_output=True,
        text=True,
    )
    return Path(completed.stdout.strip()).resolve()


def sanitized_environment(home: Path) -> dict[str, str]:
    environment = os.environ.copy()
    for name in list(environment):
        upper = name.upper()
        if any(token in upper for token in ("API_KEY", "TOKEN", "PASSWORD", "SECRET")):
            del environment[name]
    environment["HOME"] = str(home)
    environment["USERPROFILE"] = str(home)
    environment["XDG_CONFIG_HOME"] = str(home / ".config")
    return environment


def terminate_process_group(process: subprocess.Popen[str]) -> None:
    if process.poll() is not None:
        return
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    process.wait()


def run_child(
    binary: Path,
    argv: list[str],
    cwd: Path,
    mask: int,
    timeout: float,
    environment: dict[str, str],
) -> dict[str, Any]:
    started = time.monotonic()
    process = subprocess.Popen(
        [str(binary), *argv],
        cwd=cwd,
        env=environment,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        start_new_session=True,
        preexec_fn=lambda: os.umask(mask),
    )
    timed_out = False
    try:
        stdout, stderr = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        timed_out = True
        terminate_process_group(process)
        stdout, stderr = process.communicate()
    return {
        "argv": [str(binary), *argv],
        "cwd": str(cwd),
        "umask": f"{mask:03o}",
        "timeout_seconds": timeout,
        "duration_seconds": round(time.monotonic() - started, 6),
        "exit_code": process.returncode,
        "timed_out": timed_out,
        "stdout": stdout,
        "stderr": stderr,
    }


class ModelsFixture:
    def __init__(self, config_path: Path, mutate: bool) -> None:
        self.config_path = config_path
        self.mutate = mutate
        self.mutated = False
        self.paths: list[str] = []
        self._server: ThreadingHTTPServer | None = None
        self._thread: threading.Thread | None = None

    def start(self) -> str:
        fixture = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def do_GET(self) -> None:  # noqa: N802 - required HTTPServer hook
                fixture.paths.append(self.path)
                if self.path != "/v1/models":
                    self.send_error(404)
                    return
                if fixture.mutate and not fixture.mutated:
                    fixture.config_path.write_text(NEWER_CONFIG, encoding="utf-8")
                    os.chmod(fixture.config_path, 0o640)
                    fixture.mutated = True
                body = b'{"data":[]}\n'
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)
                self.wfile.flush()

            def log_message(self, _format: str, *_args: Any) -> None:
                return

        class QuietThreadingHTTPServer(ThreadingHTTPServer):
            def handle_error(self, _request: Any, _client_address: Any) -> None:
                return

        self._server = QuietThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True)
        self._thread.start()
        return f"http://127.0.0.1:{self._server.server_port}/v1"

    def stop(self) -> None:
        if self._server is not None:
            self._server.shutdown()
            self._server.server_close()
        if self._thread is not None:
            self._thread.join(timeout=2)
            if self._thread.is_alive():
                raise ProbeFailure("local models fixture did not stop")


def write_seed(path: Path, data: str, mode: int) -> None:
    path.write_text(data, encoding="utf-8")
    os.chmod(path, mode)
    if (path.stat().st_mode & 0o777) != mode:
        raise ProbeFailure(f"seed mode={(path.stat().st_mode & 0o777):03o}, want {mode:03o}")


def assert_no_config_artifacts(config_dir: Path) -> None:
    artifacts = [
        path.name
        for path in config_dir.iterdir()
        if path.name == "config.yaml.lock" or path.name.startswith(".config.yaml.tmp-")
    ]
    if artifacts:
        raise ProbeFailure(f"config lock/temp artifacts remain: {artifacts}")


def observation(path: Path) -> dict[str, Any]:
    data = path.read_bytes()
    return {
        "bytes": len(data),
        "mode": f"{path.stat().st_mode & 0o777:03o}",
        "sha256": sha256_bytes(data),
    }


def run_config_case(
    binary: Path,
    mask: int,
    timeout: float,
    stale: bool,
    existing: bool,
) -> dict[str, Any]:
    label = "stale" if stale else "success"
    label += "/existing-0640" if existing else "/absent-default"
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c22-public-config-") as temporary:
        root = Path(temporary)
        home = root / "home"
        config_dir = root / "config"
        home.mkdir()
        config_dir.mkdir()
        config_path = config_dir / "config.yaml"
        if existing:
            write_seed(config_path, SEED_CONFIG, 0o640)
        fixture = ModelsFixture(config_path, mutate=stale)
        base_url = fixture.start()
        argv = [
            "--config-dir",
            str(config_dir),
            "config",
            "add-local",
            "--base-url",
            base_url,
            "--model",
            "c22-dummy-model",
        ]
        try:
            result = run_child(binary, argv, root, mask, timeout, sanitized_environment(home))
        finally:
            fixture.stop()
        record: dict[str, Any] = {
            "label": label,
            "umask": f"{mask:03o}",
            "existing": existing,
            "stale": stale,
            "server_paths": fixture.paths,
            "server_mutated": fixture.mutated,
            "result": result,
        }
        combined = result["stdout"] + result["stderr"]
        if stale:
            if result["timed_out"] or result["exit_code"] == 0:
                raise ProbeFailure(f"{label} unexpectedly succeeded or timed out: {combined!r}")
            if "config revision conflict" not in combined:
                raise ProbeFailure(f"{label} lost the typed conflict: {combined!r}")
            if "Local provider added to" in combined:
                raise ProbeFailure(f"{label} printed a success summary")
            if not fixture.mutated:
                raise ProbeFailure(f"{label} did not cross the probe mutation barrier")
            after = observation(config_path)
            if config_path.read_text(encoding="utf-8") != NEWER_CONFIG:
                raise ProbeFailure(f"{label} changed newer config bytes")
            if after["mode"] != "640":
                raise ProbeFailure(f"{label} changed newer config mode to {after['mode']}")
        else:
            if result["timed_out"] or result["exit_code"] != 0:
                raise ProbeFailure(f"{label} failed: {combined!r}")
            for marker in (
                "Local provider added to",
                "provider: local",
                "base_url: " + base_url,
                "model: c22-dummy-model",
            ):
                if marker not in result["stdout"]:
                    raise ProbeFailure(f"{label} missing stdout marker {marker!r}")
            after = observation(config_path)
            expected_mode = "640" if existing else "600"
            if after["mode"] != expected_mode:
                raise ProbeFailure(f"{label} mode={after['mode']}, want {expected_mode}")
            data = config_path.read_text(encoding="utf-8")
            for marker in ("provider: local", "model: c22-dummy-model", "base_url: " + base_url):
                if marker not in data:
                    raise ProbeFailure(f"{label} persisted config missing {marker!r}")
        assert_no_config_artifacts(config_dir)
        record["config_observation"] = observation(config_path)
        record["config_bytes"] = config_path.read_text(encoding="utf-8")
        return record


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--umasks", default="022,077")
    parser.add_argument("--child-timeout-seconds", type=float, default=60.0)
    parser.add_argument("--output", type=Path)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    if os.name != "posix":
        print("public config evidence requires POSIX child umask isolation", file=sys.stderr)
        return 2
    if args.child_timeout_seconds <= 0 or args.child_timeout_seconds > 60:
        print("--child-timeout-seconds must be in (0, 60]", file=sys.stderr)
        return 2
    binary = args.binary.resolve()
    if not binary.is_file():
        print(f"binary is unavailable: {binary}", file=sys.stderr)
        return 2
    try:
        masks = [int(value, 8) for value in args.umasks.split(",")]
        if not masks or any(mask < 0 or mask > 0o777 for mask in masks):
            raise ValueError
    except ValueError:
        print("--umasks must be a comma-separated octal list", file=sys.stderr)
        return 2
    repository = git_root()
    results: list[dict[str, Any]] = []
    failures: list[str] = []
    for mask in masks:
        for stale, existing in ((False, True), (False, False), (True, True)):
            try:
                results.append(run_config_case(binary, mask, args.child_timeout_seconds, stale, existing))
            except ProbeFailure as error:
                failures.append(f"umask {mask:03o}: {error}")
    report = {
        "schema": "audio-runtime-c22-public-config-v1",
        "source_revision": subprocess.run(
            ["git", "rev-parse", "HEAD"], cwd=repository, check=True, capture_output=True, text=True
        ).stdout.strip(),
        "binary": str(binary),
        "binary_sha256": sha256_file(binary),
        "umasks": [f"{mask:03o}" for mask in masks],
        "child_timeout_seconds": args.child_timeout_seconds,
        "results": results,
        "status": "passed" if not failures else "failed",
        "failures": failures,
    }
    report_path = (args.output or Path(__file__).resolve().parent / "verify-public-config.json").resolve()
    report_path.parent.mkdir(parents=True, exist_ok=True)
    report_path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps({"status": report["status"], "results": len(results), "failures": failures}, sort_keys=True))
    return 0 if not failures else 1


if __name__ == "__main__":
    raise SystemExit(main())
