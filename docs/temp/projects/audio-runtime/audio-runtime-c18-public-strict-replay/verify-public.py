#!/usr/bin/env python3
"""Run bounded C18 public replay evidence without replacing the runtime.

The runner launches the built consumer or yui executable against copied
fixtures.  It records literal argv/cwd, complete stdout/stderr, exit status,
timeouts, and artifact hashes.  Each child has its own process group so a
timeout cannot leave a replay worker behind.  The ask case owns only a local
deterministic HTTP fixture for the record half; the replay half runs after the
fixture is stopped.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, Callable


FACTORY_FIXTURE_ROOT = Path(
    "docs/temp/probes/audio-runtime-c11-hermetic-profile-vertical-probe-stage4"
) / "evidence"
AUDIO_TOOL_FIXTURE = FACTORY_FIXTURE_ROOT / "audio-tool-pass"
AUDIO_INTERRUPTION_FIXTURE = FACTORY_FIXTURE_ROOT / "audio-interruption-pass"
HEALTHY_TAIL_SHA256 = (
    "16508b8b42304d49869684c95e47c794b0eb9b54fd9137537dfaa4370097dfbf"
)
RENDERED_PCM_SHA256 = "7d2d8221eb8ec0be3e1da4a3ed518e1e183aa56e4ac0140ca0cf761068555805"
PROMPT = "c18 deterministic ask"
ASK_ANSWER = "c18 local answer"


class ProbeFailure(Exception):
    """An observed result did not match its declared oracle."""


class ProbeBlocked(Exception):
    """A required evidence prerequisite is unavailable."""


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def git_root() -> Path:
    completed = subprocess.run(
        ["git", "rev-parse", "--show-toplevel"],
        cwd=Path(__file__).resolve().parent,
        check=True,
        capture_output=True,
        text=True,
    )
    return Path(completed.stdout.strip()).resolve()


def source_root(path: Path | None, repository: Path, default: Path) -> Path:
    resolved = (default if path is None else path).expanduser()
    if not resolved.is_absolute():
        factory_root = os.environ.get("FACTORY_ROOT")
        base = Path(factory_root).resolve() if factory_root else repository
        resolved = base / resolved
    resolved = resolved.resolve()
    if not (resolved / "config").is_dir() or not (resolved / "run" / "bundle").is_dir():
        raise ProbeBlocked(f"fixture root must contain config/ and run/bundle/: {resolved}")
    return resolved


def require_file(path: Path, label: str) -> Path:
    if not path.is_file():
        raise ProbeBlocked(f"{label} is unavailable: {path}")
    return path.resolve()


def run_process(
    binary: Path,
    argv: list[str],
    cwd: Path,
    timeout: float,
) -> dict[str, Any]:
    started = time.monotonic()
    process = subprocess.Popen(
        [str(binary), *argv],
        cwd=cwd,
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
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
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


def run_checked(
    results: list[dict[str, Any]],
    label: str,
    binary: Path,
    argv: list[str],
    cwd: Path,
    timeout: float,
    check: Callable[[dict[str, Any]], None],
) -> dict[str, Any]:
    result = run_process(binary, argv, cwd, timeout)
    result["label"] = label
    try:
        check(result)
    except ProbeFailure as error:
        result["passed"] = False
        result["failure"] = str(error)
    else:
        result["passed"] = True
    results.append(result)
    if not result["passed"]:
        raise ProbeFailure(f"{label}: {result['failure']}")
    return result


def expect_exit(result: dict[str, Any], code: int) -> None:
    if result["timed_out"] or result["exit_code"] != code:
        raise ProbeFailure(
            f"exit={result['exit_code']} timed_out={result['timed_out']} "
            f"stdout={result['stdout']!r} stderr={result['stderr']!r}"
        )


def expect_strict(result: dict[str, Any], wire_events: int) -> None:
    expect_exit(result, 0)
    combined = result["stdout"] + result["stderr"]
    if "strict replay continuation" not in combined:
        raise ProbeFailure("strict replay continuation marker is missing")
    if f"Replay verified: {wire_events} wire events, " not in combined:
        raise ProbeFailure(f"strict verification did not report {wire_events} wire events")
    if "tool calls" not in combined:
        raise ProbeFailure("strict verification did not report tool calls")


def expect_interruption_strict(result: dict[str, Any]) -> None:
    expect_exit(result, 0)
    if "Replay verified: 15 wire events, 0 tool calls." not in result["stdout"] + result["stderr"]:
        raise ProbeFailure("interruption strict replay did not verify 15 wire events and zero tools")


def expect_provider(result: dict[str, Any]) -> None:
    expect_exit(result, 0)
    combined = result["stdout"] + result["stderr"]
    for marker in (
        "PROBE_TOOL_MARKER_9182",
        "strict replay continuation",
        "[session terminal:",
    ):
        if marker not in combined:
            raise ProbeFailure(f"provider marker is missing: {marker!r}")


def expect_interruption_provider(result: dict[str, Any]) -> None:
    expect_exit(result, 0)
    if "[session terminal:" not in result["stdout"] + result["stderr"]:
        raise ProbeFailure("interruption provider replay did not report a terminal")


def copy_fixture(source: Path, destination: Path) -> Path:
    destination.mkdir(parents=True)
    shutil.copytree(source / "config", destination / "config")
    shutil.copytree(source / "run" / "bundle", destination / "run" / "bundle")
    (destination / "run").mkdir(exist_ok=True)
    (destination / "evidence" / "runs").mkdir(parents=True)
    return destination


def fixture_hashes(root: Path) -> dict[str, str]:
    hashes: dict[str, str] = {}
    for path in sorted(root.rglob("*")):
        if path.is_file():
            hashes[str(path.relative_to(root))] = sha256_file(path)
    return hashes


def mutate_declared_pcm(bundle: Path, mutation: str) -> None:
    pcm = bundle / "audio" / "out-000.pcm"
    if not pcm.is_file():
        raise ProbeBlocked(f"declared PCM fixture is unavailable: {pcm}")
    if mutation == "missing":
        pcm.unlink()
        return
    if mutation == "corrupt":
        data = bytearray(pcm.read_bytes())
        if not data:
            raise ProbeBlocked(f"declared PCM fixture is empty: {pcm}")
        data[0] ^= 1
        pcm.write_bytes(data)
        return
    if mutation == "path-escape":
        manifest_path = bundle / "manifest.json"
        manifest = json.loads(manifest_path.read_text())
        for artifact in manifest.get("artifacts", []):
            if artifact.get("path") == "audio/out-000.pcm":
                artifact["path"] = "../c18-escaped.pcm"
                manifest_path.write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")
                return
        raise ProbeBlocked("manifest has no declared audio/out-000.pcm artifact")
    raise ProbeFailure(f"unknown declared PCM mutation: {mutation}")


def expect_rejected_bundle(result: dict[str, Any], output: Path, marker: Path) -> None:
    combined = result["stdout"] + result["stderr"]
    if result["timed_out"] or result["exit_code"] == 0:
        raise ProbeFailure(
            f"bundle mutation unexpectedly succeeded: exit={result['exit_code']} "
            f"timed_out={result['timed_out']} stdout={result['stdout']!r} stderr={result['stderr']!r}"
        )
    if "Replay verified:" in combined:
        raise ProbeFailure("bundle mutation emitted a success verification")
    if marker.exists():
        raise ProbeFailure(f"bundle mutation executed the recorded tool: {marker}")
    if output.exists() and output.stat().st_size != 0:
        raise ProbeFailure(f"bundle mutation produced nonempty output: {output}")


def bundle_mutation_case(
    binary: Path,
    source: Path,
    results: list[dict[str, Any]],
    timeout: float,
    route: str,
) -> None:
    for mutation in ("missing", "corrupt", "path-escape"):
        with tempfile.TemporaryDirectory(prefix=f"audio-runtime-c18-{route}-{mutation}-") as temporary:
            fixture = copy_fixture(source, Path(temporary) / "fixture")
            bundle = fixture / "run" / "bundle"
            mutate_declared_pcm(bundle, mutation)
            output = fixture / "run" / "mutated-output.pcm"
            marker = fixture / "evidence" / "runs" / "exec-invocations-v4.log"
            if route == "strict":
                argv = [
                    "--workdir",
                    str(fixture),
                    "-C",
                    "config",
                    "session",
                    "replay",
                    "run/bundle",
                ]
            else:
                argv = [
                    "--workdir",
                    str(fixture),
                    "--allow-path",
                    "evidence",
                    "-C",
                    "config",
                    "session",
                    "--replay",
                    "run/bundle",
                    "--audio-out",
                    "run/mutated-output.pcm",
                ]
            run_checked(
                results,
                f"{route}/{mutation}",
                binary,
                argv,
                fixture,
                timeout,
                lambda result, output=output, marker=marker: expect_rejected_bundle(result, output, marker),
            )


def strict_case(binary: Path, source: Path, results: list[dict[str, Any]], timeout: float) -> dict[str, Any]:
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c18-strict-") as temporary:
        fixture = copy_fixture(source, Path(temporary) / "fixture")
        config = fixture / "config"
        bundle = fixture / "run" / "bundle"
        run_checked(
            results,
            "strict/relative-bundle",
            binary,
            ["--workdir", str(fixture), "-C", "config", "session", "replay", "run/bundle"],
            fixture,
            timeout,
            lambda result: expect_strict(result, 18),
        )
        run_checked(
            results,
            "strict/absolute-bundle",
            binary,
            ["--workdir", str(fixture), "-C", str(config), "session", "replay", str(bundle)],
            fixture,
            timeout,
            lambda result: expect_strict(result, 18),
        )
        manifest = json.loads((bundle / "manifest.json").read_text())
        if manifest.get("terminal", {}).get("reason") != "fixture_complete":
            raise ProbeFailure("strict fixture terminal reason is not fixture_complete")
    bundle_mutation_case(binary, source, results, timeout, "strict")
    return {"fixture_root": str(source), "fixture_hashes": fixture_hashes(source)}


def provider_case(
    binary: Path,
    source: Path,
    results: list[dict[str, Any]],
    timeout: float,
    interruption: bool = False,
) -> dict[str, Any]:
    if not interruption:
        with tempfile.TemporaryDirectory(prefix="audio-runtime-c18-provider-raw-") as temporary:
            fixture = copy_fixture(source, Path(temporary) / "fixture")
            output = fixture / "run" / "provider-raw-output.pcm"
            run_checked(
                results,
                "provider/raw-capture-legacy",
                binary,
                [
                    "--workdir",
                    str(fixture),
                    "--allow-path",
                    "evidence",
                    "-C",
                    "config",
                    "session",
                    "--replay",
                    "run/bundle/provider.json",
                    "--audio-out",
                    "run/provider-raw-output.pcm",
                ],
                fixture,
                timeout,
                expect_provider,
            )
            marker = fixture / "evidence" / "runs" / "exec-invocations-v4.log"
            if not marker.is_file() or "PROBE_TOOL_MARKER_9182" not in marker.read_text():
                raise ProbeFailure("raw provider replay did not retain the expected exec side effect")
            if output.stat().st_size != 3200 or sha256_file(output) != RENDERED_PCM_SHA256:
                raise ProbeFailure("raw provider replay PCM is not the exact 3200-byte C18 oracle")

    for label, bundle_argument, config_argument, allow_path in (
        (
            "interruption/provider-relative-bundle" if interruption else "provider/relative-bundle",
            "run/bundle",
            "config",
            "evidence",
        ),
        (
            "interruption/provider-absolute-bundle" if interruption else "provider/absolute-bundle",
            None,
            None,
            None,
        ),
    ):
        with tempfile.TemporaryDirectory(prefix="audio-runtime-c18-provider-") as temporary:
            fixture = copy_fixture(source, Path(temporary) / "fixture")
            config = fixture / "config"
            bundle = fixture / "run" / "bundle"
            output = fixture / "run" / "provider-output.pcm"
            absolute_bundle = bundle_argument or str(bundle)
            absolute_config = config_argument or str(config)
            absolute_allow_path = allow_path or str(fixture / "evidence")
            run_checked(
                results,
                label,
                binary,
                [
                    "--workdir",
                    str(fixture),
                    "--allow-path",
                    absolute_allow_path,
                    "-C",
                    absolute_config,
                    "session",
                    "--replay",
                    absolute_bundle,
                    "--audio-out",
                    "run/provider-output.pcm",
                ],
                fixture,
                timeout,
                expect_interruption_provider if interruption else expect_provider,
            )
            if not interruption:
                marker = fixture / "evidence" / "runs" / "exec-invocations-v4.log"
                if not marker.is_file() or "PROBE_TOOL_MARKER_9182" not in marker.read_text():
                    raise ProbeFailure("provider did not retain the expected exec side effect")
                if output.stat().st_size != 3200 or sha256_file(output) != RENDERED_PCM_SHA256:
                    raise ProbeFailure("provider PCM is not the exact 3200-byte C18 oracle")
            else:
                data = output.read_bytes()
                if len(data) != 3360:
                    raise ProbeFailure(f"interruption rendered PCM length={len(data)}, want 3360")
                if sha256_file(output) != "302e7421a29a4868a0a1a2f1ca2e8432c9015a6475412ec63fe2b15414f469ff":
                    raise ProbeFailure("interruption rendered PCM does not match the preserved bundle")
                if hashlib.sha256(data[-2400:]).hexdigest() != HEALTHY_TAIL_SHA256:
                    raise ProbeFailure("interruption healthy tail hash does not match the oracle")
                records = json.loads((bundle / "provider.json").read_text())["records"]
                responses = {
                    record["payload"]["response"]["id"]: record["payload"]["response"].get("status")
                    for record in records
                    if record.get("type") == "response.done"
                }
                if responses != {"resp-c07-interrupted": "cancelled", "resp-c07-healthy": "completed"}:
                    raise ProbeFailure(f"interruption response statuses={responses!r}")

    if not interruption:
        with tempfile.TemporaryDirectory(prefix="audio-runtime-c18-wrong-cwd-") as temporary:
            fixture = copy_fixture(source, Path(temporary) / "fixture")
            config = fixture / "config"
            bundle = fixture / "run" / "bundle"
            wrong_root = Path(tempfile.mkdtemp(prefix="audio-runtime-c18-wrong-root-"))
            try:
                wrong_output = wrong_root / "provider-output.pcm"
                result = run_process(
                    binary,
                    [
                        "--workdir",
                        str(wrong_root),
                        "--allow-path",
                        str(wrong_root),
                        "-C",
                        str(config),
                        "session",
                        "--replay",
                        str(bundle),
                        "--audio-out",
                        str(wrong_output),
                    ],
                    wrong_root,
                    timeout,
                )
                result["label"] = "provider/wrong-cwd-negative"
                combined = result["stdout"] + result["stderr"]
                result["passed"] = (
                    not result["timed_out"]
                    and result["exit_code"] != 0
                    and "Replay verified:" not in combined
                    and "evidence/runs" in combined
                    and (not wrong_output.exists() or wrong_output.stat().st_size == 0)
                    and not (wrong_root / "evidence" / "runs" / "exec-invocations-v4.log").exists()
                )
                if not result["passed"]:
                    result["failure"] = f"wrong-cwd result was not a causal failure: {combined!r}"
                    results.append(result)
                    raise ProbeFailure(f"{result['label']}: {result['failure']}")
                results.append(result)
            finally:
                shutil.rmtree(wrong_root, ignore_errors=True)
        bundle_mutation_case(binary, source, results, timeout, "provider")
    return {"fixture_root": str(source), "fixture_hashes": fixture_hashes(source)}


class _SSEHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_POST(self) -> None:  # noqa: N802 - required HTTPServer hook
        length = int(self.headers.get("Content-Length", "0"))
        self.rfile.read(length)
        body = (
            'data: {"choices":[{"delta":{"role":"assistant","content":"'
            + ASK_ANSWER
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


def ask_case(binary: Path, repository: Path, results: list[dict[str, Any]], timeout: float) -> dict[str, Any]:
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c18-ask-") as temporary:
        root = Path(temporary)
        config = root / "config"
        config.mkdir()
        capture = root / "capture.json"
        server = ThreadingHTTPServer(("127.0.0.1", 0), _SSEHandler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        base_url = f"http://127.0.0.1:{server.server_port}/v1"
        record = run_checked(
            results,
            "ask/record-local-sse",
            binary,
            [
                "-C",
                str(config),
                "ask",
                "--stream",
                "--system-prompt",
                "none",
                "--no-system-information",
                "--provider",
                "local",
                "--model",
                "c18-capture-model",
                "--base-url",
                base_url,
                "--record",
                str(capture),
                PROMPT,
            ],
            root,
            timeout,
            lambda result: (
                expect_exit(result, 0),
                result["stdout"] == ASK_ANSWER
                or (_ for _ in ()).throw(ProbeFailure("local SSE record output is not the expected answer")),
            ),
        )
        server.shutdown()
        thread.join(timeout=2)
        server.server_close()
        if thread.is_alive():
            raise ProbeFailure("local SSE helper did not stop before replay")
        if not capture.is_file():
            raise ProbeFailure("ask record did not write a capture")
        captures = json.loads(capture.read_text())
        if len(captures) != 1 or ASK_ANSWER not in captures[0]["response"]["body"]:
            raise ProbeFailure("ask capture does not retain the expected response body")
        replay = run_checked(
            results,
            "ask/replay-after-helper-stop",
            binary,
            [
                "-C",
                str(config),
                "ask",
                "--stream",
                "--system-prompt",
                "none",
                "--no-system-information",
                "--provider",
                "local",
                "--model",
                "c18-capture-model",
                "--base-url",
                base_url,
                "--replay",
                str(capture),
                PROMPT,
            ],
            root,
            timeout,
            lambda result: (
                expect_exit(result, 0),
                result["stdout"] == record["stdout"]
                or (_ for _ in ()).throw(ProbeFailure("ask replay output differs from record")),
            ),
        )
        return {
            "capture_sha256": sha256_file(capture),
            "record_stdout": record["stdout"],
            "replay_stdout": replay["stdout"],
        }


def external_case(binary: Path, repository: Path, results: list[dict[str, Any]], timeout: float) -> dict[str, Any]:
    fixture = repository / "tests" / "embedding" / "testdata" / "replay"
    require_file(fixture / "timeline.jsonl", "external replay fixture")
    marker = fixture / "exec-marker"
    if marker.exists():
        raise ProbeFailure(f"external fixture already has executable-tool marker: {marker}")
    for label, argument in (
        ("external/relative-fixture", os.fspath(fixture.relative_to(repository))),
        ("external/absolute-fixture", str(fixture.resolve())),
    ):
        result = run_checked(
            results,
            label,
            binary,
            [argument],
            repository,
            timeout,
            lambda value: (
                expect_exit(value, 0),
                value["stdout"] == "strict embedding continuation"
                or (_ for _ in ()).throw(ProbeFailure("external consumer output differs")),
                value["stderr"]
                == "Replay verified: 16 wire events, 1 tool calls. Device execution: false.\n"
                or (_ for _ in ()).throw(ProbeFailure("external consumer verification differs")),
            ),
        )
        if marker.exists():
            raise ProbeFailure(f"{label} created executable-tool marker: {marker}")
    return {"fixture_root": str(fixture), "fixture_hashes": fixture_hashes(fixture)}


def write_report(path: Path, report: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--case", choices=("external", "strict", "provider", "interruption", "ask"), required=True)
    parser.add_argument("--consumer", type=Path, help="independent GOWORK=off consumer binary for --case external")
    parser.add_argument("--yui", type=Path, help="candidate yui binary for CLI cases")
    parser.add_argument("--fixture", type=Path, help="copied-ready audio/tool fixture root")
    parser.add_argument("--interruption-fixture", type=Path, help="copied-ready interruption fixture root")
    parser.add_argument("--evidence", type=Path, help="JSON report path")
    parser.add_argument("--timeout", type=float, default=50.0)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    if args.timeout <= 0 or args.timeout > 50:
        raise ProbeFailure("--timeout must be within 0 < timeout <= 50 seconds")
    repository = git_root()
    script_dir = Path(__file__).resolve().parent
    report_path = (args.evidence or script_dir / f"verify-public-{args.case}.json").expanduser().resolve()
    results: list[dict[str, Any]] = []
    report: dict[str, Any] = {
        "case": args.case,
        "source_revision": subprocess.run(
            ["git", "rev-parse", "HEAD"], cwd=repository, check=True, capture_output=True, text=True
        ).stdout.strip(),
        "timeout_seconds": args.timeout,
        "results": results,
        "status": "running",
    }
    try:
        if args.case == "external":
            consumer = require_file((args.consumer or Path("docs/temp/projects/audio-runtime/audio-runtime-c18-public-strict-replay/strict-replay")).resolve(), "external consumer")
            report["binary"] = str(consumer)
            report["binary_sha256"] = sha256_file(consumer)
            report["evidence"] = external_case(consumer, repository, results, args.timeout)
        elif args.case == "ask":
            yui = require_file((args.yui or Path("agent-cli/bin/yui")).resolve(), "yui binary")
            report["binary"] = str(yui)
            report["binary_sha256"] = sha256_file(yui)
            report["evidence"] = ask_case(yui, repository, results, args.timeout)
        else:
            yui = require_file((args.yui or Path("agent-cli/bin/yui")).resolve(), "yui binary")
            report["binary"] = str(yui)
            report["binary_sha256"] = sha256_file(yui)
            source = source_root(args.fixture, repository, AUDIO_TOOL_FIXTURE)
            if args.case == "strict":
                report["evidence"] = strict_case(yui, source, results, args.timeout)
            elif args.case == "provider":
                report["evidence"] = provider_case(yui, source, results, args.timeout)
            else:
                interruption = source_root(args.interruption_fixture, repository, AUDIO_INTERRUPTION_FIXTURE)
                report["evidence"] = provider_case(yui, interruption, results, args.timeout, interruption=True)
                with tempfile.TemporaryDirectory(prefix="audio-runtime-c18-interruption-strict-") as temporary:
                    fixture = copy_fixture(interruption, Path(temporary) / "fixture")
                    config = fixture / "config"
                    bundle = fixture / "run" / "bundle"
                    run_checked(
                        results,
                        "interruption/strict-relative-bundle",
                        yui,
                        ["--workdir", str(fixture), "-C", "config", "session", "replay", "run/bundle"],
                        fixture,
                        args.timeout,
                        expect_interruption_strict,
                    )
                    run_checked(
                        results,
                        "interruption/strict-absolute-bundle",
                        yui,
                        ["--workdir", str(fixture), "-C", str(config), "session", "replay", str(bundle)],
                        fixture,
                        args.timeout,
                        expect_interruption_strict,
                    )
    except ProbeBlocked as error:
        report["status"] = "blocked"
        report["failure"] = str(error)
        write_report(report_path, report)
        print(f"BLOCKED: {error}", file=sys.stderr)
        return 2
    except ProbeFailure as error:
        report["status"] = "failed"
        report["failure"] = str(error)
        write_report(report_path, report)
        print(f"FAIL: {error}", file=sys.stderr)
        return 1
    report["status"] = "passed"
    write_report(report_path, report)
    print(json.dumps({"case": args.case, "passed": True, "results": len(results)}, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
