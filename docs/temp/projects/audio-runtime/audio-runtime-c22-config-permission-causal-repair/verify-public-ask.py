#!/usr/bin/env python3
"""Run a credential-free local ask/record/replay check against a yui binary."""

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


PROMPT = "c22 deterministic ask"
ANSWER = "c22 local answer"


class ProbeFailure(Exception):
    """An observed ask/replay result did not match its oracle."""


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def git_root() -> Path:
    result = subprocess.run(
        ["git", "rev-parse", "--show-toplevel"],
        cwd=Path(__file__).resolve().parent,
        check=True,
        capture_output=True,
        text=True,
    )
    return Path(result.stdout.strip()).resolve()


def child_environment(home: Path) -> dict[str, str]:
    environment = os.environ.copy()
    for name in list(environment):
        upper = name.upper()
        if any(token in upper for token in ("API_KEY", "TOKEN", "PASSWORD", "SECRET")):
            del environment[name]
    environment["HOME"] = str(home)
    environment["USERPROFILE"] = str(home)
    environment["XDG_CONFIG_HOME"] = str(home / ".config")
    return environment


def kill_process_group(process: subprocess.Popen[str]) -> None:
    if process.poll() is not None:
        return
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    process.wait()


def run_child(binary: Path, argv: list[str], cwd: Path, timeout: float, environment: dict[str, str]) -> dict[str, Any]:
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
    )
    timed_out = False
    try:
        stdout, stderr = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        timed_out = True
        kill_process_group(process)
        stdout, stderr = process.communicate()
    return {
        "argv": [str(binary), *argv],
        "cwd": str(cwd),
        "timeout_seconds": timeout,
        "duration_seconds": round(time.monotonic() - started, 6),
        "exit_code": process.returncode,
        "timed_out": timed_out,
        "stdout": stdout,
        "stderr": stderr,
    }


class LocalSSEFixture:
    def __init__(self) -> None:
        self.paths: list[str] = []
        self._server: ThreadingHTTPServer | None = None
        self._thread: threading.Thread | None = None

    def start(self) -> str:
        fixture = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def do_POST(self) -> None:  # noqa: N802 - required HTTPServer hook
                fixture.paths.append(self.path)
                length = int(self.headers.get("Content-Length", "0"))
                self.rfile.read(length)
                body = (
                    'data: {"choices":[{"delta":{"role":"assistant","content":"'
                    + ANSWER
                    + '"},"finish_reason":null}]}\n\n'
                    'data: {"choices":[{"delta":{},"finish_reason":"stop"}]}\n\n'
                    "data: [DONE]\n\n"
                ).encode()
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
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
                raise ProbeFailure("local SSE fixture did not stop before replay")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--child-timeout-seconds", type=float, default=60.0)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    if args.child_timeout_seconds <= 0 or args.child_timeout_seconds > 60:
        print("--child-timeout-seconds must be in (0, 60]", file=sys.stderr)
        return 2
    binary = args.binary.resolve()
    if not binary.is_file():
        print(f"binary is unavailable: {binary}", file=sys.stderr)
        return 2
    repository = git_root()
    results: list[dict[str, Any]] = []
    failure = ""
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c22-public-ask-") as temporary:
        root = Path(temporary)
        home = root / "home"
        config_dir = root / "config"
        home.mkdir()
        config_dir.mkdir()
        capture = root / "capture.json"
        fixture = LocalSSEFixture()
        base_url = fixture.start()
        common = [
            "-C",
            str(config_dir),
            "ask",
            "--stream",
            "--system-prompt",
            "none",
            "--no-system-information",
            "--provider",
            "local",
            "--model",
            "c22-capture-model",
            "--base-url",
            base_url,
        ]
        try:
            record_argv = [*common, "--record", str(capture), PROMPT]
            record = run_child(binary, record_argv, root, args.child_timeout_seconds, child_environment(home))
            results.append({"label": "record", **record})
            if record["timed_out"] or record["exit_code"] != 0:
                raise ProbeFailure(f"record failed: {record['stdout']!r} {record['stderr']!r}")
            if record["stdout"] != ANSWER:
                raise ProbeFailure(f"record output={record['stdout']!r}, want {ANSWER!r}")
            if not capture.is_file():
                raise ProbeFailure("record did not write a capture")
            captured = json.loads(capture.read_text(encoding="utf-8"))
            if len(captured) != 1 or ANSWER not in captured[0]["response"]["body"]:
                raise ProbeFailure("capture does not retain the expected response body")
            fixture.stop()
            replay_argv = [*common, "--replay", str(capture), PROMPT]
            replay = run_child(binary, replay_argv, root, args.child_timeout_seconds, child_environment(home))
            results.append({"label": "replay-after-helper-stop", **replay})
            if replay["timed_out"] or replay["exit_code"] != 0:
                raise ProbeFailure(f"replay failed: {replay['stdout']!r} {replay['stderr']!r}")
            if replay["stdout"] != record["stdout"]:
                raise ProbeFailure(f"replay output={replay['stdout']!r}, record={record['stdout']!r}")
        except (ProbeFailure, KeyError, json.JSONDecodeError) as error:
            failure = str(error)
        finally:
            if fixture._thread is not None and fixture._thread.is_alive():
                fixture.stop()
        report = {
            "schema": "audio-runtime-c22-public-ask-v1",
            "source_revision": subprocess.run(
                ["git", "rev-parse", "HEAD"], cwd=repository, check=True, capture_output=True, text=True
            ).stdout.strip(),
            "binary": str(binary),
            "binary_sha256": sha256_file(binary),
            "prompt": PROMPT,
            "answer": ANSWER,
            "fixture_paths": fixture.paths,
            "capture_sha256": sha256_file(capture) if capture.is_file() else "",
            "results": results,
            "status": "passed" if not failure else "failed",
            "failure": failure,
        }
    report_path = (args.output or Path(__file__).resolve().parent / "verify-public-ask.json").resolve()
    report_path.parent.mkdir(parents=True, exist_ok=True)
    report_path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps({"status": report["status"], "results": len(results), "failure": failure}, sort_keys=True))
    return 0 if not failure else 1


if __name__ == "__main__":
    raise SystemExit(main())
