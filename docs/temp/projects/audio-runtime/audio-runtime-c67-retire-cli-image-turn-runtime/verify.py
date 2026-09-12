#!/usr/bin/env python3
"""Bounded C67 image-input service, adapter, and ownership evidence."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import time
from typing import Any, Sequence


HERE = Path(__file__).resolve().parent
ROOT = Path(
    subprocess.run(
        ["git", "rev-parse", "--show-toplevel"],
        cwd=HERE,
        check=True,
        capture_output=True,
        text=True,
    ).stdout.strip()
).resolve()
FACTORY_ROOT = Path(os.environ.get("FACTORY_ROOT", str(ROOT))).resolve()
RUNTIME = ROOT / "go-agent-runtime"
CLI = ROOT / "agent-cli"
CONSUMER = HERE / "consumer"
RUNS = HERE / "runs"

TASK = "audio-runtime-c67-retire-cli-image-turn-runtime"
BRANCH = "codex/audio-runtime-c67-retire-cli-image-turn-runtime"
BASELINE = "d5d6f84363d8569d5dc1a59985f8d45cf50e1d06"
MANIFEST_BASELINE = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
STARTUP = "8bdafc7f947a3a2c9856220abdc539437035bd21"
LEGACY_PATH = "agent-cli/internal/services/internal/agentruntime/session_image.go"
CALLER_PATHS = (
    "agent-cli/internal/services/internal/agentruntime/service.go",
    "agent-cli/internal/services/internal/agentruntime/session_recording.go",
)
ALLOWED_PREFIXES = (
    "agent-cli/internal/services/internal/agentruntime/session_image.go",
    "agent-cli/internal/services/internal/agentruntime/session_image_test.go",
    "agent-cli/internal/services/internal/agentruntime/session_instruction_composition_test.go",
    "go-agent-runtime/services/imageinput/",
    "coverage-manifest/go-agent-runtime/services/imageinput/",
    "docs/temp/projects/audio-runtime/audio-runtime-c67-retire-cli-image-turn-runtime/",
)
FORBIDDEN_IMPORT_PATTERN = r"agent-cli|internal/config|internal/input|pflag|cobra"
OUTPUT_CAP = 1 << 20
CHILD_TIMEOUT = 240
TOTAL_TIMEOUT = 900


class EvidenceFailure(RuntimeError):
    pass


def sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def sha256_file(path: Path) -> str:
    return sha256_bytes(path.read_bytes())


def write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def git(*args: str) -> str:
    result = subprocess.run(
        ["git", "-C", str(ROOT), *args],
        check=False,
        capture_output=True,
        text=True,
        timeout=20,
    )
    if result.returncode != 0:
        raise EvidenceFailure(result.stderr.strip() or f"git {' '.join(args)} failed")
    return result.stdout.strip()


def git_bytes(*args: str) -> bytes:
    result = subprocess.run(
        ["git", "-C", str(ROOT), *args],
        check=False,
        capture_output=True,
        timeout=20,
    )
    if result.returncode != 0:
        raise EvidenceFailure(result.stderr.decode(errors="replace").strip() or f"git {' '.join(args)} failed")
    return result.stdout


def safe_environment() -> dict[str, str]:
    environment = dict(os.environ)
    secret_markers = ("API_KEY", "TOKEN", "SECRET", "PASSWORD", "CREDENTIAL", "OPENAI", "ANTHROPIC")
    for name in list(environment):
        if any(marker in name.upper() for marker in secret_markers):
            del environment[name]
    return environment


def process_group_gone(process_id: int) -> bool:
    try:
        os.killpg(process_id, 0)
    except ProcessLookupError:
        return True
    except PermissionError:
        return False
    return False


def terminate_group(process: subprocess.Popen[str]) -> None:
    if os.name == "nt":
        process.kill()
        return
    try:
        os.killpg(process.pid, signal.SIGTERM)
        process.wait(timeout=2)
    except (ProcessLookupError, subprocess.TimeoutExpired):
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        try:
            process.wait(timeout=2)
        except subprocess.TimeoutExpired:
            process.kill()


class Runner:
    def __init__(self, mode: str) -> None:
        stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
        self.run_dir = RUNS / f"verify-{stamp}-{os.getpid()}"
        self.run_dir.mkdir(parents=True, exist_ok=True)
        self.mode = mode
        self.started = time.monotonic()
        self.steps: list[dict[str, Any]] = []

    def remaining(self) -> float:
        remaining = TOTAL_TIMEOUT - (time.monotonic() - self.started)
        if remaining <= 0:
            raise EvidenceFailure(f"aggregate verifier deadline of {TOTAL_TIMEOUT}s exceeded")
        return min(float(CHILD_TIMEOUT), remaining)

    def command(
        self,
        label: str,
        argv: Sequence[str | Path],
        cwd: Path,
        *,
        expected: int | None = 0,
        extra_env: dict[str, str] | None = None,
    ) -> dict[str, Any]:
        self.remaining()
        command = [str(item) for item in argv]
        environment = safe_environment()
        if extra_env:
            environment.update(extra_env)
        started = time.monotonic()
        process = subprocess.Popen(
            command,
            cwd=cwd,
            env=environment,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            start_new_session=os.name != "nt",
        )
        timed_out = False
        try:
            stdout, stderr = process.communicate(timeout=self.remaining())
        except subprocess.TimeoutExpired as exc:
            timed_out = True
            stdout = exc.stdout or ""
            stderr = exc.stderr or ""
            terminate_group(process)
            stdout, stderr = process.communicate()
        stdout = stdout if isinstance(stdout, str) else stdout.decode(errors="replace")
        stderr = stderr if isinstance(stderr, str) else stderr.decode(errors="replace")
        stdout_bytes = stdout.encode()
        stderr_bytes = stderr.encode()
        safe_label = re.sub(r"[^A-Za-z0-9_.-]", "_", label)
        stdout_path = self.run_dir / f"{len(self.steps) + 1:03d}-{safe_label}.stdout.log"
        stderr_path = self.run_dir / f"{len(self.steps) + 1:03d}-{safe_label}.stderr.log"
        stdout_path.write_bytes(stdout_bytes[:OUTPUT_CAP])
        stderr_path.write_bytes(stderr_bytes[:OUTPUT_CAP])
        record = {
            "label": label,
            "argv": command,
            "cwd": str(cwd),
            "exit_code": process.returncode,
            "expected_exit_code": expected,
            "timed_out": timed_out,
            "stdout_bytes": len(stdout_bytes),
            "stderr_bytes": len(stderr_bytes),
            "stdout_truncated": len(stdout_bytes) > OUTPUT_CAP,
            "stderr_truncated": len(stderr_bytes) > OUTPUT_CAP,
            "stdout_path": str(stdout_path),
            "stderr_path": str(stderr_path),
            "elapsed_seconds": round(time.monotonic() - started, 6),
            "process_group_gone": process_group_gone(process.pid) if os.name != "nt" else process.poll() is not None,
        }
        self.steps.append(record)
        if timed_out or record["stdout_truncated"] or record["stderr_truncated"]:
            raise EvidenceFailure(f"bounded command failed its deadline/output contract: {label}: {record}")
        if (expected is None and process.returncode == 0) or (expected is not None and process.returncode != expected):
            expectation = "a non-zero exit" if expected is None else str(expected)
            raise EvidenceFailure(f"{label} exited {process.returncode}, expected {expectation}; stderr={stderr[-4000:]}")
        return record

    def save(self, outcome: dict[str, Any]) -> Path:
        outcome = {
            "schema": "audio-runtime-c67-evidence/v1",
            "mode": self.mode,
            "candidate_revision": git("rev-parse", "HEAD"),
            "candidate_tree": git("rev-parse", "HEAD^{tree}"),
            "run_dir": str(self.run_dir),
            "steps": self.steps,
            **outcome,
        }
        path = self.run_dir / "outcome.json"
        write_json(path, outcome)
        return path


def line_count(value: bytes) -> int:
    return value.count(b"\n")


def inventory_declarations(source: bytes) -> list[dict[str, Any]]:
    declarations: list[dict[str, Any]] = []
    group_kind: str | None = None
    for number, raw_line in enumerate(source.decode(errors="replace").splitlines(), 1):
        line = raw_line.strip()
        if group_kind:
            if line == ")":
                group_kind = None
                continue
            match = re.match(r"([A-Za-z_]\w*)", line)
            if match:
                declarations.append({"line": number, "kind": group_kind, "name": match.group(1)})
            continue
        match = re.match(r"(func|type|const|var)\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)", line)
        if match:
            declarations.append({"line": number, "kind": match.group(1), "name": match.group(2)})
            if line.endswith("("):
                group_kind = match.group(1)
    return declarations


def baseline_inventory(runner: Runner) -> dict[str, Any]:
    current_branch = git("branch", "--show-current")
    if current_branch != BRANCH:
        raise EvidenceFailure(f"branch {current_branch!r} does not match prd.branchName {BRANCH!r}")
    before = git_bytes("show", f"{BASELINE}:{LEGACY_PATH}")
    after = (ROOT / LEGACY_PATH).read_bytes()
    before_lines = line_count(before)
    after_lines = line_count(after)
    if before_lines != 703:
        raise EvidenceFailure(f"accepted-main legacy baseline is {before_lines} lines, not 703")
    if after_lines > 453 or before_lines - after_lines < 250:
        raise EvidenceFailure(f"legacy retirement floor failed: {before_lines} -> {after_lines}")
    callers = {
        path: {
            "baseline_sha256": sha256_bytes(git_bytes("show", f"{BASELINE}:{path}")),
            "candidate_sha256": sha256_file(ROOT / path),
            "unchanged": git("diff", "--quiet", BASELINE, "--", path) == "",
        }
        for path in CALLER_PATHS
    }
    if not all(item["unchanged"] for item in callers.values()):
        raise EvidenceFailure(f"byte-identical caller guard failed: {callers}")
    declarations = inventory_declarations(before)
    caller_matches = []
    for symbol in ("PrepareSessionImageParts", "SendSessionImageTurn", "sessionImageInferencer", "sessionImageContent"):
        result = subprocess.run(
            ["git", "-C", str(ROOT), "grep", "-n", symbol, BASELINE, "--", "agent-cli"],
            check=False,
            capture_output=True,
            text=True,
            timeout=20,
        )
        caller_matches.extend(result.stdout.splitlines())
    report = {
        "baseline_revision": BASELINE,
        "manifest_baseline_revision": MANIFEST_BASELINE,
        "startup_integration_revision": STARTUP,
        "current_revision": git("rev-parse", "HEAD"),
        "branch": current_branch,
        "worktree": str(ROOT),
        "legacy_path": LEGACY_PATH,
        "baseline_lines": before_lines,
        "final_lines": after_lines,
        "retired_lines": before_lines - after_lines,
        "baseline_sha256": sha256_bytes(before),
        "final_sha256": sha256_bytes(after),
        "declarations": declarations,
        "baseline_caller_matches": caller_matches,
        "caller_guards": callers,
        "retirement_credit": "legacy production file only; tests/evidence/generated/new runtime/peer files excluded",
    }
    write_json(runner.run_dir / "baseline-inventory.json", report)
    return report


def ancestry_report() -> dict[str, Any]:
    revisions = {
        "manifest_baseline": MANIFEST_BASELINE,
        "startup_integration": STARTUP,
        "planning_and_fetched_origin_main": BASELINE,
    }
    result: dict[str, Any] = {}
    for name, revision in revisions.items():
        probe = subprocess.run(
            ["git", "-C", str(ROOT), "merge-base", "--is-ancestor", revision, "HEAD"],
            check=False,
            timeout=20,
        )
        result[name] = {"revision": revision, "ancestor": probe.returncode == 0}
        if probe.returncode != 0:
            raise EvidenceFailure(f"required ancestry missing: {name}={revision}")
    return result


def verify_admission(runner: Runner) -> dict[str, Any]:
    record = runner.command(
        "verify-admission",
        [
            sys.executable,
            str(FACTORY_ROOT / "factory/scripts/project-control.py"),
            "verify-work",
            "--type",
            "task",
            "--name",
            TASK,
            "--root",
            str(FACTORY_ROOT),
        ],
        ROOT,
    )
    output = Path(record["stdout_path"]).read_text(encoding="utf-8")
    try:
        value = json.loads(output)
    except json.JSONDecodeError as exc:
        raise EvidenceFailure(f"admission did not return JSON: {output!r}") from exc
    if value.get("status") != "admitted" or value.get("project") != "audio-runtime":
        raise EvidenceFailure(f"unexpected admission: {value}")
    return value


def mode_baseline(runner: Runner) -> dict[str, Any]:
    admission = verify_admission(runner)
    board = runner.command(
        "canonical-board",
        [
            "you",
            "--server",
            os.environ.get("FACTORY_SERVER_URL", ""),
            "--json",
            "work",
            "list",
            "--session",
            "~default",
            "--name",
            "audio-runtime-c67",
            "--max-results",
            "500",
            "--all",
        ],
        FACTORY_ROOT,
    )
    board_text = Path(board["stdout_path"]).read_text(encoding="utf-8")
    if TASK not in board_text or BRANCH not in board_text:
        raise EvidenceFailure("canonical board capture did not include the admitted C67 task and branch")
    return {"admission": admission, "canonical_board": board, "ancestry": ancestry_report(), "inventory": baseline_inventory(runner)}


def mode_publication(runner: Runner) -> dict[str, Any]:
    runtime_env = {"GOWORK": "off"}
    steps = {
        "service_tests": runner.command("service-tests", ["go", "test", "-count=1", "-timeout=120s", "./services/imageinput/..."], RUNTIME, extra_env=runtime_env),
        "service_race": runner.command("service-race", ["go", "test", "-race", "-count=1", "-timeout=180s", "./services/imageinput/..."], RUNTIME, extra_env=runtime_env),
        "cli_tests": runner.command("cli-image-regressions", ["go", "test", "-count=1", "-timeout=180s", "./internal/services/internal/agentruntime", "-run", "SessionImage|Image.*Audio|RecordingDirectory|ReadImage|InstructionComposition"], CLI, extra_env=runtime_env),
        "cli_race": runner.command("cli-image-race", ["go", "test", "-race", "-count=1", "-timeout=240s", "./internal/services/internal/agentruntime", "-run", "SessionImage|Image.*Audio|ReadImage|InstructionComposition"], CLI, extra_env=runtime_env),
        "consumer_tests": runner.command("consumer-tests", ["go", "test", "-count=1", "-timeout=120s", "./..."], CONSUMER, extra_env=runtime_env),
        "consumer_positive": runner.command("consumer-positive", ["go", "run", ".", "positive"], CONSUMER, extra_env=runtime_env),
        "consumer_negative": runner.command("consumer-negative", ["go", "run", ".", "negative"], CONSUMER, extra_env=runtime_env),
    }
    wrong = runner.command("consumer-wrong-oracle", ["go", "run", ".", "wrong-oracle"], CONSUMER, expected=1, extra_env={**runtime_env, "C67_WRONG_ORACLE": "1"})
    wrong_text = Path(wrong["stderr_path"]).read_text(encoding="utf-8")
    if "wrong oracle expected" not in wrong_text:
        raise EvidenceFailure("wrong-oracle control did not fail at the intended assertion")
    forbidden = runner.command("forbidden-import-scan", ["rg", "-n", FORBIDDEN_IMPORT_PATTERN, "services/imageinput"], RUNTIME, expected=1, extra_env=runtime_env)
    generated = runner.command("wire-generate", ["go", "generate", "./..."], RUNTIME / "services/imageinput/wire", extra_env=runtime_env)
    drift = runner.command("wire-local-drift", ["git", "diff", "--exit-code", "--", "go-agent-runtime/services/imageinput/wire/wire_gen.go"], ROOT)
    return {"focused_and_consumer": steps, "wrong_oracle": wrong, "forbidden_import_scan": forbidden, "wire_generation": generated, "wire_drift": drift}


def mode_adapter(runner: Runner) -> dict[str, Any]:
    baseline = baseline_inventory(runner)
    runtime_env = {"GOWORK": "off"}
    selected = [
        LEGACY_PATH,
        "agent-cli/internal/services/internal/agentruntime/session_image_test.go",
        "agent-cli/internal/services/internal/agentruntime/session_instruction_composition_test.go",
        "go-agent-runtime/services/imageinput/contract.go",
        "go-agent-runtime/services/imageinput/contract_test.go",
        "go-agent-runtime/services/imageinput/internal/service/prepare.go",
        "go-agent-runtime/services/imageinput/internal/service/prepare_test.go",
        "go-agent-runtime/services/imageinput/internal/service/publish.go",
        "go-agent-runtime/services/imageinput/internal/service/publish_test.go",
        "go-agent-runtime/services/imageinput/internal/service/service.go",
        "go-agent-runtime/services/imageinput/wire/providers.go",
        "go-agent-runtime/services/imageinput/wire/wire_gen.go",
        "go-agent-runtime/services/imageinput/wire/wire_test.go",
    ]
    commands = {
        "gofmt": runner.command("gofmt", ["gofmt", "-d", *selected], ROOT),
        "runtime_vet": runner.command("runtime-vet", ["go", "vet", "./services/imageinput/..."], RUNTIME, extra_env=runtime_env),
        "coverage_registration": runner.command("coverage-registration", ["make", "coverage-registration"], ROOT, extra_env=runtime_env),
    }
    return {"baseline": baseline, "quality_commands": commands}


def scope_report() -> dict[str, Any]:
    changed = [path for path in git("diff", "--name-only", f"{BASELINE}...HEAD").splitlines() if path]
    outside = [path for path in changed if not any(path == prefix or path.startswith(prefix) for prefix in ALLOWED_PREFIXES)]
    if outside:
        raise EvidenceFailure(f"changed paths outside admitted ownedPaths: {outside}")
    return {
        "changed_paths": changed,
        "outside_owned_paths": outside,
        "caller_paths_unchanged": {path: git("diff", "--quiet", BASELINE, "--", path) == "" for path in CALLER_PATHS},
        "deferred_shared_paths": [
            "scripts/wire-packages.txt",
            "docs/architecture/architecture-size-baseline.json",
        ],
        "ci_review_probe_claimed": False,
    }


def mode_final(runner: Runner) -> dict[str, Any]:
    scope = scope_report()
    commands: dict[str, Any] = {}
    # These checks are intentionally evidence, not a claim that shared gates are green.
    commands["diff_check"] = runner.command("diff-check", ["git", "diff", "--check"], ROOT)
    commands["wire_registry_dependency"] = runner.command(
        "wire-registry-dependency",
        [sys.executable, str(ROOT / "scripts/check-wire.py"), "--go", "go", "agent-cli", "go-agent-runtime", "go-agent-loop", "go-audio", "go-device-gateway", "go-llm-gateway"],
        ROOT,
        expected=1,
    )
    wire_text = Path(commands["wire_registry_dependency"]["stderr_path"]).read_text(encoding="utf-8")
    if "imageinput" not in wire_text and "unregistered" not in wire_text:
        raise EvidenceFailure("Wire-registry dependency did not identify the new imageinput graph")
    commands["architecture_dependency"] = runner.command("architecture-dependency", ["make", "verify-architecture"], ROOT, expected=None)
    architecture_text = Path(commands["architecture_dependency"]["stdout_path"]).read_text(encoding="utf-8") + Path(commands["architecture_dependency"]["stderr_path"]).read_text(encoding="utf-8")
    return {
        "scope": scope,
        "shared_gate_status": "BLOCKED_PENDING_OWNER_MEDIATED_REGISTRY_AND_BASELINE_RECONCILIATION",
        "shared_gate_commands": commands,
        "shared_gate_diagnostic": architecture_text[-12000:],
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("baseline-inventory-oracles", "publication-negative-mutations", "adapter-retirement-scope", "final-scope-provenance"), required=True)
    args = parser.parse_args()
    runner = Runner(args.mode)
    try:
        if args.mode == "baseline-inventory-oracles":
            outcome = mode_baseline(runner)
        elif args.mode == "publication-negative-mutations":
            outcome = mode_publication(runner)
        elif args.mode == "adapter-retirement-scope":
            outcome = mode_adapter(runner)
        else:
            outcome = mode_final(runner)
        path = runner.save({"decision": "PASS" if args.mode != "final-scope-provenance" else "CONTINUE", **outcome})
        print(json.dumps({"decision": "PASS" if args.mode != "final-scope-provenance" else "CONTINUE", "mode": args.mode, "report": str(path)}, sort_keys=True))
        return 0
    except (EvidenceFailure, OSError, subprocess.SubprocessError) as exc:
        path = runner.save({"decision": "FAIL", "error": str(exc)})
        print(json.dumps({"decision": "FAIL", "mode": args.mode, "report": str(path), "error": str(exc)}), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
