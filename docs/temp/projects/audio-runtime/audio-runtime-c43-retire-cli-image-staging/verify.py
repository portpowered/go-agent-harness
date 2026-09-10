#!/usr/bin/env python3
"""Bounded C43 evidence runner.

The runner checks the private staging controls, the public Wire consumer, the
shipped CLI image workflow, and a credential-free tool replay regression. The
wrong-oracle control is expected to fail; it is recorded as a passing negative
control only when the public consumer rejects the deliberately wrong bytes.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import re
import select
import signal
import shutil
import socket
import subprocess
import sys
import threading
import time
from typing import Any
from urllib.parse import parse_qs, urlparse
from urllib.request import urlopen


EVIDENCE_DIR = Path(__file__).resolve().parent
REPO_ROOT = Path(
    subprocess.run(
        ["rtk", "proxy", "git", "rev-parse", "--show-toplevel"],
        cwd=EVIDENCE_DIR,
        check=True,
        capture_output=True,
        text=True,
    ).stdout.strip()
).resolve()
CONSUMER_DIR = EVIDENCE_DIR / "consumer"
MAX_CHILD_SECONDS = 60.0
MAX_TOTAL_SECONDS = 600.0
SOURCE_BASELINE = "926ded7bfa8f3c3e42115192d03aa1240c4806db"
STAGING_SOURCE = Path("agent-cli/internal/services/internal/agentruntime/session_image_staging.go")
RETIRED_SYMBOLS = (
    "sessionImageToolPathDescription",
    "sessionImageStageExtension",
    "advertiseSessionImagePaths",
)
RETAINED_SYMBOLS = ("prepareSessionImageToolAccess", "sessionImageStagingConfigDir")
IMAGE_MODE = "image"
TIMEOUT_MODE = "timeout"


class EvidenceFailure(RuntimeError):
    pass


def clipped(value: str, limit: int = 16000) -> str:
    if len(value) <= limit:
        return value
    return value[:limit] + "\n...[clipped]"


def terminate_group(process: subprocess.Popen[str]) -> None:
    if os.name == "nt":
        process.kill()
        return
    try:
        os.killpg(process.pid, signal.SIGTERM)
        process.wait(timeout=3)
    except (ProcessLookupError, subprocess.TimeoutExpired):
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        try:
            process.wait(timeout=3)
        except subprocess.TimeoutExpired:
            process.kill()


def text_output(value: str | bytes | None) -> str:
    if value is None:
        return ""
    if isinstance(value, bytes):
        return value.decode("utf-8", errors="replace")
    return value


def process_group_exists(process: subprocess.Popen[str]) -> bool:
    if os.name == "nt" or process.poll() is None:
        return process.poll() is None
    try:
        os.killpg(process.pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def command_text(argv: list[str], cwd: Path) -> str:
    result = subprocess.run(
        argv,
        cwd=cwd,
        check=True,
        capture_output=True,
        text=True,
    )
    return result.stdout.strip()


def source_delta_evidence(run_dir: Path, current_revision: str) -> dict[str, Any]:
    baseline = command_text(
        ["rtk", "proxy", "git", "show", f"{SOURCE_BASELINE}:{STAGING_SOURCE}"],
        REPO_ROOT,
    )
    current_path = REPO_ROOT / STAGING_SOURCE
    current = current_path.read_text(encoding="utf-8")
    diff = subprocess.run(
        [
            "rtk",
            "proxy",
            "git",
            "diff",
            "--no-ext-diff",
            f"{SOURCE_BASELINE}..{current_revision}",
            "--",
            str(STAGING_SOURCE),
        ],
        cwd=REPO_ROOT,
        check=True,
        capture_output=True,
        text=True,
    ).stdout
    delta_path = run_dir / "source-delta.diff"
    delta_path.write_text(diff, encoding="utf-8")
    inventory = {
        "baseline_revision": SOURCE_BASELINE,
        "current_revision": current_revision,
        "source_path": str(STAGING_SOURCE),
        "baseline_source_lines": len(baseline.splitlines()),
        "final_adapter_lines": len(current.splitlines()),
        "retired_symbols": [
            {
                "symbol": symbol,
                "baseline_present": symbol in baseline,
                "final_present": symbol in current,
            }
            for symbol in RETIRED_SYMBOLS
        ],
        "retained_adapter_symbols": [
            {
                "symbol": symbol,
                "baseline_present": symbol in baseline,
                "final_present": symbol in current,
            }
            for symbol in RETAINED_SYMBOLS
        ],
        "source_delta_sha256": sha256_file(delta_path),
        "source_delta_path": str(delta_path),
    }
    for symbol in RETIRED_SYMBOLS:
        if symbol not in baseline or symbol in current:
            raise EvidenceFailure(f"source retirement inventory mismatch for {symbol}")
    for symbol in RETAINED_SYMBOLS:
        if symbol not in current:
            raise EvidenceFailure(f"required adapter symbol missing from final source: {symbol}")
    if inventory["baseline_source_lines"] != 142 or inventory["final_adapter_lines"] != 71:
        raise EvidenceFailure(f"source line delta was not 142-to-71: {inventory}")
    inventory_path = run_dir / "symbol-inventory.json"
    inventory_path.write_text(json.dumps(inventory, indent=2) + "\n", encoding="utf-8")
    inventory["inventory_path"] = str(inventory_path)
    return inventory


def parse_literal_png(source_path: Path) -> bytes:
    source = source_path.read_text(encoding="utf-8")
    match = re.search(r"var literalPNG = \[\]byte\{(.*?)\n\}", source, re.DOTALL)
    if match is None:
        raise EvidenceFailure(f"literal PNG declaration missing from {source_path}")
    values = [int(token, 16) for token in re.findall(r"0x[0-9a-fA-F]+", match.group(1))]
    if not values:
        raise EvidenceFailure(f"literal PNG declaration was empty in {source_path}")
    return bytes(values)


class Runner:
    def __init__(self, mode: str) -> None:
        stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
        self.run_dir = EVIDENCE_DIR / "runs" / f"verify-{stamp}-{os.getpid()}"
        self.run_dir.mkdir(parents=True, exist_ok=True)
        self.mode = mode
        self.started = time.monotonic()
        self.sequence = 0
        self.steps: list[dict[str, Any]] = []

    def remaining(self) -> float:
        value = MAX_TOTAL_SECONDS - (time.monotonic() - self.started)
        if value <= 0:
            raise EvidenceFailure("aggregate verifier deadline exceeded")
        return min(value, MAX_CHILD_SECONDS)

    def command(
        self,
        label: str,
        argv: list[str],
        cwd: Path,
        *,
        expect_success: bool = True,
        required_output: str = "",
    ) -> dict[str, Any]:
        self.sequence += 1
        safe_label = "".join(char if char.isalnum() or char in "-_" else "_" for char in label)
        stdout_path = self.run_dir / f"{self.sequence:03d}-{safe_label}.stdout.log"
        stderr_path = self.run_dir / f"{self.sequence:03d}-{safe_label}.stderr.log"
        environment = os.environ.copy()
        if cwd == CONSUMER_DIR:
            environment["GOWORK"] = "off"
        started = time.monotonic()
        timed_out = False
        process = subprocess.Popen(
            argv,
            cwd=cwd,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            env=environment,
            start_new_session=os.name != "nt",
        )
        try:
            stdout, stderr = process.communicate(timeout=self.remaining())
        except subprocess.TimeoutExpired as exc:
            timed_out = True
            stdout = text_output(exc.stdout)
            stderr = text_output(exc.stderr)
            terminate_group(process)
        stdout = text_output(stdout)
        stderr = text_output(stderr)
        duration = time.monotonic() - started
        stdout_path.write_text(stdout, encoding="utf-8", errors="replace")
        stderr_path.write_text(stderr, encoding="utf-8", errors="replace")
        step = {
            "label": label,
            "argv": argv,
            "cwd": str(cwd),
            "exit_code": process.returncode,
            "timed_out": timed_out,
            "duration_seconds": round(duration, 6),
            "stdout_log": str(stdout_path),
            "stderr_log": str(stderr_path),
            "stdout": clipped(stdout),
            "stderr": clipped(stderr),
        }
        self.steps.append(step)
        success = not timed_out and process.returncode == 0
        if expect_success and not success:
            raise EvidenceFailure(f"{label} failed with exit {process.returncode}; see {stderr_path}")
        if not expect_success:
            if success:
                raise EvidenceFailure(f"{label} unexpectedly passed; wrong-oracle control was not active")
            combined = stdout + "\n" + stderr
            if required_output and required_output not in combined:
                raise EvidenceFailure(f"{label} failed without required negative-control evidence {required_output!r}")
        return step

    def save(self, outcome: dict[str, Any]) -> None:
        (self.run_dir / "outcome.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")


class PageFixture:
    def __init__(self, page: bytes) -> None:
        self.page = page
        self.events: list[str] = []
        self.server: ThreadingHTTPServer | None = None
        self.thread: threading.Thread | None = None

    def __enter__(self) -> "PageFixture":
        page = self.page
        events = self.events

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self) -> None:  # noqa: N802 - stdlib callback name
                parsed = urlparse(self.path)
                if parsed.path == "/c43-status":
                    state = parse_qs(parsed.query).get("state", [""])[0]
                    if state:
                        events.append(state)
                    self.send_response(204)
                    self.end_headers()
                    return
                if parsed.path != "/c43.html":
                    self.send_error(404)
                    return
                self.send_response(200)
                self.send_header("Content-Type", "text/html; charset=utf-8")
                self.send_header("Content-Length", str(len(page)))
                self.end_headers()
                self.wfile.write(page)

            def log_message(self, _format: str, *_args: Any) -> None:
                return

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        return self

    @property
    def origin(self) -> str:
        if self.server is None:
            raise EvidenceFailure("page fixture server is not running")
        host, port = self.server.server_address
        return f"http://{host}:{port}"

    @property
    def url(self) -> str:
        return self.origin + "/c43.html"

    def __exit__(self, _exc_type: Any, _exc: Any, _traceback: Any) -> None:
        if self.server is not None:
            self.server.shutdown()
            self.server.server_close()
        if self.thread is not None:
            self.thread.join(timeout=3)


def free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def wait_for_json_url(url: str, timeout: float) -> Any:
    deadline = time.monotonic() + timeout
    last_error = ""
    while time.monotonic() < deadline:
        try:
            with urlopen(url, timeout=0.5) as response:
                return json.loads(response.read().decode("utf-8"))
        except Exception as exc:  # endpoint startup failures are recorded below
            last_error = str(exc)
            time.sleep(0.1)
    raise EvidenceFailure(f"timed out waiting for {url}: {last_error}")


def chrome_path() -> str:
    candidates = [
        os.environ.get("C43_CHROME_PATH", ""),
        "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
        shutil.which("google-chrome") or "",
        shutil.which("chromium") or "",
        shutil.which("chromium-browser") or "",
    ]
    for candidate in candidates:
        if candidate and Path(candidate).is_file() and os.access(candidate, os.X_OK):
            return candidate
    raise EvidenceFailure("Chrome prerequisite unavailable; install Chrome 151+ or set C43_CHROME_PATH")


def start_chrome(case_dir: Path, page_url: str) -> tuple[subprocess.Popen[str], dict[str, Any], list[Any]]:
    executable = chrome_path()
    port = free_port()
    profile = case_dir / "chrome-profile"
    profile.mkdir(parents=True, exist_ok=True)
    stdout_file = (case_dir / "chrome.stdout.log").open("w", encoding="utf-8")
    stderr_file = (case_dir / "chrome.stderr.log").open("w", encoding="utf-8")
    argv = [
        executable,
        "--headless=new",
        "--disable-gpu",
        "--disable-component-update",
        "--disable-extensions",
        "--disable-features=DelayMediaSinkDiscovery",
        "--disable-sync",
        "--no-default-browser-check",
        "--no-first-run",
        "--remote-debugging-address=127.0.0.1",
        f"--remote-debugging-port={port}",
        "--enable-features=WebMCP,WebMCPTesting,DevToolsWebMCPSupport",
        "--enable-blink-features=DeclarativeWebmcp",
        f"--user-data-dir={profile}",
        page_url,
    ]
    process = subprocess.Popen(
        argv,
        cwd=REPO_ROOT,
        stdin=subprocess.DEVNULL,
        stdout=stdout_file,
        stderr=stderr_file,
        text=True,
        start_new_session=os.name != "nt",
    )
    cdp_url = f"http://127.0.0.1:{port}"
    try:
        version = wait_for_json_url(cdp_url + "/json/version", 12)
        targets = wait_for_json_url(cdp_url + "/json/list", 12)
        (case_dir / "chrome-version.json").write_text(json.dumps(version, indent=2) + "\n", encoding="utf-8")
        (case_dir / "chrome-targets.json").write_text(json.dumps(targets, indent=2) + "\n", encoding="utf-8")
    except Exception:
        terminate_group(process)
        stdout_file.close()
        stderr_file.close()
        raise
    return process, {"executable": executable, "cdp_url": cdp_url, "port": port, "profile": str(profile), "argv": argv}, [stdout_file, stderr_file]


def start_provider(
    provider_binary: Path,
    case_dir: Path,
    mode: str,
    fixture_sha256: str,
    fixture_bytes: int,
    fixture_base64: str,
) -> tuple[subprocess.Popen[str], dict[str, Any], Any]:
    stdout_pipe = subprocess.PIPE
    stderr_file = (case_dir / "provider.stderr.log").open("w", encoding="utf-8")
    result_path = case_dir / "provider-result.json"
    event_log = case_dir / "provider-events.jsonl"
    argv = [
        str(provider_binary),
        "--mode", mode,
        "--result", str(result_path),
        "--log", str(event_log),
        "--fixture-sha256", fixture_sha256,
        "--fixture-bytes", str(fixture_bytes),
        "--fixture-base64", fixture_base64,
    ]
    environment = os.environ.copy()
    environment["GOWORK"] = "off"
    process = subprocess.Popen(
        argv,
        cwd=CONSUMER_DIR,
        stdin=subprocess.DEVNULL,
        stdout=stdout_pipe,
        stderr=stderr_file,
        text=True,
        env=environment,
        start_new_session=os.name != "nt",
    )
    if process.stdout is None or not select.select([process.stdout], [], [], 8)[0]:
        terminate_group(process)
        stderr_file.close()
        raise EvidenceFailure("local provider did not publish its ready endpoint")
    ready_line = text_output(process.stdout.readline())
    try:
        ready = json.loads(ready_line)
    except json.JSONDecodeError as exc:
        terminate_group(process)
        stderr_file.close()
        raise EvidenceFailure(f"local provider ready line was not JSON: {ready_line!r}") from exc
    if not ready.get("ready") or not isinstance(ready.get("url"), str):
        terminate_group(process)
        stderr_file.close()
        raise EvidenceFailure(f"local provider ready response was incomplete: {ready}")
    ready["argv"] = argv
    ready["result_path"] = str(result_path)
    ready["event_log"] = str(event_log)
    ready["ready_line"] = ready_line.strip()
    return process, ready, stderr_file


def scrub_external_credentials(environment: dict[str, str]) -> dict[str, str]:
    scrubbed = dict(environment)
    for key in list(scrubbed):
        if key.startswith("AGENT_MODEL__") or key in {
            "OPENAI_API_KEY",
            "OPENAI_API_TOKEN",
            "OPENAI_API_BASE",
            "OPENAI_BASE_URL",
        }:
            scrubbed.pop(key, None)
    return scrubbed


def write_session_config(config_dir: Path, *, read_image: bool, exec_enabled: bool) -> None:
    entries = [
        ("exec", exec_enabled),
        ("read_file", False),
        ("read_image", read_image),
        ("write_file", False),
        ("edit_file", False),
        ("append_file", False),
        ("list_dir", False),
        ("web_fetch", False),
        ("web_search", False),
        ("show", False),
        ("mouse", False),
        ("load_skill", False),
        ("sleep", False),
    ]
    lines = [
        "model:",
        "  provider: openai",
        "  openai:",
        "    model: gpt-realtime",
        "tools:",
        "  exec:",
        "    enable_deny_patterns: true",
        "  list:",
    ]
    for tool_id, enabled in entries:
        lines.extend([f"    - id: {tool_id}", f"      enabled: {'true' if enabled else 'false'}"])
    config_dir.mkdir(parents=True, exist_ok=True)
    config_path = config_dir / "config.yaml"
    config_path.write_text("\n".join(lines) + "\n", encoding="utf-8")
    config_path.chmod(0o600)


def run_process_case(
    runner: Runner,
    binary: Path,
    provider_binary: Path,
    page: bytes,
    case_name: str,
    mode: str,
) -> dict[str, Any]:
    case_dir = runner.run_dir / "process" / case_name
    case_dir.mkdir(parents=True, exist_ok=True)
    config_dir = case_dir / "config"
    write_session_config(config_dir, read_image=True, exec_enabled=False)
    literal_source = CONSUMER_DIR / "main.go"
    fixture_bytes = parse_literal_png(literal_source)
    fixture_path = case_dir / "literal.png"
    fixture_path.write_bytes(fixture_bytes)
    fixture_path.chmod(0o600)
    fixture_sha256 = sha256_bytes(fixture_bytes)
    fixture_base64 = base64.b64encode(fixture_bytes).decode("ascii")
    page_path = EVIDENCE_DIR / "browser" / "page.html"
    page_sha256 = sha256_file(page_path)
    cli_stdout_path = case_dir / "cli.stdout.log"
    cli_stderr_path = case_dir / "cli.stderr.log"
    cli_stdout_file = cli_stdout_path.open("w", encoding="utf-8")
    cli_stderr_file = cli_stderr_path.open("w", encoding="utf-8")
    provider: subprocess.Popen[str] | None = None
    provider_stderr_file: Any = None
    chrome: subprocess.Popen[str] | None = None
    chrome_handles: list[Any] = []
    cli: subprocess.Popen[str] | None = None
    provider_ready: dict[str, Any] = {}
    chrome_info: dict[str, Any] = {}
    cli_argv: list[str] = []
    started = time.monotonic()
    cli_timed_out = False
    cli_exit: int | None = None
    with PageFixture(page) as page_fixture:
        try:
            provider, provider_ready, provider_stderr_file = start_provider(
                provider_binary,
                case_dir,
                mode,
                fixture_sha256,
                len(fixture_bytes),
                fixture_base64,
            )
            chrome, chrome_info, chrome_handles = start_chrome(case_dir, page_fixture.url)
            cli_argv = [
                str(binary),
                "--config-dir", str(config_dir),
                "--workdir", str(REPO_ROOT),
                "session",
                "--provider", "openai",
                "--model", "gpt-realtime",
                "--api-key", "c43-hermetic-key",
                "--base-url", provider_ready["url"],
                "--system-prompt", "none",
                "--image", str(fixture_path),
                "--browser-tools", "webmcp",
                "--browser-cdp-url", chrome_info["cdp_url"],
                "--browser-auto-select", "single",
                "--browser-allowed-origin", page_fixture.origin,
                "--browser-approval", "never",
                "--record-dir", str(case_dir / "recording"),
                "--max-duration", "20s" if mode == IMAGE_MODE else "2s",
                "--wait-for-close",
            ]
            cli_environment = scrub_external_credentials(os.environ.copy())
            cli = subprocess.Popen(
                cli_argv,
                cwd=REPO_ROOT,
                stdin=subprocess.DEVNULL,
                stdout=cli_stdout_file,
                stderr=cli_stderr_file,
                text=True,
                env=cli_environment,
                start_new_session=os.name != "nt",
            )
            try:
                cli.wait(timeout=runner.remaining())
            except subprocess.TimeoutExpired:
                cli_timed_out = True
                terminate_group(cli)
            cli_exit = cli.returncode
        finally:
            if cli is not None and cli.poll() is None:
                terminate_group(cli)
            if provider is not None and provider.poll() is None:
                terminate_group(provider)
            if chrome is not None:
                terminate_group(chrome)
            if provider is not None:
                try:
                    provider.wait(timeout=3)
                except subprocess.TimeoutExpired:
                    terminate_group(provider)
            if chrome is not None:
                try:
                    chrome.wait(timeout=3)
                except subprocess.TimeoutExpired:
                    terminate_group(chrome)
    cli_stdout_file.close()
    cli_stderr_file.close()
    if provider_stderr_file is not None:
        provider_stderr_file.close()
    for handle in chrome_handles:
        handle.close()
    duration = time.monotonic() - started
    provider_stdout = ""
    if provider is not None and provider.stdout is not None:
        provider_stdout = text_output(provider.stdout.read())
    (case_dir / "provider.stdout.log").write_text(provider_ready.get("ready_line", "") + provider_stdout, encoding="utf-8")
    provider_result_path = Path(provider_ready["result_path"]) if provider_ready else case_dir / "provider-result.json"
    provider_result: dict[str, Any] = {}
    if provider_result_path.is_file():
        provider_result = json.loads(provider_result_path.read_text(encoding="utf-8"))
    cli_stdout = cli_stdout_path.read_text(encoding="utf-8")
    cli_stderr = cli_stderr_path.read_text(encoding="utf-8")
    leftovers = sorted(str(path) for path in config_dir.rglob(".session-images-*") if path.exists())
    process_groups = {
        "cli": process_group_exists(cli) if cli is not None else False,
        "provider": process_group_exists(provider) if provider is not None else False,
        "chrome": process_group_exists(chrome) if chrome is not None else False,
    }
    observation: dict[str, Any] = {
        "case": case_name,
        "mode": mode,
        "cli_argv": cli_argv,
        "duration_seconds": round(duration, 6),
        "cli_exit_code": cli_exit,
        "cli_timed_out": cli_timed_out,
        "cli_stdout_log": str(cli_stdout_path),
        "cli_stderr_log": str(cli_stderr_path),
        "cli_stdout": clipped(cli_stdout),
        "cli_stderr": clipped(cli_stderr),
        "provider": provider_ready,
        "provider_result": provider_result,
        "chrome": chrome_info,
        "fixture": {
            "source": str(literal_source),
            "source_sha256": sha256_file(literal_source),
            "path": str(fixture_path),
            "sha256": fixture_sha256,
            "bytes": len(fixture_bytes),
        },
        "page": {"path": str(page_path), "sha256": page_sha256, "url": page_fixture.url},
        "page_events": list(page_fixture.events),
        "staging_leftovers": leftovers,
        "process_group_survivors": process_groups,
        "network_scope": ["127.0.0.1 page HTTP", "127.0.0.1 CDP", "127.0.0.1 provider WebSocket"],
    }
    (case_dir / "observations.json").write_text(json.dumps(observation, indent=2) + "\n", encoding="utf-8")
    runner.sequence += 1
    runner.steps.append({
        "label": f"shipped-cli-process-{case_name}",
        "argv": observation.get("cli_argv", []),
        "cwd": str(REPO_ROOT),
        "exit_code": cli_exit,
        "timed_out": cli_timed_out,
        "duration_seconds": round(duration, 6),
        "observation": str(case_dir / "observations.json"),
    })
    if mode == IMAGE_MODE:
        if cli_exit != 0 or cli_timed_out:
            raise EvidenceFailure(f"shipped CLI image process failed: exit={cli_exit}, stderr={clipped(cli_stderr)}")
        if provider_result.get("status") != "PASS":
            raise EvidenceFailure(f"local provider image proof failed: {provider_result}")
        if provider_result.get("initial_path") != provider_result.get("refreshed_path"):
            raise EvidenceFailure(f"dynamic refresh changed the staged path: {provider_result}")
        if "c43_refreshed_probe" not in provider_result.get("refreshed_tools", []):
            raise EvidenceFailure(f"dynamic native WebMCP refresh was not observed: {provider_result}")
        if leftovers or any(process_groups.values()):
            raise EvidenceFailure(f"image process left staging or child processes: {observation}")
    else:
        if cli_timed_out or cli_exit is None or duration < 1.0 or duration > MAX_CHILD_SECONDS:
            raise EvidenceFailure(f"induced timeout did not complete as a bounded app shutdown: {observation}")
        if provider_result.get("status") != "PASS" or not provider_result.get("expected_timeout_client_close"):
            raise EvidenceFailure(f"timeout provider proof was not a client-close outcome: {provider_result}")
        if leftovers or any(process_groups.values()):
            raise EvidenceFailure(f"timeout process left staging or child processes: {observation}")
    return observation


def process_inputs(runner: Runner) -> tuple[Path, Path, dict[str, Any]]:
    cached = getattr(runner, "process_artifacts", None)
    if cached is not None:
        return cached
    current_revision = command_text(["rtk", "proxy", "git", "rev-parse", "HEAD"], REPO_ROOT)
    source_info = source_delta_evidence(runner.run_dir, current_revision)
    artifacts_dir = runner.run_dir / "artifacts"
    artifacts_dir.mkdir(parents=True, exist_ok=True)
    cli_binary = artifacts_dir / "yui"
    provider_binary = artifacts_dir / "c43-local-provider"
    runner.command(
        "build-shipped-cli",
        ["rtk", "proxy", "go", "build", "-o", str(cli_binary), "./agent-cli/cmd/yui"],
        REPO_ROOT,
    )
    runner.command(
        "build-local-provider",
        ["rtk", "proxy", "go", "build", "-o", str(provider_binary), "./provider"],
        CONSUMER_DIR,
    )
    if not cli_binary.is_file() or not provider_binary.is_file():
        raise EvidenceFailure("process build completed without both executable artifacts")
    provenance = {
        "source_revision": current_revision,
        "source_info": source_info,
        "cli_binary": str(cli_binary),
        "cli_binary_sha256": sha256_file(cli_binary),
        "provider_binary": str(provider_binary),
        "provider_binary_sha256": sha256_file(provider_binary),
        "provider_source": str(CONSUMER_DIR / "provider" / "main.go"),
        "provider_source_sha256": sha256_file(CONSUMER_DIR / "provider" / "main.go"),
        "provider_go_mod_sha256": sha256_file(CONSUMER_DIR / "go.mod"),
        "provider_go_sum_sha256": sha256_file(CONSUMER_DIR / "go.sum"),
        "page_source": str(EVIDENCE_DIR / "browser" / "page.html"),
        "page_source_sha256": sha256_file(EVIDENCE_DIR / "browser" / "page.html"),
        "go_environment": command_text(["rtk", "proxy", "go", "env", "GOVERSION", "GOOS", "GOARCH"], REPO_ROOT),
        "build_commands": [runner.steps[-2]["argv"], runner.steps[-1]["argv"]],
        "network": "loopback-only deterministic provider and WebMCP page",
    }
    provenance_path = runner.run_dir / "build-provenance.json"
    provenance_path.write_text(json.dumps(provenance, indent=2) + "\n", encoding="utf-8")
    provenance["path"] = str(provenance_path)
    cached = (cli_binary, provider_binary, provenance)
    runner.process_artifacts = cached
    return cached


def consumer_steps(runner: Runner) -> None:
    runner.command(
        "consumer-build-test",
        ["rtk", "proxy", "go", "test", "./...", "-count=1", "-timeout=45s"],
        CONSUMER_DIR,
    )
    runner.command(
        "consumer-positive",
        ["rtk", "proxy", "go", "run", ".", "positive"],
        CONSUMER_DIR,
    )
    runner.command(
        "consumer-wrong-oracle-negative",
        ["rtk", "proxy", "go", "run", ".", "negative"],
        CONSUMER_DIR,
        expect_success=False,
        required_output="wrong PNG oracle",
    )
    runner.command(
        "consumer-cleanup-negative",
        ["rtk", "proxy", "go", "run", ".", "cleanup-negative"],
        CONSUMER_DIR,
    )


def focused_runtime_steps(runner: Runner) -> None:
    runner.command(
        "live-capability-lifecycle",
        [
            "rtk",
            "proxy",
            "go",
            "test",
            "./go-agent-runtime/services/session/internal/live",
            "-run",
            "TestLiveCapabilityHandleOwnsLifecycleAndBrowserEvents",
            "-count=1",
            "-timeout=45s",
        ],
        REPO_ROOT,
    )
    runner.command(
        "live-capability-lifecycle-race",
        [
            "rtk",
            "proxy",
            "go",
            "test",
            "-race",
            "./go-agent-runtime/services/session/internal/live",
            "-run",
            "TestLiveCapabilityHandleOwnsLifecycleAndBrowserEvents",
            "-count=1",
            "-timeout=45s",
        ],
        REPO_ROOT,
    )
    runner.command(
        "private-image-staging",
        [
            "rtk",
            "proxy",
            "go",
            "test",
            "./go-agent-runtime/services/tools/internal/imagestaging",
            "./go-agent-runtime/services/tools/wire",
            "-count=1",
            "-timeout=45s",
        ],
        REPO_ROOT,
    )
    runner.command(
        "private-image-staging-race",
        [
            "rtk",
            "proxy",
            "go",
            "test",
            "-race",
            "./go-agent-runtime/services/tools/internal/imagestaging",
            "-count=1",
            "-timeout=45s",
        ],
        REPO_ROOT,
    )
    runner.command(
        "cli-image-adapter",
        [
            "rtk",
            "proxy",
            "sh",
            "-c",
            "cd agent-cli && go test -tags=nomicrophone ./internal/services/internal/agentruntime -run TestPrepareSessionImageToolAccess -count=1 -timeout=45s",
        ],
        REPO_ROOT,
    )
    runner.command(
        "cli-image-adapter-race",
        [
            "rtk",
            "proxy",
            "sh",
            "-c",
            "cd agent-cli && go test -race -tags=nomicrophone ./internal/services/internal/agentruntime -run TestPrepareSessionImageToolAccess -count=1 -timeout=45s",
        ],
        REPO_ROOT,
    )
    runner.command(
        "tools-regression",
        ["rtk", "proxy", "go", "test", "./go-agent-runtime/services/tools/...", "-count=1", "-timeout=55s"],
        REPO_ROOT,
    )


def public_cli_steps(runner: Runner) -> None:
    runner.command(
        "in-process-cli-image-regression",
        [
            "rtk",
            "proxy",
            "sh",
            "-c",
            "cd agent-cli && go test -tags=nomicrophone ./test/integration -run '^TestSessionCommandImageAndScheduledAudioUsesExactStagedImagePath$' -count=1 -timeout=55s",
        ],
        REPO_ROOT,
    )
    cli_binary, provider_binary, provenance = process_inputs(runner)
    page = (EVIDENCE_DIR / "browser" / "page.html").read_bytes()
    process_observations = [
        run_process_case(runner, cli_binary, provider_binary, page, "image", IMAGE_MODE),
        run_process_case(runner, cli_binary, provider_binary, page, "induced-timeout", TIMEOUT_MODE),
    ]
    runner.process_observations = process_observations
    provenance["process_observations"] = process_observations
    (Path(provenance["path"])).write_text(json.dumps(provenance, indent=2) + "\n", encoding="utf-8")


def strict_exec_capture(path: Path, command: str) -> None:
    marker = "PROBE_TOOL_MARKER_9182"
    call_id = "call_exec_probe_1"
    continuation = "strict replay continuation"
    tool = {
        "type": "function",
        "name": "exec",
        "description": "Execute a shell command and return its output. Use with caution.",
        "parameters": {
            "type": "object",
            "properties": {
                "command": {"type": "string", "description": "The shell command to execute"},
                "working_dir": {"type": "string", "description": "Optional working directory for the command"},
            },
            "required": ["command"],
        },
    }
    records: list[dict[str, Any]] = []

    def record(sequence: int, direction: str, event_type: str, payload: dict[str, Any]) -> None:
        records.append({
            "sequence": sequence,
            "direction": direction,
            "timestamp_ms": sequence,
            "type": event_type,
            "payload_type": "websocket_message",
            "payload": payload,
        })

    record(1, "client_to_server", "session.update", {"type": "session.update", "session": {"type": "realtime", "model": "gpt-realtime", "tools": [tool]}})
    record(2, "server_to_client", "session.created", {"type": "session.created", "session": {"id": "sess-strict-exec-round-trip", "model": "gpt-realtime"}})
    record(3, "client_to_server", "conversation.item.create", {"type": "conversation.item.create", "item": {"type": "message", "role": "user", "content": [{"type": "input_text", "text": f"probe {marker}"}]}})
    record(4, "client_to_server", "response.create", {"type": "response.create"})
    record(5, "server_to_client", "response.created", {"type": "response.created", "response": {"id": "resp-strict-exec-call"}})
    record(6, "server_to_client", "response.output_item.added", {"type": "response.output_item.added", "item": {"type": "function_call", "id": "item-strict-exec-1", "call_id": call_id, "name": "exec"}})
    record(7, "server_to_client", "response.function_call_arguments.done", {"type": "response.function_call_arguments.done", "call_id": call_id, "name": "exec", "arguments": json.dumps({"command": command}, separators=(",", ":"))})
    record(8, "server_to_client", "response.done", {"type": "response.done", "response": {"id": "resp-strict-exec-call", "status": "completed"}})
    record(9, "client_to_server", "conversation.item.create", {"type": "conversation.item.create", "item": {"type": "function_call_output", "call_id": call_id, "output": marker + "\n"}})
    record(10, "client_to_server", "response.create", {"type": "response.create"})
    record(11, "server_to_client", "response.created", {"type": "response.created", "response": {"id": "resp-strict-exec-continuation"}})
    record(12, "server_to_client", "response.output_text.delta", {"type": "response.output_text.delta", "delta": continuation})
    record(13, "server_to_client", "response.output_text.done", {"type": "response.output_text.done"})
    record(14, "server_to_client", "response.done", {"type": "response.done", "response": {"id": "resp-strict-exec-continuation", "status": "completed"}})
    record(15, "server_to_client", "session.closed", {"type": "session.closed", "session_id": "sess-strict-exec-round-trip", "reason": "fixture_complete"})
    coverage = {
        "version": 2,
        "provider": {"name": "openai", "model": "gpt-realtime"},
        "session": {"id": "sess-strict-exec-round-trip", "fixture_provenance": "synthetic"},
        "records": records,
    }
    coverage_bytes = json.dumps(coverage, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    capture = dict(coverage)
    capture["integrity"] = {
        "algorithm": "sha256",
        "coverage": "session_capture.v2:json(version,provider,session,records,ends_with_disconnect)",
        "digest": sha256_bytes(coverage_bytes),
    }
    path.write_text(json.dumps(capture, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def process_replay_step(runner: Runner, binary: Path) -> dict[str, Any]:
    case_dir = runner.run_dir / "process" / "credential-free-tool-replay"
    case_dir.mkdir(parents=True, exist_ok=True)
    config_dir = case_dir / "config"
    write_session_config(config_dir, read_image=False, exec_enabled=True)
    command = "printf '%s\\n' PROBE_TOOL_MARKER_9182"
    capture_path = case_dir / "strict-exec.session.json"
    strict_exec_capture(capture_path, command)
    stdout_path = case_dir / "cli.stdout.log"
    stderr_path = case_dir / "cli.stderr.log"
    argv = [
        str(binary),
        "--config-dir", str(config_dir),
        "session",
        "--replay", str(capture_path),
        "probe", "PROBE_TOOL_MARKER_9182",
    ]
    started = time.monotonic()
    environment = scrub_external_credentials(os.environ.copy())
    with stdout_path.open("w", encoding="utf-8") as stdout_file, stderr_path.open("w", encoding="utf-8") as stderr_file:
        process = subprocess.Popen(
            argv,
            cwd=REPO_ROOT,
            stdin=subprocess.DEVNULL,
            stdout=stdout_file,
            stderr=stderr_file,
            text=True,
            env=environment,
            start_new_session=os.name != "nt",
        )
        timed_out = False
        try:
            process.wait(timeout=runner.remaining())
        except subprocess.TimeoutExpired:
            timed_out = True
            terminate_group(process)
        exit_code = process.returncode
        group_survivor = process_group_exists(process)
    duration = time.monotonic() - started
    stdout = stdout_path.read_text(encoding="utf-8")
    stderr = stderr_path.read_text(encoding="utf-8")
    observation = {
        "argv": argv,
        "cwd": str(REPO_ROOT),
        "capture": str(capture_path),
        "capture_sha256": sha256_file(capture_path),
        "config": str(config_dir / "config.yaml"),
        "config_sha256": sha256_file(config_dir / "config.yaml"),
        "exit_code": exit_code,
        "timed_out": timed_out,
        "duration_seconds": round(duration, 6),
        "stdout_log": str(stdout_path),
        "stderr_log": str(stderr_path),
        "stdout": clipped(stdout),
        "stderr": clipped(stderr),
        "process_group_survivor": group_survivor,
        "credential_free": True,
    }
    (case_dir / "observations.json").write_text(json.dumps(observation, indent=2) + "\n", encoding="utf-8")
    runner.sequence += 1
    runner.steps.append({
        "label": "credential-free-tool-replay-process",
        "argv": argv,
        "cwd": str(REPO_ROOT),
        "exit_code": exit_code,
        "timed_out": timed_out,
        "duration_seconds": round(duration, 6),
        "observation": str(case_dir / "observations.json"),
    })
    if timed_out or exit_code != 0 or group_survivor:
        raise EvidenceFailure(f"credential-free tool replay process failed: {observation}")
    if "strict replay continuation" not in stdout:
        raise EvidenceFailure(f"credential-free tool replay omitted its continuation: {observation}")
    return observation


def replay_steps(runner: Runner) -> None:
    runner.command(
        "credential-free-tool-replay-regression",
        [
            "rtk",
            "proxy",
            "sh",
            "-c",
            "cd agent-cli && go test -tags=nomicrophone ./test/integration -run '^TestSessionCommand_DefaultRegistryExecRoundTripInStrictOpenAIReplay$' -count=1 -timeout=55s",
        ],
        REPO_ROOT,
    )
    cli_binary, _provider_binary, provenance = process_inputs(runner)
    observation = process_replay_step(runner, cli_binary)
    provenance["credential_free_tool_replay_process"] = observation
    Path(provenance["path"]).write_text(json.dumps(provenance, indent=2) + "\n", encoding="utf-8")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("focused", "consumer", "public-image", "regression"), default="focused")
    args = parser.parse_args()
    runner = Runner(args.mode)
    outcome: dict[str, Any] = {
        "schema": "audio-runtime-c43-retire-cli-image-staging-evidence.v1",
        "mode": args.mode,
        "repo_root": str(REPO_ROOT),
        "evidence_dir": str(EVIDENCE_DIR),
        "steps": runner.steps,
        "status": "FAILED",
    }
    try:
        if args.mode in ("focused", "consumer"):
            consumer_steps(runner)
        if args.mode == "focused":
            focused_runtime_steps(runner)
            public_cli_steps(runner)
            replay_steps(runner)
        elif args.mode == "public-image":
            public_cli_steps(runner)
        elif args.mode == "regression":
            replay_steps(runner)
        outcome["status"] = "PASS"
        outcome["steps"] = runner.steps
        runner.save(outcome)
        print(json.dumps(outcome, indent=2))
        return 0
    except (EvidenceFailure, OSError) as exc:
        outcome["error"] = str(exc)
        outcome["steps"] = runner.steps
        runner.save(outcome)
        print(json.dumps(outcome, indent=2))
        return 1


if __name__ == "__main__":
    sys.exit(main())
