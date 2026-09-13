#!/usr/bin/env python3
"""Fail-closed C126 source, retirement, and causal-mutation evidence driver."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
from typing import Any


HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[4]
FACTORY_ROOT = Path(os.environ.get("FACTORY_ROOT", str(ROOT))).resolve()
WORK = "audio-runtime-c126-retire-cli-room-error-sanitization"
BRANCH = "codex/audio-runtime-c126-retire-cli-room-error-sanitization"
PROJECT = "audio-runtime"
CONTRACT = "audio-runtime-v1"
STARTUP_INTEGRATION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
ACCEPTED_MAIN = "09c70f51243caeaf1184c4806b99bbf7749e3044"
CURRENT_MAIN_REF = "origin/main"
LEGACY = "agent-cli/internal/services/internal/agentruntime/session_room_errors.go"
ADAPTER = "agent-cli/internal/services/internal/agentruntime/session_room_error_adapter.go"
ROOM_COORDINATOR_BASELINE = "docs/architecture/baselines/github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime/session_room_coordinator.go.json"
SERVICE_ROOT = "go-agent-runtime/services/sessionroomerrors"
CLI_PRODUCTION_PATHS = (
    LEGACY,
    ADAPTER,
    "agent-cli/internal/services/internal/agentruntime/session_room_coordinator.go",
    "agent-cli/internal/services/internal/agentruntime/session_room_evidence.go",
    "agent-cli/internal/services/internal/agentruntime/session_room_orchestration.go",
    "agent-cli/internal/services/internal/agentruntime/session_room_planning.go",
    "agent-cli/internal/services/internal/agentruntime/session_room_run.go",
)
OWNED_PREFIXES = (
    "agent-cli/internal/services/internal/agentruntime/session_room_errors.go",
    "agent-cli/internal/services/internal/agentruntime/session_room_error_adapter.go",
    "agent-cli/internal/services/internal/agentruntime/session_room_coordinator.go",
    "agent-cli/internal/services/internal/agentruntime/session_room_evidence.go",
    "agent-cli/internal/services/internal/agentruntime/session_room_orchestration.go",
    "agent-cli/internal/services/internal/agentruntime/session_room_planning.go",
    "agent-cli/internal/services/internal/agentruntime/session_room_run.go",
    ROOM_COORDINATOR_BASELINE,
    "agent-cli/internal/services/internal/agentruntime/session_room_terminal_test.go",
    "agent-cli/internal/services/internal/agentruntime/session_room_evidence_test.go",
    "agent-cli/internal/services/internal/agentruntime/session_room_bound_shutdown_test.go",
    SERVICE_ROOT + "/",
    "coverage-manifest/go-agent-runtime/services/sessionroomerrors/",
    "docs/temp/projects/audio-runtime/audio-runtime-c126-retire-cli-room-error-sanitization/",
    "scripts/wire-packages.txt",
)
EXCLUDED_PREFIXES = (
    "agent-cli/internal/services/internal/agentruntime/session_room_lifecycle.go",
    "agent-cli/internal/services/internal/agentruntime/session_room_tracked_session.go",
    "agent-cli/internal/services/internal/agentruntime/session_room_lifecycle_test.go",
    "agent-cli/internal/services/internal/agentruntime/session_instructions.go",
    "agent-cli/internal/services/internal/agentruntime/session_tool_lifecycle.go",
    "agent-cli/internal/services/internal/agentruntime/session_audio_trace.go",
    "agent-cli/internal/services/internal/agentruntime/session_runtime_observation.go",
    "agent-cli/internal/services/internal/agentruntime/trace.go",
    "agent-cli/internal/services/internal/agentruntime/session_audio_format.go",
    "agent-cli/internal/services/internal/agentruntime/session_audio_rate.go",
    "agent-cli/internal/services/internal/agentruntime/session_diagnostics.go",
    "agent-cli/internal/services/internal/agentruntime/session_diagnostics_failure.go",
    "agent-cli/internal/services/internal/agentruntime/session_diagnostics_terminal.go",
    "agent-cli/internal/wire/rtc_runtime.go",
    "go-llm-gateway/pkg/transport/rtc/track_in.go",
    "go-llm-gateway/pkg/transport/rtc/track_out.go",
)
SHARED_FILES = ("scripts/wire-packages.txt", "docs/architecture/architecture-policy.json")
SESSIONROOMERRORS_WIRE_ENTRY = "go-agent-runtime/services/sessionroomerrors/wire"
SOURCE_LINES = 114
SOURCE_BYTES = 2960
SOURCE_SHA256 = "7547e6b2c7a77ea34aeea3029221a26145a5269e65704fe1d74bd7244d25b750"
MAX_OUTPUT = 64 * 1024


class EvidenceFailure(RuntimeError):
    pass


def sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def stable_json(value: Any) -> str:
    return json.dumps(value, indent=2, sort_keys=True, ensure_ascii=False) + "\n"


def write_report(name: str, value: Any) -> Path:
    path = HERE / "runs" / name
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(stable_json(value), encoding="utf-8")
    return path


def safe_environment() -> dict[str, str]:
    blocked = ("KEY", "TOKEN", "SECRET", "PASSWORD", "CREDENTIAL", "OPENAI", "ANTHROPIC")
    result = {key: value for key, value in os.environ.items() if not any(marker in key.upper() for marker in blocked)}
    for key in ("PATH", "HOME", "TMPDIR", "FACTORY_ROOT", "FACTORY_SERVER_URL", "GOWORK"):
        if key in os.environ:
            result[key] = os.environ[key]
    return result


def redact(value: str) -> str:
    value = re.sub(r"(?i)(api[_-]?key|token|secret|password|credential)\s*[:=]\s*[^\s,}]+", r"\1=<redacted>", value)
    return re.sub(r"\bsk-[A-Za-z0-9_-]{12,}\b", "<redacted-api-key>", value)


def run(command: list[str], *, cwd: Path = ROOT, timeout: int = 180, env: dict[str, str] | None = None) -> dict[str, Any]:
    try:
        result = subprocess.run(
            command,
            cwd=cwd,
            env=env or safe_environment(),
            capture_output=True,
            text=True,
            timeout=timeout,
        )
    except subprocess.TimeoutExpired as error:
        return {"argv": command, "returncode": None, "timed_out": True, "stdout": redact(str(error.stdout or ""))[-MAX_OUTPUT:], "stderr": redact(str(error.stderr or ""))[-MAX_OUTPUT:]}
    return {
        "argv": command,
        "returncode": result.returncode,
        "timed_out": False,
        "stdout": redact(result.stdout)[-MAX_OUTPUT:],
        "stderr": redact(result.stderr)[-MAX_OUTPUT:],
    }


def git(*args: str, check: bool = True) -> str:
    result = subprocess.run(["git", *args], cwd=ROOT, capture_output=True, text=True, check=check, timeout=120)
    return result.stdout.strip()


def git_show(revision: str, path: str) -> bytes:
    result = subprocess.run(["git", "show", f"{revision}:{path}"], cwd=ROOT, capture_output=True, check=True, timeout=120)
    return result.stdout


def current_hash(path: str) -> str | None:
    absolute = ROOT / path
    if not absolute.is_file():
        return None
    return sha256_file(absolute)


def load_admission() -> dict[str, Any]:
    path = HERE / "admission.json"
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise EvidenceFailure(f"missing or invalid admission evidence: {path}") from error
    if value.get("project") != PROJECT or value.get("contractRevision") != CONTRACT or value.get("work") != WORK:
        raise EvidenceFailure("admission evidence is for a different project, contract, or work")
    if value.get("branch") != BRANCH or value.get("branchMatchesPrd") is not True:
        raise EvidenceFailure("admission branch does not match the admitted PRD")
    if value.get("verifyWork", {}).get("status") != "admitted":
        raise EvidenceFailure("admission evidence is not admitted")
    return value


def check_admission() -> dict[str, Any]:
    admission = load_admission()
    command = [sys.executable, str(FACTORY_ROOT / "factory/scripts/project-control.py"), "verify-work", "--type", "task", "--name", WORK, "--root", str(FACTORY_ROOT)]
    result = run(command, cwd=FACTORY_ROOT, timeout=120)
    if result["returncode"] != 0 or result["timed_out"]:
        raise EvidenceFailure(f"live project admission failed: {result}")
    try:
        payload = json.loads(result["stdout"])
    except json.JSONDecodeError as error:
        raise EvidenceFailure("live project admission did not return JSON") from error
    if payload.get("status") != "admitted" or payload.get("project") != PROJECT or payload.get("name") != WORK:
        raise EvidenceFailure(f"live project admission was not exact: {payload}")
    return {"recorded": admission["verifyWork"], "live": payload}


def check_ancestry() -> dict[str, Any]:
    checks: dict[str, Any] = {}
    for label, revision in (("startup", STARTUP_INTEGRATION), ("accepted_main", ACCEPTED_MAIN)):
        result = run(["git", "merge-base", "--is-ancestor", revision, "HEAD"])
        if result["returncode"] != 0:
            raise EvidenceFailure(f"{label} revision is not an ancestor of HEAD: {revision}")
        checks[label] = revision
    current_main = git("rev-parse", CURRENT_MAIN_REF)
    result = run(["git", "merge-base", "--is-ancestor", current_main, "HEAD"])
    if result["returncode"] != 0:
        raise EvidenceFailure(f"fresh current origin/main is not an ancestor of HEAD: {current_main}")
    checks["current_origin_main"] = current_main
    return checks


def check_immutable_source() -> dict[str, Any]:
    data = git_show(ACCEPTED_MAIN, LEGACY)
    lines = len(data.splitlines())
    facts = {"revision": ACCEPTED_MAIN, "path": LEGACY, "physical_lines": lines, "bytes": len(data), "sha256": sha256_bytes(data)}
    if lines != SOURCE_LINES or len(data) != SOURCE_BYTES or facts["sha256"] != SOURCE_SHA256:
        raise EvidenceFailure(f"immutable legacy source mismatch: {facts}")
    return facts


def changed_paths() -> list[str]:
    values: set[str] = set()
    for args in (("diff", "--name-only", CURRENT_MAIN_REF), ("diff", "--name-only"), ("diff", "--cached", "--name-only"), ("ls-files", "--others", "--exclude-standard")):
        output = git(*args, check=False)
        values.update(line.strip() for line in output.splitlines() if line.strip())
    return sorted(values)


def owned_path(path: str) -> bool:
    return any(path == prefix or prefix.endswith("/") and path.startswith(prefix) for prefix in OWNED_PREFIXES)


def check_scope() -> dict[str, Any]:
    paths = changed_paths()
    unowned = [path for path in paths if not owned_path(path)]
    if unowned:
        raise EvidenceFailure(f"unowned candidate paths: {unowned}")
    return {"changed_paths": paths, "unowned": unowned, "owned_scope": True}


def check_exclusions(admission: dict[str, Any]) -> dict[str, Any]:
    captured = admission.get("excludedPaths", {})
    if set(captured) != set(EXCLUDED_PREFIXES):
        raise EvidenceFailure("admission exclusion census is incomplete or has drifted")
    current_main = git("rev-parse", CURRENT_MAIN_REF)
    observed: dict[str, Any] = {}
    for path, expected in captured.items():
        current_main_hash = sha256_bytes(git_show(current_main, path))
        actual = current_hash(path)
        if actual != current_main_hash:
            raise EvidenceFailure(f"excluded path changed under C126: {path} current_main={current_main_hash} actual={actual}")
        observed[path] = {"pre_edit_expected": expected, "current_origin_main": current_main, "current_main_sha256": current_main_hash, "actual": actual or ""}
    return observed


def check_shared(admission: dict[str, Any]) -> dict[str, Any]:
    observed: dict[str, Any] = {}
    current_main = git("rev-parse", CURRENT_MAIN_REF)
    for path in SHARED_FILES:
        expected = admission.get("sharedFiles", {}).get(path, {}).get("sha256")
        baseline = git_show(current_main, path)
        actual = current_hash(path)
        if actual is None:
            raise EvidenceFailure(f"shared file is missing after current-main integration: {path}")
        candidate_delta = "unchanged"
        if path == "scripts/wire-packages.txt":
            baseline_lines = baseline.decode("utf-8").splitlines()
            actual_lines = (ROOT / path).read_text(encoding="utf-8").splitlines()
            if actual_lines == baseline_lines:
                candidate_delta = "unchanged"
            elif actual_lines == baseline_lines + [SESSIONROOMERRORS_WIRE_ENTRY]:
                candidate_delta = SESSIONROOMERRORS_WIRE_ENTRY
            else:
                raise EvidenceFailure(f"shared Wire registry has an unauthorized delta: {path}")
        elif actual != sha256_bytes(baseline):
            raise EvidenceFailure(f"shared file changed by C126 without a demonstrated need: {path}")
        observed[path] = {
            "pre_edit_expected": expected,
            "current_origin_main": current_main,
            "current_main_sha256": sha256_bytes(baseline),
            "actual": actual,
            "candidate_delta": candidate_delta,
        }
    return observed


def check_service_shape() -> dict[str, Any]:
    files = sorted(path for path in (ROOT / SERVICE_ROOT).rglob("*") if path.is_file() and path.suffix == ".go")
    if not files:
        raise EvidenceFailure("sessionroomerrors service has no Go files")
    forbidden = re.compile(r"agent-cli|internal/services/internal/agentruntime|os\.(Getenv|LookupEnv)|func\s+init\s*\(|time\.Sleep")
    violations: list[str] = []
    for path in files:
        text = path.read_text(encoding="utf-8")
        if forbidden.search(text):
            violations.append(path.relative_to(ROOT).as_posix())
    if violations:
        raise EvidenceFailure(f"host-specific service dependency found: {violations}")
    adapter = ROOT / ADAPTER
    if not adapter.is_file():
        raise EvidenceFailure("deprecated CLI adapter is missing")
    adapter_text = adapter.read_text(encoding="utf-8")
    required = ("Deprecated:", "sessionroomerrors", "NewService", "ParticipantFailure", "ParticipantFailureReason", "FailureResult", "Sanitize")
    missing = [literal for literal in required if literal not in adapter_text]
    if missing:
        raise EvidenceFailure(f"adapter is not an explicit forwarding adapter: {missing}")
    return {"go_files": [path.relative_to(ROOT).as_posix() for path in files], "forbidden_matches": violations, "adapter": ADAPTER}


def common_checks() -> dict[str, Any]:
    admission = load_admission()
    return {
        "admission": check_admission(),
        "branch": git("branch", "--show-current"),
        "ancestry": check_ancestry(),
        "source": check_immutable_source(),
        "scope": check_scope(),
        "exclusions": check_exclusions(admission),
        "shared": check_shared(admission),
        "service": check_service_shape(),
    }


def line_count_at_revision(revision: str, path: str) -> int:
    try:
        return len(git_show(revision, path).splitlines())
    except subprocess.CalledProcessError:
        return 0


def line_count_current(path: str) -> int:
    absolute = ROOT / path
    return len(absolute.read_bytes().splitlines()) if absolute.is_file() else 0


def retirement() -> dict[str, Any]:
    checks = common_checks()
    if (ROOT / LEGACY).exists():
        raise EvidenceFailure("legacy session_room_errors.go still exists")
    baseline = {path: line_count_at_revision(ACCEPTED_MAIN, path) for path in CLI_PRODUCTION_PATHS}
    candidate = {path: line_count_current(path) for path in CLI_PRODUCTION_PATHS}
    retired_lines = sum(baseline.values()) - sum(candidate.values())
    if retired_lines < 70:
        raise EvidenceFailure(f"CLI production retirement is below the 70-line floor: baseline={baseline} candidate={candidate} net={retired_lines}")
    if baseline[LEGACY] <= 0 or candidate[LEGACY] != 0:
        raise EvidenceFailure("legacy production file retirement was not demonstrated")
    adapter_text = (ROOT / ADAPTER).read_text(encoding="utf-8")
    if "roomSafeError" in adapter_text or "redactSelfPlayError" in adapter_text:
        raise EvidenceFailure("adapter retained legacy policy implementation")
    checks["retirement"] = {"baseline_lines": baseline, "candidate_lines": candidate, "net_lines_retired": retired_lines, "legacy_files_retired": 1, "floor": 70}
    return checks


def overlay_test(label: str, source: str, mutation: str, package_cwd: Path, package: str, test_name: str, root: Path) -> dict[str, Any]:
    original = ROOT / source
    text = original.read_text(encoding="utf-8")
    mutated = mutation(text)
    if mutated == text:
        raise EvidenceFailure(f"{label} mutation did not change source")
    with tempfile.TemporaryDirectory(prefix=f"c126-{label}-") as directory:
        replacement = Path(directory) / original.name
        replacement.write_text(mutated, encoding="utf-8")
        overlay = Path(directory) / "overlay.json"
        overlay.write_text(json.dumps({"Replace": {str(original.resolve()): str(replacement.resolve())}}), encoding="utf-8")
        result = run(["go", "test", "-overlay", str(overlay), package, "-run", f"^{test_name}$", "-count=1", "-timeout=120s"], cwd=package_cwd, timeout=150)
    combined = result["stdout"] + "\n" + result["stderr"]
    compiled = result["returncode"] != 1 or "build failed" not in combined.lower()
    failed_oracle = result["returncode"] not in (0, None) and not result["timed_out"] and ("FAIL" in combined or "failed" in combined.lower())
    if not compiled or not failed_oracle:
        raise EvidenceFailure(f"{label} mutant did not fail its causal oracle: {result}")
    return {"label": label, "source": source, "test": test_name, "returncode": result["returncode"], "compiled": compiled, "oracle_failed": failed_oracle, "stdout": result["stdout"], "stderr": result["stderr"]}


def mutations() -> dict[str, Any]:
    checks = common_checks()
    positive = run(["go", "test", "./services/sessionroomerrors/...", "-count=1", "-timeout=180s"], cwd=ROOT / "go-agent-runtime", timeout=210)
    if positive["returncode"] != 0:
        raise EvidenceFailure(f"positive service source failed before mutation testing: {positive}")

    def leak_secret(text: str) -> str:
        old = "\treturn sanitizeText(value, e.secrets)\n"
        return text.replace(old, "\treturn value\n", 1)

    def erase_identity(text: str) -> str:
        old = "func (e *participantFailure) Unwrap() error {\n\tif e == nil {\n\t\treturn nil\n\t}\n\treturn e.cause\n}"
        new = old.replace("\treturn e.cause", "\treturn nil")
        return text.replace(old, new, 1)

    def overwrite_first(text: str) -> str:
        old = "\tif e.recordErr == nil {\n\t\te.recordErr = wrapped\n\t}"
        return text.replace(old, "\tif true {\n\t\te.recordErr = wrapped\n\t}", 1)

    def remove_disconnect_fallback(text: str) -> str:
        return text.replace("\tif request.TransportDisconnected {", "\tif false {", 1)

    controls = [
        overlay_test("leak-secret", f"{SERVICE_ROOT}/internal/service/service.go", leak_secret, ROOT / "go-agent-runtime", "./services/sessionroomerrors/internal/service", "TestParticipantFailureCopiesSecretsAndRedactsNestedCredentials", ROOT),
        overlay_test("erase-cause-identity", f"{SERVICE_ROOT}/internal/service/service.go", erase_identity, ROOT / "go-agent-runtime", "./services/sessionroomerrors/internal/service", "TestParticipantFailurePreservesIdentityAndCause", ROOT),
        overlay_test("overwrite-first-failure", "agent-cli/internal/services/internal/agentruntime/session_room_evidence.go", overwrite_first, ROOT / "agent-cli", "./internal/services/internal/agentruntime", "TestRoomEvidence_RecordingHealthRetainsFirstSanitizedFailure", ROOT),
        overlay_test("remove-disconnect-fallback", f"{SERVICE_ROOT}/internal/service/service.go", remove_disconnect_fallback, ROOT / "go-agent-runtime", "./services/sessionroomerrors/internal/service", "TestParticipantFailureReasonUsesStableFallbackPrecedence", ROOT),
    ]
    checks["positive"] = positive
    checks["mutations"] = controls
    write_report("positive-and-four-mutations.json", checks)
    return checks


def final() -> dict[str, Any]:
    checks = retirement()
    head = git("rev-parse", "HEAD")
    checks["handoff"] = {"candidate_head": head, "ci_polling": "not performed by executor", "review": "not performed by executor", "startup_integration_revision": STARTUP_INTEGRATION, "accepted_main": ACCEPTED_MAIN}
    write_report("final.json", checks)
    return checks


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True, choices=("service-callers-and-exclusions", "positive-and-four-mutations", "retirement-and-owned-paths", "final"))
    parser.add_argument("--base", default=ACCEPTED_MAIN)
    args = parser.parse_args()
    try:
        if args.mode == "service-callers-and-exclusions":
            value = common_checks()
            write_report("service-callers-and-exclusions.json", value)
        elif args.mode == "positive-and-four-mutations":
            value = mutations()
        elif args.mode == "retirement-and-owned-paths":
            value = retirement()
            value["requested_base"] = args.base
            write_report("retirement-and-owned-paths.json", value)
        else:
            value = final()
        print(json.dumps({"status": "passed", "mode": args.mode, "report": str((HERE / "runs").relative_to(ROOT)), "candidate": git("rev-parse", "HEAD")}, sort_keys=True))
        return 0
    except (EvidenceFailure, OSError, subprocess.SubprocessError, json.JSONDecodeError) as error:
        failure = {"status": "failed", "mode": args.mode, "error": redact(str(error)), "candidate": git("rev-parse", "HEAD", check=False)}
        write_report(f"{args.mode}.failure.json", failure)
        print(json.dumps(failure, sort_keys=True), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
