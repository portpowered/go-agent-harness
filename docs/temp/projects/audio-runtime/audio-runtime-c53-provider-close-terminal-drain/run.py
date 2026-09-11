#!/usr/bin/env python3
"""Bounded C53 public-runtime and shipped-replay evidence runner."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import tarfile
import tempfile
import threading
import time


OWNED_ROOT = Path(__file__).resolve().parent
REPO_ROOT = Path(subprocess.run(["git", "rev-parse", "--show-toplevel"], cwd=OWNED_ROOT, check=True, capture_output=True, text=True).stdout.strip())
OWNED_REL = OWNED_ROOT.relative_to(REPO_ROOT)
PEER_ROOT = REPO_ROOT.parent / "audio-runtime-c23-long-session-tool-characterization"
C05_ROOT = REPO_ROOT.parent / "audio-runtime-c05-provider-terminal-policy"
FIXTURE = OWNED_ROOT / "fixtures.json"
BIN_ROOT = OWNED_ROOT / "bin"
ARTIFACT_ROOT = OWNED_ROOT / "artifacts"
REPORT_ROOT = OWNED_ROOT / "reports"
PROVENANCE = OWNED_ROOT / "provenance.json"
BASE_REVISION = "2456a5d1594e73faf85e1132d6050735bc3e4710"
INTEGRATION_REVISION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
C23_HEAD = "6677465e00c9fd052a7c6b6bc69cc17f8e32e02f"
C23_FIRST_FAILURE_SHA = "186d2c5902947582202d33afe1fa0d62c61d38d2d15bc3ae3fad8819f3a7b7f5"
C23_CHILD_REPORT_SHA = "d504b8a6f5a00d049c9af7a43649a98ca1080d6baee832e226bbb0e603bb2819"
MAX_CHILD_SECONDS = 60.0
MAX_CHILD_OUTPUT_BYTES = 64 * 1024
MAX_RETAINED_DISK_BYTES = 8 * 1024 * 1024
SHIPPED_EXPECTED_BYTES = 9120
SHIPPED_EXPECTED_SHA = "9495487706f4a3001922c10d4dd496e7cd29fff9815a76f2f842f8e970725cc9"
SHIPPED_EXPECTED_TYPES = [
    "session.update",
    "session.created",
    "conversation.item.create",
    "response.create",
    "response.created",
    "response.output_audio.delta",
    "input_audio_buffer.speech_started",
    "conversation.item.truncate",
    "conversation.item.truncated",
    "response.output_audio.done",
    "response.done",
    "response.created",
    "response.output_audio.delta",
    "response.output_audio.done",
    "response.done",
]


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


def load_json(path: Path) -> dict:
    require(path.is_file() and not path.is_symlink(), f"missing JSON artifact: {path}")
    try:
        value = json.loads(path.read_text())
    except json.JSONDecodeError as error:
        raise EvidenceError(f"invalid JSON artifact {path}: {error}") from error
    require(isinstance(value, dict), f"JSON artifact is not an object: {path}")
    return value


def write_json(path: Path, value: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")


def git_value(*args: str, cwd: Path = REPO_ROOT) -> str:
    result = subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True, text=True)
    return result.stdout.strip()


def is_ancestor(ancestor: str, descendant: str) -> bool:
    return subprocess.run(["git", "merge-base", "--is-ancestor", ancestor, descendant], cwd=REPO_ROOT, check=False).returncode == 0


def relative_owned(path: Path) -> str:
    return str(path.resolve().relative_to(OWNED_ROOT.resolve()))


def relative_repo(path: Path) -> str:
    return str(path.resolve().relative_to(REPO_ROOT.resolve()))


def path_label(path: Path) -> str:
    try:
        return relative_repo(path)
    except ValueError:
        return "<temporary-base-archive>"


def c23_artifact_paths() -> tuple[Path, Path]:
    first = PEER_ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c23-long-session-tool-characterization/artifacts/first-failures/0304ec692497ddfc7b0c31f7fd67cd17a7bd3a1e-controls.json"
    child = PEER_ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c23-long-session-tool-characterization/artifacts/runs/20260911T082822Z-controls-interruption/consumer-report.json"
    return first, child


def verify_preserved_inputs() -> dict:
    first, child = c23_artifact_paths()
    require(first.is_file() and child.is_file(), "C23 immutable failure artifacts are missing")
    require(sha256_file(first) == C23_FIRST_FAILURE_SHA, "C23 first-failure artifact changed")
    require(sha256_file(child) == C23_CHILD_REPORT_SHA, "C23 child report artifact changed")
    require(git_value("rev-parse", "HEAD", cwd=PEER_ROOT) == C23_HEAD, "C23 predecessor head changed")
    c23_status = git_value("status", "--porcelain", "--untracked-files=all", cwd=PEER_ROOT)
    require(not c23_status, f"C23 predecessor is no longer clean: {c23_status.splitlines()[0] if c23_status else ''}")
    c05_status = ""
    c05_branch = "unavailable"
    if C05_ROOT.is_dir():
        c05_status = git_value("status", "--porcelain", "--untracked-files=all", cwd=C05_ROOT)
        c05_branch = git_value("branch", "--show-current", cwd=C05_ROOT)
    return {
        "c23": {"head": C23_HEAD, "first_failure_sha256": sha256_file(first), "child_report_sha256": sha256_file(child), "status": "clean"},
        "c05": {"worktree": str(C05_ROOT), "branch": c05_branch, "status": c05_status, "status_scope": "recorded_only; uncommitted contents not read"},
    }


def go_mod_cache() -> str:
    return subprocess.run(["go", "env", "GOMODCACHE"], check=True, capture_output=True, text=True).stdout.strip()


def sanitized_environment(source_revision: str, run_root: Path, gowork: str, extras: dict[str, str] | None = None) -> dict[str, str]:
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
        "C53_SOURCE_REVISION": source_revision,
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


def run_child(command: list[str], label: str, cwd: Path, run_root: Path, source_revision: str, timeout: float = MAX_CHILD_SECONDS, gowork: str = "off", extras: dict[str, str] | None = None) -> dict:
    require(0 < timeout <= MAX_CHILD_SECONDS, f"{label} timeout exceeds the 60 second child bound")
    run_root.mkdir(parents=True, exist_ok=True)
    stdout_path, stderr_path = run_root / "stdout.log", run_root / "stderr.log"
    observed = {"stdout": 0, "stderr": 0}
    overflow = {"stdout": False, "stderr": False}

    def drain(stream, path: Path, stream_name: str) -> None:
        try:
            with path.open("wb") as output:
                while True:
                    chunk = stream.read(8192)
                    if not chunk:
                        return
                    previous = observed[stream_name]
                    observed[stream_name] += len(chunk)
                    keep = max(0, min(len(chunk), MAX_CHILD_OUTPUT_BYTES - previous))
                    if keep:
                        output.write(chunk[:keep])
                    if observed[stream_name] > MAX_CHILD_OUTPUT_BYTES:
                        overflow[stream_name] = True
        except (OSError, ValueError):
            overflow[stream_name] = True

    started = time.monotonic()
    term_sent = kill_sent = timed_out = False
    parent_reaped = False
    setup_error = ""
    process = None
    threads = []
    try:
        process = subprocess.Popen(command, cwd=cwd, env=sanitized_environment(source_revision, run_root, gowork, extras), stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
        before_alive = group_alive(process.pid)
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
    except OSError as error:
        returncode = None
        setup_error = str(error)
        before_alive = False
    elapsed_ms = int((time.monotonic() - started) * 1000)
    after_alive = group_alive(process.pid) if process is not None else False
    disk_bytes = sum(path.stat().st_size for path in (stdout_path, stderr_path) if path.is_file())
    return {
        "label": label,
        "command": command,
        "cwd": path_label(cwd),
        "returncode": returncode,
        "timed_out": timed_out,
        "elapsed_ms": elapsed_ms,
        "stdout": relative_owned(stdout_path),
        "stderr": relative_owned(stderr_path),
        "stdout_bytes": observed["stdout"],
        "stderr_bytes": observed["stderr"],
        "output_bounded": not any(overflow.values()),
        "disk_bytes": disk_bytes,
        "disk_bounded": disk_bytes <= MAX_RETAINED_DISK_BYTES,
        "cleanup": {"parent_reaped": parent_reaped, "group_alive_before": before_alive, "group_alive_after": after_alive, "reader_threads_joined": not any(thread.is_alive() for thread in threads), "term_sent": term_sent, "kill_sent": kill_sent, "setup_error": setup_error},
    }


def build_consumer(source_revision: str, label: str, run_root: Path, output: Path) -> tuple[dict, str]:
    result = run_child(["go", "build", "-mod=readonly", "-trimpath", "-o", str(output), "./consumer"], label, OWNED_ROOT, run_root, source_revision, gowork="off")
    require(result["returncode"] == 0 and result["cleanup"]["parent_reaped"], f"{label} failed to build: {result}")
    return result, sha256_file(output)


def prepare_provenance(source_revision: str, candidate_revision: str, consumer: dict | None, consumer_sha: str | None, yui: dict | None = None, yui_sha: str | None = None) -> dict:
    preserved = verify_preserved_inputs()
    require(is_ancestor(BASE_REVISION, candidate_revision), "candidate does not preserve current-main ancestry")
    require(is_ancestor(INTEGRATION_REVISION, candidate_revision), "candidate does not preserve startup integration ancestry")
    source_plan = REPO_ROOT / "factory/projects/audio-runtime/source-plan.md"
    inputs = []
    for relative in [
        "go.work",
        "go.work.sum",
        "agent-cli/go.mod",
        "go-agent-loop/go.mod",
        "go-agent-runtime/go.mod",
        "go-audio/go.mod",
        "go-device-gateway/go.mod",
        "go-llm-gateway/go.mod",
        "go-agent-runtime/services/session/internal/live/control.go",
        "go-agent-runtime/services/session/internal/live/lifecycle.go",
        "go-agent-runtime/services/session/internal/live/observation.go",
        "go-agent-runtime/services/session/internal/live/service.go",
        "go-agent-runtime/services/session/internal/live/start_support.go",
        "go-agent-runtime/services/session/internal/live/policies_test.go",
        str(OWNED_REL / "go.mod"),
        str(OWNED_REL / "go.sum"),
        str(OWNED_REL / "fixtures.json"),
        str(OWNED_REL / "consumer/main.go"),
        str(OWNED_REL / "run.py"),
        str(OWNED_REL / "verify.py"),
    ]:
        path = REPO_ROOT / relative
        if path.is_file():
            inputs.append({"path": relative, "bytes": path.stat().st_size, "sha256": sha256_file(path)})
    provenance = {
        "schema": "audio-runtime.c53.provenance.v1",
        "project": "audio-runtime",
        "task": "audio-runtime-c53-provider-close-terminal-drain",
        "contract_revision": "audio-runtime-v1",
        "branch": git_value("branch", "--show-current"),
        "source_revision": source_revision,
        "candidate_revision": candidate_revision,
        "current_main_fetched": source_revision == BASE_REVISION,
        "integration_revision": INTEGRATION_REVISION,
        "baseline_manifest_revision": "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad",
        "c23_failure_source_revision": "6b4f32b5940d43192774e86f05e3c7c9293c71e3",
        "preserved": preserved,
        "source_plan_sha256": sha256_file(source_plan) if source_plan.is_file() else None,
        "source_tree_status": git_value("status", "--porcelain", "--untracked-files=all"),
        "build": {"toolchain": subprocess.run(["go", "version"], check=True, capture_output=True, text=True).stdout.strip(), "flags": ["-trimpath", "-mod=readonly"], "environment_allowlist": ["PATH", "HOME", "TMPDIR", "GOCACHE", "GOMODCACHE", "GOWORK", "LANG", "LC_ALL", "C53_SOURCE_REVISION"]},
        "builds": {"consumer": {"sha256": consumer_sha, "result": consumer} if consumer else {}, "yui": {"sha256": yui_sha, "result": yui} if yui else {}},
        "build_inputs": inputs,
        "bounds": {"child_timeout_seconds": MAX_CHILD_SECONDS, "aggregate_timeout_seconds": 600, "max_child_output_bytes": MAX_CHILD_OUTPUT_BYTES, "max_retained_disk_bytes": MAX_RETAINED_DISK_BYTES},
        "owned_paths": [str(path) for path in ["go-agent-runtime/services/session/internal/live/observation.go", "go-agent-runtime/services/session/internal/live/start_support.go", "go-agent-runtime/services/session/internal/live/lifecycle.go", "go-agent-runtime/services/session/internal/live/control.go", "go-agent-runtime/services/session/internal/live/service.go", "go-agent-runtime/services/session/internal/live/policies_test.go", "go-agent-runtime/services/session/internal/live/terminal_drain_test.go", str(OWNED_REL)]],
    }
    write_json(PROVENANCE, provenance)
    return provenance


def ensure_candidate(source_revision: str, cache: dict) -> tuple[Path, dict]:
    if "consumer" in cache:
        return cache["consumer_path"], cache["consumer_build"]
    output = BIN_ROOT / "c53-consumer"
    result, digest = build_consumer(source_revision, "build-candidate-consumer", ARTIFACT_ROOT / "build-candidate-consumer", output)
    cache.update({"consumer_path": output, "consumer_build": result, "consumer_sha": digest})
    return output, result


def run_public(source_revision: str, cache: dict, label: str, mutation: str = "") -> tuple[dict, dict]:
    binary, _ = ensure_candidate(source_revision, cache)
    output = ARTIFACT_ROOT / label / "consumer-report.json"
    command = [str(binary), "--fixture", str(FIXTURE), "--output", str(output)]
    if mutation:
        command.extend(["--mutation", mutation])
    execution = run_child(command, label, OWNED_ROOT, ARTIFACT_ROOT / label / "process", source_revision, gowork="off")
    report = load_json(output)
    return execution, report


def case_provider_close(source_revision: str, candidate_revision: str, cache: dict, expect: str) -> None:
    candidate_binary, _ = ensure_candidate(source_revision, cache)
    root = ARTIFACT_ROOT / "provider-close-overtake"
    root.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="c53-base-") as temporary:
        temp_root = Path(temporary)
        archive = temp_root / "base.tar"
        with archive.open("wb") as stream:
            subprocess.run(["git", "archive", "--format=tar", BASE_REVISION], cwd=REPO_ROOT, check=True, stdout=stream)
        archived_root = temp_root / "repo"
        archived_root.mkdir()
        with tarfile.open(archive) as tar:
            tar.extractall(archived_root)
        base_evidence = archived_root / OWNED_REL
        (base_evidence / "consumer").mkdir(parents=True, exist_ok=True)
        for relative in ("go.mod", "go.sum", "fixtures.json", "consumer/main.go"):
            destination = base_evidence / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(OWNED_ROOT / relative, destination)
        base_binary = base_evidence / "bin" / "c53-consumer"
        base_build = run_child(["go", "build", "-mod=readonly", "-trimpath", "-o", str(base_binary), "./consumer"], "build-base-consumer", base_evidence, root / "base-build", BASE_REVISION, gowork="off")
        require(base_build["returncode"] == 0, f"base consumer build failed: {base_build}")
        base_run = run_child([str(base_binary), "--fixture", str(FIXTURE), "--output", str(root / "base-report.json")], "base-provider-close-overtake", REPO_ROOT, root / "base-run", BASE_REVISION, gowork="off")
        base_report = load_json(root / "base-report.json")
        require(base_run["returncode"] != 0, "unmodified current-main base unexpectedly passed the causal provider-close control")
        require(len(base_report.get("trace", [])) > 2 and base_report.get("error"), "base failure did not reach the public terminal oracle")
        write_json(root / "baseline-first-failure.json", {"schema": "audio-runtime.c53.baseline-first-failure.v1", "expected_outcome": "base-failure", "source_revision": BASE_REVISION, "candidate_revision": candidate_revision, "base_binary_sha256": sha256_file(base_binary), "build": base_build, "execution": base_run, "report": base_report, "causal_boundary": "healthy MESSAGE.END is absent or public loop teardown is error/partial before provider-scoped SESSION.CLOSE can retire the response"})
    candidate_run, candidate_report = run_public(source_revision, cache, "provider-close-overtake/candidate")
    require(candidate_run["returncode"] == 0, f"candidate provider-close control failed: {candidate_report}")
    write_json(root / "candidate.json", {"schema": "audio-runtime.c53.provider-close.v1", "expected_outcome": "repaired-pass", "execution": candidate_run, "report": candidate_report})
    require(expect in ("base-failure", "repaired", ""), f"unsupported provider-close expectation: {expect}")


def case_public(source_revision: str, cache: dict) -> None:
    execution, report = run_public(source_revision, cache, "public-interruption-provider-close")
    require(execution["returncode"] == 0, f"public interruption consumer failed: {report}")
    write_json(REPORT_ROOT / "public-interruption-provider-close.json", {"execution": execution, "report": report})


def case_negative(source_revision: str, cache: dict) -> None:
    mutations = ["missing-message-end", "duplicate-message-end", "reordered-session-close", "admitted-cancelled-audio", "mutated-healthy-pcm"]
    results = []
    for mutation in mutations:
        execution, report = run_public(source_revision, cache, f"negative-{mutation}", mutation)
        require(execution["returncode"] != 0, f"negative control unexpectedly passed: {mutation}")
        require(execution["cleanup"]["parent_reaped"] and execution["cleanup"]["group_alive_after"] is False, f"negative control cleanup failed: {mutation}")
        require(len(report.get("trace", [])) > 1 and report.get("error"), f"negative control failed before its public oracle: {mutation}")
        results.append({"mutation": mutation, "execution": execution, "report": report})
    write_json(REPORT_ROOT / "negative-controls.json", {"schema": "audio-runtime.c53.negative-controls.v1", "passed": True, "controls": results})


def case_focused(source_revision: str, race: bool) -> None:
    label = "focused-live-race" if race else "focused-live-normal"
    command = ["go", "test", "-tags=nomicrophone", "-count=1", "-timeout=60s"]
    if race:
        command.insert(2, "-race")
    command.append("./services/session/internal/live")
    result = run_child(command, label, REPO_ROOT / "go-agent-runtime", ARTIFACT_ROOT / label / "process", source_revision, timeout=60, gowork=str(REPO_ROOT / "go.work"), extras={"CGO_ENABLED": "0"})
    require(result["returncode"] == 0, f"{label} failed: {result}")
    write_json(REPORT_ROOT / f"{label}.json", {"schema": "audio-runtime.c53.focused-test.v1", "passed": True, "execution": result})


def case_shipped(source_revision: str, cache: dict) -> None:
    root = ARTIFACT_ROOT / "shipped-yui-audio-tool-interruption-replay"
    yui = BIN_ROOT / "yui"
    build = run_child(["go", "build", "-trimpath", "-o", str(yui), "./agent-cli/cmd/yui"], "build-candidate-yui", REPO_ROOT, root / "build", source_revision, gowork=str(REPO_ROOT / "go.work"))
    require(build["returncode"] == 0, f"YUI build failed: {build}")
    yui_sha = sha256_file(yui)
    capture = PEER_ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c23-long-session-tool-characterization/artifacts/runs/20260910T234434Z-shipped-regressions/test6.session.json"
    config = PEER_ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c23-long-session-tool-characterization/artifacts/runs/20260910T234434Z-shipped-regressions/config"
    shipped_manifest = PEER_ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c23-long-session-tool-characterization/shipped-regressions.json"
    input_fixture = PEER_ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c23-long-session-tool-characterization/fixtures.json"
    source_audio = REPO_ROOT / "agent-cli/internal/transport/cli/testdata/test6-openai-barge-in.base64"
    frozen = load_json(shipped_manifest)
    capture_value = load_json(capture)
    types = [record.get("payload", {}).get("type") for record in capture_value.get("records", [])]
    require(types == SHIPPED_EXPECTED_TYPES, "shipped capture event/tool/interruption order changed")
    output = root / "healthy-tail.pcm"
    command = [str(yui), "-C", str(config), "session", "--replay", str(capture), "--replay-timing", "recorded", "--prompt", "test6 customer barge in", "--audio-out", str(output), "--no-terminal-tools"]
    execution = run_child(command, "shipped-yui-test6", REPO_ROOT, root / "process", source_revision, gowork=str(REPO_ROOT / "go.work"))
    require(execution["returncode"] == 0 and output.is_file(), f"shipped YUI replay failed: {execution}")
    output_sha = sha256_file(output)
    output_bytes = output.stat().st_size
    require(output_bytes == SHIPPED_EXPECTED_BYTES and output_sha == SHIPPED_EXPECTED_SHA, "shipped YUI PCM length/order/SHA changed")
    result = {"schema": "audio-runtime.c53.shipped-replay.v1", "passed": True, "source_revision": source_revision, "binary": {"path": relative_owned(yui), "sha256": yui_sha, "build": build}, "inputs": {"capture_sha256": sha256_file(capture), "config_sha256": sha256_tree(config), "fixture_sha256": sha256_file(input_fixture), "source_audio_sha256": sha256_file(source_audio), "frozen_manifest_sha256": sha256_file(shipped_manifest), "expected_manifest": frozen.get("audio_output", {})}, "execution": execution, "capture": {"record_count": len(capture_value.get("records", [])), "ordered_types": types, "sequence_contiguous": [record.get("sequence") for record in capture_value.get("records", [])] == list(range(1, len(types) + 1)), "interrupted_response_id": "resp-test6-interrupted", "healthy_response_id": "resp-test6-new-assistant"}, "audio_output": {"path": relative_owned(output), "bytes": output_bytes, "sha256": output_sha, "expected_bytes": SHIPPED_EXPECTED_BYTES, "expected_sha256": SHIPPED_EXPECTED_SHA}, "classification": {"provider_edge": "credential-free shipped replay", "audio_tool_interruption_order": "proved", "physical_device": "not_attempted", "acoustic": "not_attempted", "quiet_host": "not_claimed"}}
    write_json(REPORT_ROOT / "shipped-yui-audio-tool-interruption-replay.json", result)
    cache.update({"yui_build": build, "yui_sha": yui_sha})


def sha256_tree(path: Path) -> str:
    digest = hashlib.sha256()
    for child in sorted(path.rglob("*")):
        if child.is_file() and not child.is_symlink():
            digest.update(str(child.relative_to(path)).encode())
            digest.update(sha256_file(child).encode())
    return digest.hexdigest()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", required=True, choices=["provider-close-overtake", "public-interruption-provider-close", "public-negative-controls", "focused-live-normal", "focused-live-race", "shipped-yui-audio-tool-interruption-replay"])
    parser.add_argument("--expect", default="")
    parser.add_argument("--child-timeout", type=float, default=60.0)
    parser.add_argument("--aggregate-timeout", type=float, default=180.0)
    args = parser.parse_args()
    require(args.child_timeout <= MAX_CHILD_SECONDS, "child timeout exceeds 60 seconds")
    require(0 < args.aggregate_timeout < 600, "aggregate timeout must be below 600 seconds")
    started = time.monotonic()
    source_revision = git_value("rev-parse", "origin/main")
    candidate_revision = git_value("rev-parse", "HEAD")
    require(source_revision == BASE_REVISION, f"origin/main is {source_revision}, expected freshly fetched {BASE_REVISION}")
    cache: dict = {}
    consumer_result = consumer_sha = None
    if args.case not in ("focused-live-normal", "focused-live-race"):
        ensure_candidate(source_revision, cache)
        consumer_result, consumer_sha = cache["consumer_build"], cache["consumer_sha"]
    if args.case == "provider-close-overtake":
        case_provider_close(source_revision, candidate_revision, cache, args.expect)
    elif args.case == "public-interruption-provider-close":
        case_public(source_revision, cache)
    elif args.case == "public-negative-controls":
        case_negative(source_revision, cache)
    elif args.case == "focused-live-normal":
        case_focused(source_revision, False)
    elif args.case == "focused-live-race":
        case_focused(source_revision, True)
    elif args.case == "shipped-yui-audio-tool-interruption-replay":
        case_shipped(source_revision, cache)
    require(time.monotonic() - started < args.aggregate_timeout, "aggregate evidence timeout exceeded")
    provenance = prepare_provenance(source_revision, candidate_revision, consumer_result, consumer_sha, cache.get("yui_build"), cache.get("yui_sha"))
    write_json(REPORT_ROOT / f"{args.case}.summary.json", {"schema": "audio-runtime.c53.run-summary.v1", "case": args.case, "passed": True, "elapsed_ms": int((time.monotonic() - started) * 1000), "candidate_revision": candidate_revision, "provenance": relative_owned(PROVENANCE), "bounds": provenance["bounds"]})
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except EvidenceError as error:
        print(f"evidence failure: {error}", file=os.sys.stderr)
        raise SystemExit(1)
