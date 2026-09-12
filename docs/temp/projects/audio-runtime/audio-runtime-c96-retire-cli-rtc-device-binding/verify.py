#!/usr/bin/env python3
"""Verify C96 causal controls, retirement, identity, and owned scope."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile


TASK_ROOT = Path(__file__).resolve().parent
REPO_ROOT = Path(subprocess.check_output(["git", "rev-parse", "--show-toplevel"], cwd=TASK_ROOT, text=True).strip())
RUNTIME = REPO_ROOT / "go-agent-runtime"
CONSUMER = TASK_ROOT / "external-consumer"
LEGACY = REPO_ROOT / "agent-cli/internal/services/internal/agentruntime/rtc_device_binding.go"
BASE_REVISION = "3d3e72786ac6fc1fd47c7e029589e5117674b035"
STARTUP_REVISION = "8bdafc7f947a3a2c9856220abdc539437035bd21"
EXPECTED_BRANCH = "codex/audio-runtime-c96-retire-cli-rtc-device-binding"
EXPECTED_SOURCE_LINES = 303
EXPECTED_SOURCE_SHA256 = "0c0a72c306f748535d8d35ff2c019d1ac1c8408b7f22dff46814e2abf0a57bc1"
OWNED_PREFIXES = (
    "agent-cli/internal/services/internal/agentruntime/rtc_device_binding.go",
    "agent-cli/internal/services/internal/agentruntime/rtc_device_binding_test.go",
    "go-agent-runtime/services/devicebinding/",
    "coverage-manifest/go-agent-runtime/services/devicebinding/",
    "docs/temp/projects/audio-runtime/audio-runtime-c96-retire-cli-rtc-device-binding/",
)
EXCLUDED_PREFIXES = (
    "agent-cli/internal/services/internal/agentruntime/rtc_device_runtime.go",
    "agent-cli/internal/services/internal/agentruntime/session_drain.go",
    "agent-cli/internal/services/internal/agentruntime/rtc_device_runtime_test.go",
    "agent-cli/internal/services/internal/agentruntime/session_drain_test.go",
    "agent-cli/internal/services/internal/agentruntime/session_audio_out.go",
    "agent-cli/internal/services/internal/agentruntime/session_audio_out_test.go",
    "scripts/wire-packages.txt",
    "docs/architecture/architecture-size-baseline.json",
)


class VerificationError(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise VerificationError(message)


def environment() -> dict[str, str]:
    result = os.environ.copy()
    result["GOWORK"] = "off"
    return result


def run(argv: list[str], cwd: Path, timeout: int = 240) -> dict[str, object]:
    try:
        result = subprocess.run(
            argv,
            cwd=cwd,
            env=environment(),
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
            timeout=timeout,
        )
    except subprocess.TimeoutExpired as error:
        raise VerificationError(f"timed out: {argv}") from error
    return {
        "argv": argv,
        "cwd": str(cwd),
        "returncode": result.returncode,
        "output_tail": result.stdout[-4000:],
    }


def git(*argv: str) -> str:
    return subprocess.check_output(["git", *argv], cwd=REPO_ROOT, text=True).strip()


def source_at(revision: str, relative: str) -> bytes:
    return subprocess.check_output(["git", "show", f"{revision}:{relative}"], cwd=REPO_ROOT)


def source_facts() -> dict[str, object]:
    content = source_at(BASE_REVISION, "agent-cli/internal/services/internal/agentruntime/rtc_device_binding.go")
    return {
        "lines": len(content.splitlines()),
        "sha256": hashlib.sha256(content).hexdigest(),
    }


def changed_paths() -> list[str]:
    output = git("diff", "--name-only", "origin/main...HEAD")
    return [line for line in output.splitlines() if line]


def verify_identity() -> dict[str, object]:
    prd = json.loads((REPO_ROOT / "prd.json").read_text(encoding="utf-8"))
    facts = source_facts()
    origin_main = git("rev-parse", "origin/main")
    require(git("branch", "--show-current") == EXPECTED_BRANCH, "candidate branch does not match PRD")
    require(prd.get("branchName") == EXPECTED_BRANCH, "prd.json branchName changed")
    require(facts == {"lines": EXPECTED_SOURCE_LINES, "sha256": EXPECTED_SOURCE_SHA256}, "immutable legacy source facts changed")
    require(subprocess.run(["git", "merge-base", "--is-ancestor", STARTUP_REVISION, "HEAD"], cwd=REPO_ROOT).returncode == 0, "startup ancestry missing")
    require(subprocess.run(["git", "merge-base", "--is-ancestor", BASE_REVISION, "HEAD"], cwd=REPO_ROOT).returncode == 0, "accepted-main ancestry missing")
    require(subprocess.run(["git", "merge-base", "--is-ancestor", origin_main, "HEAD"], cwd=REPO_ROOT).returncode == 0, "fresh origin/main ancestry missing")
    return {
        "branch": EXPECTED_BRANCH,
        "candidate_revision": git("rev-parse", "HEAD"),
        "origin_main": origin_main,
        "startup_revision": STARTUP_REVISION,
        "accepted_main_revision": BASE_REVISION,
        "immutable_source": facts,
    }


def verify_public_boundary() -> dict[str, object]:
    source = (RUNTIME / "services/devicebinding/contract.go").read_text(encoding="utf-8")
    require("agent-cli" not in source, "public contract imports the CLI")
    require("os." not in source and "flag." not in source and "terminal" not in source.lower(), "public contract reaches host state")
    imports = []
    for path in (RUNTIME / "services/devicebinding").glob("*.go"):
        imports.extend(line.strip() for line in path.read_text(encoding="utf-8").splitlines() if '"' in line and line.strip().startswith('"'))
    require(not any("agent-cli" in item or "/internal/" in item for item in imports), "public contract has a forbidden direct import")
    return {"public_contract_files": sorted(path.name for path in (RUNTIME / "services/devicebinding").glob("*.go")), "forbidden_imports": []}


def verify_consumer() -> dict[str, object]:
    result = run(["go", "test", "./...", "-count=1", "-timeout=120s"], CONSUMER, timeout=180)
    require(result["returncode"] == 0, f"external consumer failed: {result}")
    main = (CONSUMER / "main.go").read_text(encoding="utf-8")
    direct = [line.strip() for line in main.splitlines() if line.strip().startswith('"')]
    require(not any("agent-cli" in item or "/internal/" in item for item in direct), "consumer imports an internal or CLI package")
    return {"test": result, "direct_imports": direct}


def verify_positive() -> dict[str, object]:
    results = [
        run(["go", "test", "./services/devicebinding/...", "-count=1", "-timeout=240s"], RUNTIME),
        run(["go", "test", "./internal/services/internal/agentruntime", "-run", "^TestRTCDevice|^TestPrepareRTCDevice|^TestSessionAudioDevice", "-count=1", "-timeout=240s"], REPO_ROOT / "agent-cli"),
    ]
    for result in results:
        require(result["returncode"] == 0, f"positive focused test failed: {result}")
    return {"focused_tests": results, "consumer": verify_consumer()}


def mutate_and_reject(label: str, relative: str, old: str, new: str, test_package: Path, test_name: str) -> dict[str, object]:
    with tempfile.TemporaryDirectory(prefix=f"audio-runtime-c96-{label}-") as temporary:
        worktree = Path(temporary) / "candidate"
        added = False
        try:
            add = run(["git", "worktree", "add", "--detach", str(worktree), "HEAD"], REPO_ROOT, timeout=120)
            require(add["returncode"] == 0, f"could not create mutation worktree {label}: {add}")
            added = True
            path = worktree / relative
            content = path.read_text(encoding="utf-8")
            require(content.count(old) == 1, f"mutation {label} source anchor is not unique")
            path.write_text(content.replace(old, new, 1), encoding="utf-8")
            package_arg = test_package.as_posix()
            if not package_arg.startswith("."):
                package_arg = f"./{package_arg}"
            result = run(["go", "test", package_arg, "-run", f"^{test_name}$", "-count=1", "-timeout=120s"], worktree / "go-agent-runtime", timeout=180)
            require(result["returncode"] != 0, f"mutation {label} unexpectedly passed: {result}")
            require(test_name in str(result["output_tail"]), f"mutation {label} failed outside its intended oracle: {result}")
            return {"mutation": label, "returncode": result["returncode"], "oracle": test_name, "output_tail": result["output_tail"]}
        finally:
            if added:
                subprocess.run(["git", "worktree", "remove", "--force", str(worktree)], cwd=REPO_ROOT, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)


def verify_mutations() -> dict[str, object]:
    controls = [
        mutate_and_reject(
            "collapse-selector",
            "go-agent-runtime/services/devicebinding/contract.go",
            'func (r Request) InputSelected() bool { return r.InputPresent || r.InputDevice != "" }',
            'func (r Request) InputSelected() bool { return r.InputDevice != "" }',
            Path("./services/devicebinding"),
            "TestRequestSelectionUsesPresenceAndOpaqueIDs",
        ),
        mutate_and_reject(
            "leak-input",
            "go-agent-runtime/services/devicebinding/internal/service/service.go",
            'return nil, nil, errors.Join(err, binding.Close())',
            'return nil, nil, err',
            Path("./services/devicebinding/internal/service"),
            "TestOpenOutputFailureReturnsTypedErrorAndRollsBackInput",
        ),
        mutate_and_reject(
            "false-render-support",
            "go-agent-runtime/services/devicebinding/internal/service/service.go",
            'request.RenderedSamplesObserver != nil && !sink.SetRenderedSamplesObserver(request.RenderedSamplesObserver) && request.RenderedSamplesUnavailable != nil',
            'request.RenderedSamplesObserver != nil && sink.SetRenderedSamplesObserver(request.RenderedSamplesObserver) && request.RenderedSamplesUnavailable != nil',
            Path("./services/devicebinding/internal/service"),
            "TestOpenReportsUnavailableRenderBoundary",
        ),
        mutate_and_reject(
            "reverse-shutdown",
            "go-agent-runtime/services/devicebinding/contract.go",
            'if b.Sink != nil {\n\t\t\tsinkErr = b.Sink.Close()\n\t\t}\n\t\tif b.Source != nil {\n\t\t\tsourceErr = b.Source.Close()\n\t\t}',
            'if b.Source != nil {\n\t\t\tsourceErr = b.Source.Close()\n\t\t}\n\t\tif b.Sink != nil {\n\t\t\tsinkErr = b.Sink.Close()\n\t\t}',
            Path("./services/devicebinding"),
            "TestBindingCloseOrdersSinkSourceFeedbackAndJoinsErrors",
        ),
    ]
    return {"count": len(controls), "rejected": controls}


def verify_retirement() -> dict[str, object]:
    current_lines = len(LEGACY.read_text(encoding="utf-8").splitlines())
    changed = changed_paths()
    require(current_lines <= 83, f"legacy binding is {current_lines} lines")
    require(EXPECTED_SOURCE_LINES - current_lines >= 220, "legacy retirement floor is not met")
    require(all(any(path == prefix or path.startswith(prefix) for prefix in OWNED_PREFIXES) for path in changed), f"out-of-scope changed path: {changed}")
    return {"legacy_lines": current_lines, "retired_lines": EXPECTED_SOURCE_LINES - current_lines, "changed_paths": changed}


def verify_exclusions() -> dict[str, object]:
    changed = changed_paths()
    excluded = [path for path in changed if any(path == prefix or path.startswith(prefix) for prefix in EXCLUDED_PREFIXES)]
    require(not excluded, f"excluded path changed: {excluded}")
    return {"excluded_paths_changed": excluded}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True, choices=("positive-and-four-mutations", "retirement-and-owned-paths", "excluded-path-byte-check"))
    args = parser.parse_args()
    try:
        if args.mode == "positive-and-four-mutations":
            report = {"identity": verify_identity(), "boundary": verify_public_boundary(), "positive": verify_positive(), "mutations": verify_mutations()}
        elif args.mode == "retirement-and-owned-paths":
            report = {"identity": verify_identity(), "retirement": verify_retirement(), "boundary": verify_public_boundary()}
        else:
            report = {"identity": verify_identity(), "exclusions": verify_exclusions(), "source": source_facts()}
        print(json.dumps({"status": "accepted", "mode": args.mode, "report": report}, sort_keys=True))
        return 0
    except (OSError, subprocess.CalledProcessError, VerificationError) as error:
        print(json.dumps({"status": "failed", "mode": args.mode, "error": str(error)}, sort_keys=True))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
