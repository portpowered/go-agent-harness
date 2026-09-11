#!/usr/bin/env python3
"""Run bounded C54 room-document admission and shipped replay evidence."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tarfile
import tempfile
import threading
import time
from typing import Any


HERE = Path(__file__).resolve().parent
ROOT = Path(subprocess.run(["git", "rev-parse", "--show-toplevel"], cwd=HERE, check=True, capture_output=True, text=True).stdout.strip())
OWNED_REL = HERE.relative_to(ROOT)
CONSUMER_DIR = HERE / "consumer"
CONSUMER_SOURCE = CONSUMER_DIR / "main.go"
CONSUMER_BINARY = HERE / "artifacts" / "c54-consumer"
YUI_BINARY = HERE / "artifacts" / "yui"
ARTIFACTS = HERE / "artifacts"
RUNS = HERE / "runs"
PROVENANCE = HERE / "provenance.json"
SUMMARY = HERE / "verification-summary.json"
CLI_FIXTURE = HERE / "fixtures" / "cli-invalid-nested-browser.yaml"
C21_FIXTURE = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures/c16-audio-tool.session.json"
BASE_REVISION = "2456a5d1594e73faf85e1132d6050735bc3e4710"
INTEGRATION_REVISION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
BASELINE_REVISION = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
EXPECTED_BRANCH = "codex/audio-runtime-c54-room-document-admission"
EXPECTED_PCM_BYTES = 4800
EXPECTED_PCM_SHA = "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502"
EXPECTED_FIXTURE_SHA = "38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169"
MAX_CHILD_SECONDS = 60.0
MAX_AGGREGATE_SECONDS = 600.0
MAX_CHILD_OUTPUT_BYTES = 64 * 1024
MAX_RETAINED_DISK_BYTES = 8 * 1024 * 1024


class EvidenceError(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise EvidenceError(message)


def sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def sha256_tree(path: Path) -> str:
    digest = hashlib.sha256()
    for child in sorted(path.rglob("*")):
        if child.is_file() and not child.is_symlink():
            digest.update(str(child.relative_to(path)).encode())
            digest.update(sha256_file(child).encode())
    return digest.hexdigest()


def write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def load_json(path: Path) -> Any:
    require(path.is_file() and not path.is_symlink(), f"missing JSON artifact: {path}")
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as error:
        raise EvidenceError(f"invalid JSON artifact {path}: {error}") from error


def git_value(*args: str, cwd: Path = ROOT) -> str:
    result = subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True, text=True, timeout=20)
    return result.stdout.strip()


def is_ancestor(ancestor: str, descendant: str) -> bool:
    return subprocess.run(["git", "merge-base", "--is-ancestor", ancestor, descendant], cwd=ROOT, check=False, timeout=20).returncode == 0


def relative(path: Path) -> str:
    try:
        return str(path.resolve().relative_to(HERE.resolve()))
    except ValueError:
        try:
            return str(path.resolve().relative_to(ROOT.resolve()))
        except ValueError:
            return "<temporary-base-archive>"


def go_mod_cache() -> str:
    return subprocess.run(["go", "env", "GOMODCACHE"], check=True, capture_output=True, text=True, timeout=20).stdout.strip()


def sanitized_environment(run_root: Path, gowork: str, extras: dict[str, str] | None = None) -> dict[str, str]:
    home = run_root / "home"
    for path in (home, run_root / "tmp", run_root / "gocache"):
        path.mkdir(parents=True, exist_ok=True)
    environment = {
        "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
        "HOME": str(home),
        "TMPDIR": str(run_root / "tmp"),
        "GOCACHE": str(run_root / "gocache"),
        "GOMODCACHE": go_mod_cache(),
        "GOWORK": gowork,
        "LANG": "C",
        "LC_ALL": "C",
    }
    if extras:
        environment.update(extras)
    return environment


def group_alive(pid: int) -> bool:
    try:
        os.killpg(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    return True


def run_child(
    argv: list[str],
    label: str,
    cwd: Path,
    run_root: Path,
    source_revision: str,
    timeout: float,
    gowork: str,
    extras: dict[str, str] | None = None,
) -> dict[str, Any]:
    require(0 < timeout <= MAX_CHILD_SECONDS, f"{label} timeout exceeds the 60 second child bound")
    run_root.mkdir(parents=True, exist_ok=True)
    stdout_path = run_root / "stdout.log"
    stderr_path = run_root / "stderr.log"
    observed = {"stdout": 0, "stderr": 0}
    overflow = {"stdout": False, "stderr": False}

    def drain(stream: Any, path: Path, name: str) -> None:
        try:
            with path.open("wb") as output:
                while True:
                    chunk = stream.read(8192)
                    if not chunk:
                        return
                    previous = observed[name]
                    observed[name] += len(chunk)
                    keep = max(0, min(len(chunk), MAX_CHILD_OUTPUT_BYTES - previous))
                    if keep:
                        output.write(chunk[:keep])
                    if observed[name] > MAX_CHILD_OUTPUT_BYTES:
                        overflow[name] = True
        except (OSError, ValueError):
            overflow[name] = True

    started = time.monotonic()
    process: subprocess.Popen[bytes] | None = None
    threads: list[threading.Thread] = []
    timed_out = False
    term_sent = False
    kill_sent = False
    parent_reaped = False
    setup_error = ""
    before_alive = False
    returncode: int | None = None
    try:
        environment = sanitized_environment(run_root, gowork, {"C54_SOURCE_REVISION": source_revision, **(extras or {})})
        process = subprocess.Popen(
            argv,
            cwd=cwd,
            env=environment,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=True,
        )
        before_alive = group_alive(process.pid)
        assert process.stdout is not None and process.stderr is not None
        threads = [
            threading.Thread(target=drain, args=(process.stdout, stdout_path, "stdout"), daemon=True),
            threading.Thread(target=drain, args=(process.stderr, stderr_path, "stderr"), daemon=True),
        ]
        for thread in threads:
            thread.start()
        try:
            returncode = process.wait(timeout=timeout)
            parent_reaped = True
        except subprocess.TimeoutExpired:
            timed_out = True
            try:
                os.killpg(process.pid, signal.SIGTERM)
                term_sent = True
            except ProcessLookupError:
                pass
            try:
                returncode = process.wait(timeout=2)
                parent_reaped = True
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                    kill_sent = True
                except ProcessLookupError:
                    pass
                returncode = process.wait(timeout=2)
                parent_reaped = True
        for thread in threads:
            thread.join(timeout=2)
        if group_alive(process.pid):
            try:
                os.killpg(process.pid, signal.SIGKILL)
                kill_sent = True
            except ProcessLookupError:
                pass
            try:
                process.wait(timeout=2)
                parent_reaped = True
            except subprocess.TimeoutExpired:
                pass
            for thread in threads:
                thread.join(timeout=2)
    except OSError as error:
        setup_error = str(error)
    elapsed_ms = int((time.monotonic() - started) * 1000)
    after_alive = group_alive(process.pid) if process is not None else False
    disk_bytes = sum(path.stat().st_size for path in (stdout_path, stderr_path) if path.is_file())
    return {
        "label": label,
        "argv": argv,
        "cwd": relative(cwd),
        "returncode": returncode,
        "timed_out": timed_out,
        "elapsed_ms": elapsed_ms,
        "stdout": relative(stdout_path),
        "stderr": relative(stderr_path),
        "stdout_bytes": observed["stdout"],
        "stderr_bytes": observed["stderr"],
        "output_bounded": not any(overflow.values()),
        "disk_bytes": disk_bytes,
        "disk_bounded": disk_bytes <= MAX_RETAINED_DISK_BYTES,
        "cleanup": {
            "parent_reaped": parent_reaped,
            "group_alive_before": before_alive,
            "group_alive_after": after_alive,
            "reader_threads_joined": not any(thread.is_alive() for thread in threads),
            "term_sent": term_sent,
            "kill_sent": kill_sent,
            "setup_error": setup_error,
        },
    }


def execution_ok(value: dict[str, Any], label: str) -> None:
    cleanup = value.get("cleanup", {})
    require(value.get("returncode") == 0, f"{label} returned {value.get('returncode')}")
    require(not value.get("timed_out") and value.get("output_bounded") and value.get("disk_bounded"), f"{label} exceeded a process/output bound")
    require(cleanup.get("parent_reaped") and cleanup.get("reader_threads_joined") and cleanup.get("group_alive_after") is False, f"{label} left a process or reader survivor")
    require(int(value.get("elapsed_ms", 10**9)) <= MAX_CHILD_SECONDS * 1000, f"{label} exceeded the 60 second child budget")


def read_execution_output(value: dict[str, Any]) -> tuple[str, str]:
    stdout = (HERE / value["stdout"]).read_text(encoding="utf-8", errors="replace")
    stderr = (HERE / value["stderr"]).read_text(encoding="utf-8", errors="replace")
    return stdout, stderr


def check_no_secret(value: str, label: str) -> None:
    for marker in ("c54-secret-value-must-not-escape", "cdp-secret", "ws-secret", "browser-secret", "c54-no-network-key", "openai_api_key", "authorization:"):
        require(marker.lower() not in value.lower(), f"{label} exposed forbidden credential/endpoint marker {marker!r}")


def build_consumer(source_revision: str) -> dict[str, Any]:
    result = run_child(
        ["go", "build", "-mod=readonly", "-trimpath", "-o", str(CONSUMER_BINARY), "."],
        "build-external-consumer",
        CONSUMER_DIR,
        ARTIFACTS / "build-consumer",
        source_revision,
        MAX_CHILD_SECONDS,
        "off",
    )
    execution_ok(result, "external consumer build")
    require(CONSUMER_BINARY.is_file(), "external consumer binary was not produced")
    return {"execution": result, "binary": {"path": relative(CONSUMER_BINARY), "bytes": CONSUMER_BINARY.stat().st_size, "sha256": sha256_file(CONSUMER_BINARY)}}


def run_consumer_tests(source_revision: str) -> dict[str, Any]:
    results = {}
    for name, args in (
        ("normal", ["go", "test", "-count=1", "-timeout=50s", "./..."]),
        ("race", ["go", "test", "-race", "-count=1", "-timeout=50s", "./..."]),
    ):
        result = run_child(args, f"external-consumer-{name}", CONSUMER_DIR, ARTIFACTS / f"consumer-test-{name}", source_revision, MAX_CHILD_SECONDS, "off")
        execution_ok(result, f"external consumer {name} test")
        results[name] = result
    return results


def run_consumer_positive(source_revision: str, candidate_revision: str) -> dict[str, Any]:
    result = run_child(
        [str(CONSUMER_BINARY)],
        "public-external-consumer",
        CONSUMER_DIR,
        ARTIFACTS / "consumer-positive",
        source_revision,
        MAX_CHILD_SECONDS,
        "off",
        {"C54_CANDIDATE_REVISION": candidate_revision},
    )
    execution_ok(result, "public external consumer")
    stdout, stderr = read_execution_output(result)
    check_no_secret(stdout + stderr, "external consumer")
    try:
        report = json.loads(stdout)
    except json.JSONDecodeError as error:
        raise EvidenceError(f"external consumer did not emit one JSON report: {error}") from error
    require(report.get("schema") == "audio-runtime.c54.public-admission/v1", "external consumer schema changed")
    require(report.get("constructed_via") == "rooms/wire.NewManifestProviderFromRegistry", "external consumer bypassed the public manifest provider")
    require(report.get("candidate_revision") == candidate_revision and report.get("source_revision") == source_revision, "external consumer is not bound to candidate/source provenance")
    require(report.get("json_yaml_equal") and report.get("file_admission") and report.get("admit_aliases") and report.get("registry_snapshot_isolated"), "external consumer admission result is incomplete")
    normalized = report.get("normalized", {})
    require(normalized.get("max_turns") == 3 and normalized.get("max_duration") == "30s" and normalized.get("recording_directory") == "/tmp/c54-evidence", "normalized room bounds/recording changed")
    require(normalized.get("participant_kinds") == ["agent", "agent"] and normalized.get("participant_ids") == ["alpha", "beta"], "participant normalization changed")
    require(normalized.get("provider") == "openai" and normalized.get("model") == "gpt-realtime" and normalized.get("tools") == ["sleep"], "provider/model/tool normalization changed")
    require(normalized.get("browser_cdp_url") == "http://127.0.0.1:9222/json/version" and normalized.get("browser_ws_path") == "ws://127.0.0.1:9222/%3Credacted%3E", "browser redacted serialization changed")
    require(normalized.get("browser_timeout") == "2m30s" and normalized.get("browser_max_input") == 1024 and normalized.get("browser_max_result") == 2048 and normalized.get("browser_default_match"), "browser normalized/default values changed")
    errors_value = report.get("typed_errors", {})
    require(errors_value.get("missing_credential_cause") and errors_value.get("missing_credential_field") == "participants[1].api_key_env" and errors_value.get("missing_credential_value") == "", "credential typed error/redaction changed")
    require(errors_value.get("unknown_nested_cause") and errors_value.get("unknown_nested_field") == "document" and errors_value.get("unsafe_endpoint_cause") and errors_value.get("unsafe_endpoint_field") == "participants[0].browserTools.connection.cdp_url", "browser typed error attribution changed")
    require(errors_value.get("no_opener_cause") and errors_value.get("multiple_document_cause"), "negative admission matrix is incomplete")
    redaction = report.get("redaction", {})
    require(redaction.get("serialized_secret_free") and redaction.get("error_secret_free") and redaction.get("endpoint_query_removed") and redaction.get("websocket_path_redacted"), "redaction oracle changed")
    require(report.get("lifecycle") == {"validated": True, "closed": True}, "provider lifecycle evidence changed")
    return {"execution": result, "report": report}


def run_consumer_negative_controls(source_revision: str, candidate_revision: str) -> dict[str, Any]:
    modes = {
        "normalized": "C54_WRONG_NORMALIZED_ORACLE",
        "error": "C54_WRONG_ERROR_ORACLE",
        "registry": "C54_WRONG_REGISTRY_ORACLE",
        "redaction": "C54_WRONG_REDACTION_ORACLE",
    }
    controls = []
    for name, variable in modes.items():
        result = run_child(
            [str(CONSUMER_BINARY)],
            f"consumer-wrong-{name}-oracle",
            CONSUMER_DIR,
            ARTIFACTS / f"consumer-wrong-{name}",
            source_revision,
            MAX_CHILD_SECONDS,
            "off",
            {"C54_CANDIDATE_REVISION": candidate_revision, variable: "1"},
        )
        require(result.get("returncode") != 0 and not result.get("timed_out"), f"wrong {name} oracle unexpectedly passed or timed out")
        require(result.get("cleanup", {}).get("parent_reaped") and result.get("cleanup", {}).get("group_alive_after") is False, f"wrong {name} oracle cleanup failed")
        stdout, stderr = read_execution_output(result)
        check_no_secret(stdout + stderr, f"wrong {name} oracle")
        require("wrong oracle" in (stdout + stderr).lower(), f"wrong {name} oracle lost its causal assertion")
        controls.append({"name": name, "environment": variable, "execution": result})
    return {"schema": "audio-runtime.c54.negative-controls/v1", "passed": True, "controls": controls}


def run_baseline_failure(source_revision: str, candidate_revision: str) -> dict[str, Any]:
    with tempfile.TemporaryDirectory(prefix="c54-base-") as temporary:
        temporary_root = Path(temporary)
        archive = temporary_root / "base.tar"
        with archive.open("wb") as stream:
            subprocess.run(["git", "archive", "--format=tar", BASE_REVISION], cwd=ROOT, check=True, stdout=stream, timeout=30)
        archived_root = temporary_root / "repo"
        archived_root.mkdir()
        with tarfile.open(archive) as tar:
            tar.extractall(archived_root)
        archived_task = archived_root / OWNED_REL
        (archived_task / "consumer").mkdir(parents=True, exist_ok=True)
        for path in ("go.mod", "go.sum", "main.go", "main_test.go"):
            source = CONSUMER_DIR / path
            if source.is_file():
                destination = archived_task / "consumer" / path
                destination.parent.mkdir(parents=True, exist_ok=True)
                destination.write_bytes(source.read_bytes())
        baseline_binary = temporary_root / "baseline-consumer"
        result = run_child(
            ["go", "build", "-mod=readonly", "-trimpath", "-o", str(baseline_binary), "."],
            "baseline-without-c54-provider",
            archived_task / "consumer",
            ARTIFACTS / "baseline-failure",
            BASE_REVISION,
            MAX_CHILD_SECONDS,
            "off",
            {"C54_CANDIDATE_REVISION": candidate_revision},
        )
        require(result.get("returncode") != 0 and not result.get("timed_out"), "pinned baseline unexpectedly built the C54 public provider")
        require(result.get("cleanup", {}).get("parent_reaped") and result.get("cleanup", {}).get("group_alive_after") is False, "baseline failure did not clean up")
        _, stderr = read_execution_output(result)
        require("ManifestProvider" in stderr or "NewManifestProvider" in stderr, "baseline failure did not identify the missing C54 provider API")
        check_no_secret(stderr, "baseline failure")
        return {"expected_outcome": "baseline-api-missing", "source_revision": source_revision, "candidate_revision": candidate_revision, "execution": result}


def build_yui(source_revision: str) -> dict[str, Any]:
    result = run_child(
        ["go", "build", "-tags=nomicrophone", "-trimpath", "-o", str(YUI_BINARY), "./agent-cli/cmd/yui"],
        "build-shipped-yui",
        ROOT,
        ARTIFACTS / "build-yui",
        source_revision,
        MAX_CHILD_SECONDS,
        str(ROOT / "go.work"),
    )
    execution_ok(result, "shipped YUI build")
    require(YUI_BINARY.is_file(), "shipped YUI binary was not produced")
    return {"execution": result, "binary": {"path": relative(YUI_BINARY), "bytes": YUI_BINARY.stat().st_size, "sha256": sha256_file(YUI_BINARY)}}


def run_cli_admission(source_revision: str, candidate_revision: str) -> dict[str, Any]:
    case = ARTIFACTS / f"cli-admission-{time.strftime('%Y%m%dT%H%M%S')}-{os.getpid()}"
    config_dir = case / "config"
    output_dir = case / "out"
    result = run_child(
        [
            str(YUI_BINARY),
            "--config-dir", str(config_dir),
            "--log-to-stdout",
            "room", "run",
            "--config", str(CLI_FIXTURE),
            "--out", str(output_dir),
            "--workdir", str(HERE),
            "--allow-path", str(HERE),
        ],
        "shipped-yui-room-admission",
        HERE,
        case / "process",
        source_revision,
        MAX_CHILD_SECONDS,
        str(ROOT / "go.work"),
        {"C54_CANDIDATE_REVISION": candidate_revision},
    )
    require(result.get("returncode") != 0 and not result.get("timed_out"), "shipped YUI invalid room admission unexpectedly passed or timed out")
    require(result.get("cleanup", {}).get("parent_reaped") and result.get("cleanup", {}).get("group_alive_after") is False, "shipped YUI admission left a process survivor")
    stdout, stderr = read_execution_output(result)
    output = stdout + stderr
    check_no_secret(output, "shipped YUI admission")
    expected = "field unknown not found in a participant's browserTools"
    require(expected in output, "shipped YUI did not expose the runtime-attributed browser admission error")
    require("manifestBrowser" not in output, "shipped YUI exposed an internal runtime decode type")
    require(not output_dir.exists(), "shipped YUI created room output before admission")
    cli_source = (ROOT / "agent-cli/internal/room/manifest.go").read_text(encoding="utf-8")
    cli_browser = (ROOT / "agent-cli/internal/room/browser_tools.go").read_text(encoding="utf-8")
    runtime_source = (ROOT / "go-agent-runtime/services/rooms/internal/manifest/decoder.go").read_text(encoding="utf-8")
    require("runtimeWire.NewManifestProvider" in cli_source, "CLI source does not delegate to the runtime manifest provider")
    require("yamlv3.NewDecoder" not in cli_source and "yamlv3.NewDecoder" not in cli_browser and "manifestDocument" not in cli_source, "CLI retains a parser/raw manifest declaration")
    require("yamlv3.NewDecoder" in runtime_source and "type manifestDocument" in runtime_source, "runtime manifest implementation is not the raw admission owner")
    return {
        "execution": result,
        "fixture": {"path": relative(CLI_FIXTURE), "bytes": CLI_FIXTURE.stat().st_size, "sha256": sha256_file(CLI_FIXTURE)},
        "expected_error": expected,
        "delegation": {
            "source_has_runtime_provider": True,
            "cli_parser_absent": True,
            "runtime_raw_decoder_present": True,
            "observable_typed_error_surface": "participant-qualified strict browser field error",
        },
    }


def load_jsonl(path: Path) -> list[dict[str, Any]]:
    values = []
    for line_number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        try:
            value = json.loads(line)
        except json.JSONDecodeError as error:
            raise EvidenceError(f"invalid JSONL {path}:{line_number}: {error}") from error
        require(isinstance(value, dict), f"non-object JSONL record {path}:{line_number}")
        values.append(value)
    return values


def run_shipped_replay(source_revision: str, candidate_revision: str) -> dict[str, Any]:
    require(C21_FIXTURE.is_file(), f"missing read-only shipped replay fixture: {C21_FIXTURE}")
    require(sha256_file(C21_FIXTURE) == EXPECTED_FIXTURE_SHA, "read-only C21 shipped replay fixture changed")
    case = ARTIFACTS / f"shipped-yui-replay-{time.strftime('%Y%m%dT%H%M%S')}-{os.getpid()}"
    (case / "evidence" / "runs").mkdir(parents=True, exist_ok=True)
    record_dir = case / "tool-record"
    audio_out = case / "audio.wav"
    result = run_child(
        [
            str(YUI_BINARY),
            "--config-dir", str(case / "config"),
            "--log-to-stdout",
            "session",
            "--replay", str(C21_FIXTURE),
            "--audio-out", str(audio_out),
            "--record-dir", str(record_dir),
            "--trace-audio",
            "--workdir", str(case),
            "--allow-path", str(case),
        ],
        "shipped-yui-audio-tool-replay",
        case,
        case / "process",
        source_revision,
        MAX_CHILD_SECONDS,
        str(ROOT / "go.work"),
        {"C54_CANDIDATE_REVISION": candidate_revision},
    )
    execution_ok(result, "shipped credential-free audio/tool replay")
    stdout, stderr = read_execution_output(result)
    check_no_secret(stdout + stderr, "shipped replay")
    require("PROBE_TOOL_MARKER_9182" in stdout and "strict replay continuation" in stdout, "shipped replay lost the tool marker or continuation")
    require("replay mismatch" not in (stdout + stderr).lower(), "shipped replay reported a mismatch")
    manifest_path = record_dir / "manifest.json"
    pcm_path = record_dir / "audio" / "out-000.pcm"
    provider_path = record_dir / "provider.json"
    session_log_path = record_dir / "session-log.jsonl"
    require(manifest_path.is_file() and pcm_path.is_file() and provider_path.is_file() and session_log_path.is_file(), "shipped replay omitted a required record")
    manifest = load_json(manifest_path)
    require(manifest.get("terminal") == {"reason": "fixture_complete", "classification": "provider_close", "terminal_reason": "provider_close", "terminal_provenance": "provider", "output_state": "not_applicable"}, "shipped replay terminal changed")
    artifact_hashes = {item.get("path"): item.get("sha256") for item in manifest.get("artifacts", [])}
    expected_paths = {"client.transcript.jsonl", "agent.transcript.jsonl", "session-log.jsonl", "audio/out-000.pcm", "provider.json"}
    require(set(artifact_hashes) == expected_paths, "shipped replay artifact inventory changed")
    for path in expected_paths:
        artifact = record_dir / path
        require(artifact.is_file() and artifact_hashes[path] == sha256_file(artifact), f"shipped replay hash mismatch for {path}")
    require(sha256_file(provider_path) == EXPECTED_FIXTURE_SHA, "shipped replay provider fixture bytes changed")
    fixture_types = [item.get("type") for item in load_json(C21_FIXTURE).get("records", [])]
    expected_types = [
        "session.update", "session.created", "conversation.item.create", "response.create", "response.created",
        "response.output_item.added", "response.function_call_arguments.done", "response.done", "conversation.item.create",
        "response.create", "response.created", "response.output_audio.delta", "response.output_audio.delta",
        "response.output_audio.done", "response.output_text.delta", "response.output_text.done", "response.done", "session.closed",
    ]
    require(fixture_types == expected_types, "read-only replay event ordering changed")
    pcm = pcm_path.read_bytes()
    require(len(pcm) == EXPECTED_PCM_BYTES and sha256_bytes(pcm) == EXPECTED_PCM_SHA, "shipped replay PCM effect changed")
    session_log = load_jsonl(session_log_path)
    require(len(session_log) == 1, "shipped replay turn count changed")
    response = session_log[0].get("response", {})
    require(response.get("text") == "strict replay continuation" and response.get("complete") is True and response.get("audio_bytes") == EXPECTED_PCM_BYTES, "shipped replay response changed")
    events = session_log[0].get("tool_events")
    require([event.get("type") for event in events] == ["tool_call", "tool_result"] and events[1].get("content") == "PROBE_TOOL_MARKER_9182\n", "shipped replay tool effects changed")
    return {
        "schema": "audio-runtime.c54.shipped-replay/v1",
        "passed": True,
        "source_revision": source_revision,
        "candidate_revision": candidate_revision,
        "execution": result,
        "fixture": {"path": relative(C21_FIXTURE), "bytes": C21_FIXTURE.stat().st_size, "sha256": sha256_file(C21_FIXTURE), "event_count": len(fixture_types), "ordered_types": fixture_types},
        "record": {"manifest": relative(manifest_path), "provider": relative(provider_path), "session_log": relative(session_log_path), "pcm": relative(pcm_path), "pcm_bytes": len(pcm), "pcm_sha256": sha256_bytes(pcm), "artifact_count": len(expected_paths)},
        "effects": {"tool_marker": "PROBE_TOOL_MARKER_9182", "response_text": "strict replay continuation", "clean_shutdown": True, "physical_device": "not_attempted", "acoustic": "not_attempted"},
        "binary": {"path": relative(YUI_BINARY), "sha256": sha256_file(YUI_BINARY)},
    }


def run_focused_tests(source_revision: str) -> dict[str, Any]:
    commands = {
        "runtime-normal": (ROOT / "go-agent-runtime", ["go", "test", "-count=1", "-timeout=50s", "./services/rooms/..." ]),
        "runtime-race": (ROOT / "go-agent-runtime", ["go", "test", "-race", "-count=1", "-timeout=50s", "./services/rooms/..." ]),
        "cli-normal": (ROOT / "agent-cli", ["go", "test", "-count=1", "-timeout=50s", "./internal/room", "./internal/services/rooms/internal/launch", "./internal/transport/cli"]),
        "cli-race": (ROOT / "agent-cli", ["go", "test", "-race", "-count=1", "-timeout=50s", "./internal/room"]),
    }
    results = {}
    for name, (cwd, argv) in commands.items():
        result = run_child(argv, f"focused-{name}", cwd, ARTIFACTS / f"focused-{name}", source_revision, MAX_CHILD_SECONDS, str(ROOT / "go.work"))
        execution_ok(result, f"focused {name}")
        results[name] = result
    return results


def run_cleanup_controls(source_revision: str) -> dict[str, Any]:
    timeout_script = "import os, signal, time; child=os.fork(); signal.signal(signal.SIGTERM, signal.SIG_IGN); (os._exit(0) if child == 0 and False else None); time.sleep(1000)"
    # The child and parent both ignore TERM; the runner must escalate the whole
    # process group to SIGKILL and reap it before the two-second cleanup bound.
    timeout_script = "import os,signal,time; child=os.fork(); signal.signal(signal.SIGTERM,signal.SIG_IGN); time.sleep(1000)"
    timeout_result = run_child([sys.executable, "-c", timeout_script], "forced-timeout-process-group", HERE, ARTIFACTS / "cleanup-timeout", source_revision, 1.0, "off")
    require(timeout_result.get("timed_out") and timeout_result.get("cleanup", {}).get("term_sent") and timeout_result.get("cleanup", {}).get("kill_sent"), "forced-timeout control did not exercise TERM/KILL cleanup")
    require(timeout_result.get("cleanup", {}).get("parent_reaped") and timeout_result.get("cleanup", {}).get("group_alive_after") is False and timeout_result.get("cleanup", {}).get("reader_threads_joined"), "forced-timeout control left a process survivor")
    overflow_script = "import sys; sys.stdout.write('x' * 131072); sys.stdout.flush()"
    overflow_result = run_child([sys.executable, "-c", overflow_script], "bounded-output-overflow", HERE, ARTIFACTS / "cleanup-overflow", source_revision, 10.0, "off")
    require(overflow_result.get("returncode") == 0 and overflow_result.get("output_bounded") is False, "overflow control did not fail at the output cap")
    require(overflow_result.get("cleanup", {}).get("parent_reaped") and overflow_result.get("cleanup", {}).get("group_alive_after") is False, "overflow control cleanup failed")
    require(overflow_result.get("disk_bytes", MAX_RETAINED_DISK_BYTES + 1) <= MAX_RETAINED_DISK_BYTES, "overflow control retained unbounded output")
    return {"schema": "audio-runtime.c54.cleanup-controls/v1", "passed": True, "forced_timeout": {"expected_failure": "timeout", "execution": timeout_result}, "overflow": {"expected_failure": "output-cap", "execution": overflow_result}}


def build_inputs() -> list[dict[str, Any]]:
    paths = [
        "prd.json", "go.work", "go.work.sum", "agent-cli/go.mod", "agent-cli/go.sum", "go-agent-runtime/go.mod", "go-agent-runtime/go.sum", "go-agent-loop/go.mod", "go-audio/go.mod", "go-device-gateway/go.mod", "go-llm-gateway/go.mod",
        "factory/projects/audio-runtime/manifest.json", "factory/projects/audio-runtime/acceptance.md", "factory/projects/audio-runtime/source-plan.md",
        "agent-cli/internal/room/manifest.go", "agent-cli/internal/room/manifest_test.go", "agent-cli/internal/room/browser_tools.go", "agent-cli/internal/room/manifest_browser_tools_test.go",
        "go-agent-runtime/services/rooms/manifest.go", "go-agent-runtime/services/rooms/browser.go", "go-agent-runtime/services/rooms/internal/manifest/decoder.go", "go-agent-runtime/services/rooms/internal/manifest/browser.go", "go-agent-runtime/services/rooms/wire/manifest.go", "go-agent-runtime/services/rooms/wire/wire_gen.go", "docs/architecture/architecture-size-baseline.json",
        str(OWNED_REL / "consumer/go.mod"), str(OWNED_REL / "consumer/go.sum"), str(OWNED_REL / "consumer/main.go"), str(OWNED_REL / "consumer/main_test.go"), str(OWNED_REL / "fixtures/cli-invalid-nested-browser.yaml"), str(OWNED_REL / "run.py"), str(OWNED_REL / "verify.py"), str(OWNED_REL / "admission-inventory.json"),
        "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures/c16-audio-tool.session.json",
    ]
    result = []
    for value in paths:
        path = ROOT / value
        require(path.is_file(), f"missing build/provenance input: {value}")
        result.append({"path": value, "bytes": path.stat().st_size, "sha256": sha256_file(path)})
    return result


def prepare_provenance(source_revision: str, candidate_revision: str, cases: dict[str, Any], elapsed_ms: int) -> dict[str, Any]:
    origin_main = git_value("rev-parse", "origin/main")
    require(is_ancestor(BASE_REVISION, origin_main), "fetched origin/main no longer descends from the admitted baseline")
    wire_gen = ROOT / "go-agent-runtime/services/rooms/wire/wire_gen.go"
    baseline_wire = subprocess.run(["git", "show", f"{BASE_REVISION}:go-agent-runtime/services/rooms/wire/wire_gen.go"], cwd=ROOT, check=True, capture_output=True, timeout=20).stdout
    cli_manifest = ROOT / "agent-cli/internal/room/manifest.go"
    cli_browser = ROOT / "agent-cli/internal/room/browser_tools.go"
    provenance = {
        "schema": "audio-runtime.c54.provenance.v1",
        "project": "audio-runtime",
        "task": "audio-runtime-c54-room-document-admission",
        "contract_revision": "audio-runtime-v1",
        "branch": git_value("branch", "--show-current"),
        "source_revision": source_revision,
        "admitted_base_revision": BASE_REVISION,
        "fresh_fetch_origin_main_revision": origin_main,
        "fresh_fetch_is_newer_descendant": origin_main != BASE_REVISION,
        "candidate_revision": candidate_revision,
        "integration_revision": INTEGRATION_REVISION,
        "baseline_revision": BASELINE_REVISION,
        "ancestry": {"base": is_ancestor(BASE_REVISION, candidate_revision), "integration": is_ancestor(INTEGRATION_REVISION, candidate_revision), "baseline": is_ancestor(BASELINE_REVISION, candidate_revision)},
        "source_tree_status": git_value("status", "--porcelain", "--untracked-files=all"),
        "scope": {"owned_paths": ["agent-cli/internal/room/manifest.go", "agent-cli/internal/room/manifest_test.go", "agent-cli/internal/room/browser_tools.go", "agent-cli/internal/room/manifest_browser_tools_test.go", "go-agent-runtime/services/rooms/manifest.go", "go-agent-runtime/services/rooms/browser.go", "go-agent-runtime/services/rooms/internal/manifest/decoder.go", "go-agent-runtime/services/rooms/internal/manifest/browser.go", "go-agent-runtime/services/rooms/wire/manifest.go", "go-agent-runtime/services/rooms/wire/manifest_test.go", "docs/architecture/architecture-size-baseline.json", str(OWNED_REL) + "/"], "wire_gen_sha256": sha256_file(wire_gen), "wire_gen_baseline_sha256": sha256_bytes(baseline_wire), "wire_gen_unchanged": sha256_file(wire_gen) == sha256_bytes(baseline_wire)},
        "cli_ownership": {"manifest_lines": len(cli_manifest.read_text(encoding="utf-8").splitlines()), "browser_lines": len(cli_browser.read_text(encoding="utf-8").splitlines()), "pinned_manifest_lines": 765, "pinned_browser_lines": 739, "runtime_raw_decoder_owner": True, "cli_parser_retired": True, "cli_provider_adapter": True},
        "build": {"toolchain": subprocess.run(["go", "version"], check=True, capture_output=True, text=True, timeout=20).stdout.strip(), "inputs": build_inputs(), "consumer": cases.get("consumer", {}).get("binary", {}), "yui": cases.get("yui", {}).get("binary", {})},
        "fixtures": {"c21_audio_tool_sha256": EXPECTED_FIXTURE_SHA, "expected_pcm_bytes": EXPECTED_PCM_BYTES, "expected_pcm_sha256": EXPECTED_PCM_SHA, "cli_admission_sha256": sha256_file(CLI_FIXTURE)},
        "bounds": {"child_timeout_seconds": MAX_CHILD_SECONDS, "aggregate_timeout_seconds": MAX_AGGREGATE_SECONDS, "max_child_output_bytes": MAX_CHILD_OUTPUT_BYTES, "max_retained_disk_bytes": MAX_RETAINED_DISK_BYTES, "elapsed_ms": elapsed_ms},
        "admission_inventory": str(OWNED_REL / "admission-inventory.json"),
        "case_names": sorted(cases),
    }
    write_json(PROVENANCE, provenance)
    return provenance


def run_all(source_revision: str, candidate_revision: str, budget: float) -> dict[str, Any]:
    started = time.monotonic()
    cases: dict[str, Any] = {}
    cases["baseline"] = run_baseline_failure(source_revision, candidate_revision)
    cases["consumer"] = build_consumer(source_revision)
    cases["consumer_tests"] = run_consumer_tests(source_revision)
    cases["consumer_positive"] = run_consumer_positive(source_revision, candidate_revision)
    cases["negative_controls"] = run_consumer_negative_controls(source_revision, candidate_revision)
    cases["yui"] = build_yui(source_revision)
    cases["yui_admission"] = run_cli_admission(source_revision, candidate_revision)
    cases["replay"] = run_shipped_replay(source_revision, candidate_revision)
    cases["focused"] = run_focused_tests(source_revision)
    cases["cleanup_controls"] = run_cleanup_controls(source_revision)
    elapsed_ms = int((time.monotonic() - started) * 1000)
    require(elapsed_ms <= budget * 1000, f"aggregate evidence deadline exceeded: {elapsed_ms}ms")
    provenance = prepare_provenance(source_revision, candidate_revision, {"consumer": cases["consumer"], "yui": cases["yui"]}, elapsed_ms)
    summary = {"schema": "audio-runtime.c54.verification-summary.v1", "project": "audio-runtime", "task": "audio-runtime-c54-room-document-admission", "passed": True, "candidate_revision": candidate_revision, "source_revision": source_revision, "provenance": relative(PROVENANCE), "provenance_candidate_revision": provenance["candidate_revision"], "cases": cases}
    write_json(SUMMARY, summary)
    return summary


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", choices=["all"], default="all")
    parser.add_argument("--child-timeout", type=float, default=MAX_CHILD_SECONDS)
    parser.add_argument("--aggregate-timeout", type=float, default=MAX_AGGREGATE_SECONDS)
    args = parser.parse_args()
    require(args.child_timeout <= MAX_CHILD_SECONDS, "child timeout exceeds 60 seconds")
    require(0 < args.aggregate_timeout <= MAX_AGGREGATE_SECONDS, "aggregate timeout exceeds 600 seconds")
    source_revision = BASE_REVISION
    candidate_revision = git_value("rev-parse", "HEAD")
    require(git_value("branch", "--show-current") == EXPECTED_BRANCH, "candidate branch is not the admitted C54 branch")
    summary = run_all(source_revision, candidate_revision, args.aggregate_timeout)
    print(json.dumps({"schema": summary["schema"], "passed": True, "candidate_revision": candidate_revision, "source_revision": source_revision}, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (EvidenceError, subprocess.SubprocessError) as error:
        print(f"evidence failure: {error}", file=sys.stderr)
        raise SystemExit(1)
