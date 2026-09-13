#!/usr/bin/env python3
"""C103 causal mutation and retirement checks."""

from __future__ import annotations

import argparse
import hashlib
import json
import subprocess
import sys
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[5]
CLI_FILE = ROOT / "agent-cli/internal/services/internal/agentruntime/session_runtime_rtc.go"
RUNTIME_FILE = ROOT / "go-agent-runtime/services/rtcsession/internal/service/runtime.go"
BASELINE = "d4766c3dbbf2c198142047ead4449d58dd47d485"
BASELINE_SOURCE = "agent-cli/internal/services/internal/agentruntime/session_runtime_rtc.go"


def run(command: list[str], *, cwd: Path = ROOT) -> subprocess.CompletedProcess[str]:
    return subprocess.run(command, cwd=cwd, text=True, capture_output=True, check=False)


def show_result(label: str, result: subprocess.CompletedProcess[str]) -> None:
    print(f"[{label}] exit={result.returncode}")
    if result.stdout:
        print(result.stdout, end="")
    if result.stderr:
        print(result.stderr, end="", file=sys.stderr)


def assert_positive() -> None:
    result = run([
        "go", "test", "./go-agent-runtime/services/rtcsession/internal/service",
        "-run", "TestRuntime|TestInferencer|TestLazy",
        "-count=1", "-timeout=240s",
    ])
    show_result("positive-control", result)
    if result.returncode != 0:
        raise RuntimeError("positive service lifecycle control failed")


def run_mutant(name: str, replacement: str, marker: str) -> None:
    original = RUNTIME_FILE.read_text()
    with tempfile.TemporaryDirectory(prefix=f"c103-{name}-") as directory:
        mutant = Path(directory) / "runtime.go"
        mutant.write_text(replacement)
        overlay = Path(directory) / "overlay.json"
        overlay.write_text(json.dumps({"Replace": {str(RUNTIME_FILE): str(mutant)}}))
        result = run([
            "go", "test", "-overlay", str(overlay),
            "./go-agent-runtime/services/rtcsession/internal/service",
            "-run", "TestRuntimePartialStart.*",
            "-count=1", "-timeout=120s",
        ])
        show_result(f"mutant-{name}", result)
        output = result.stdout + result.stderr
        if result.returncode == 0:
            raise RuntimeError(f"{name} mutant unexpectedly passed")
        if marker not in output:
            raise RuntimeError(f"{name} mutant did not fail its intended semantic oracle")
    if RUNTIME_FILE.read_text() != original:
        raise RuntimeError(f"{name} mutation did not remain isolated")


def positive_and_mutations() -> None:
    assert_positive()
    original = RUNTIME_FILE.read_text()
    reverse = original.replace(
        "return errors.Join(closeResource(media), closeResource(dataPlane), closeResource(signaling))",
        "return errors.Join(closeResource(signaling), closeResource(dataPlane), closeResource(media))",
        1,
    )
    if reverse == original:
        raise RuntimeError("reverse-cleanup mutation target was not found")
    run_mutant("reverse-cleanup", reverse, "cleanup events =")

    masked = original.replace(
        "return &rtcsession.SessionRTCRuntimeError{Phase: phase, Err: err}",
        "return err",
        1,
    )
    if masked == original:
        raise RuntimeError("typed-error mutation target was not found")
    run_mutant("masked-typed-error", masked, "want phase")
    print("causal mutation proof: positive control passed; both compiling mutants failed intended oracles")


def git_hash(path: str) -> str:
	try:
		return hashlib.sha256((ROOT / path).read_bytes()).hexdigest()
	except OSError as error:
		raise RuntimeError(f"could not hash {path}: {error}")


def retirement_and_owned_paths() -> None:
    line_count = len(CLI_FILE.read_text().splitlines())
    if line_count > 264:
        raise RuntimeError(f"retired CLI file has {line_count} lines; limit is 264")
    source = CLI_FILE.read_text()
    forbidden = (
        "sessionComposedRTCRuntime",
        "sessionRTCRuntimeInferencer",
        "sessionRTCRuntimeSession",
        "sessionRTCLazyDialer",
        "func init()",
        "os.Getenv",
        "os.LookupEnv",
        "time.Sleep",
    )
    found = [token for token in forbidden if token in source]
    if found:
        raise RuntimeError(f"retired CLI file still owns forbidden lifecycle symbols: {found}")
    if source.count("Deprecated:") < 2:
        raise RuntimeError("retired CLI compatibility aliases/adapters lack explicit deprecation")

    baseline = run(["git", "show", f"{BASELINE}:{BASELINE_SOURCE}"])
    if baseline.returncode != 0:
        raise RuntimeError(f"immutable baseline unavailable: {baseline.stderr}")
    baseline_bytes = baseline.stdout.encode()
    if len(baseline_bytes.splitlines()) != 624:
        raise RuntimeError("immutable source baseline line count changed")
    if hashlib.sha256(baseline_bytes).hexdigest() != "7f881c42da476b6d072ac91ec0135814754092856daa1a6c41643903552d9abc":
        raise RuntimeError("immutable source baseline hash changed")

    protected = {
        "agent-cli/internal/services/internal/agentruntime/rtc_device_binding.go": "0c0a72c306f748535d8d35ff2c019d1ac1c8408b7f22dff46814e2abf0a57bc1",
        "agent-cli/internal/services/internal/agentruntime/session_runtime_openai.go": "499e9c92856e94192d8e462619deeb4d15246190d2b14effa09502c21ddb8198",
        "agent-cli/internal/services/internal/agentruntime/session_runtime_grok.go": "864fa8b8a3f889d883733a4c1b202e0630d0155ce34f03f32beb712085f788d5",
        "agent-cli/internal/services/internal/agentruntime/session_room_run.go": "2ac0ff50ee59a1ec8f04682e04d876d270146ecda8245c5d14d28a5f7120e8d6",
        "agent-cli/internal/services/internal/agentruntime/session_diagnostics.go": "0584e3acc16e29b8fb79ece8a19199f0f8690de39099af922f08781edd957eb8",
        "agent-cli/internal/services/internal/agentruntime/session_diagnostics_response.go": "6d9b24f1e134737f1187c1e4b76390a588be200c2c560aff5e99dbff6a3d2f54",
        "agent-cli/internal/services/internal/agentruntime/session_runtime_plan.go": "dd9c598e470816f4f5ec18c97059c55a23b04a0edcf39c94374433db1f70d33b",
        "agent-cli/internal/services/internal/agentruntime/service.go": "d3a1650dfb0a50b8cfcdf9376422d1e054f747a00e4b1951753cff6f9fca82b0",
        "scripts/wire-packages.txt": "3958443ed08ad6a550fd29ce8191c026d1a4017339e00c0b7406e0dcec7de05b",
        "docs/architecture/architecture-size-baseline.json": "6d61448b83eca374b324335f6a9e2fac768969d073b1091d17d48d45bf3badd1",
    }
    for path, expected in protected.items():
        got = git_hash(path)
        if got != expected:
            raise RuntimeError(f"protected peer changed: {path} ({got} != {expected})")
    print(f"retirement proof: {line_count} CLI lines; immutable baseline and {len(protected)} protected paths verified")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("positive-and-two-mutations", "retirement-and-owned-paths"), required=True)
    args = parser.parse_args()
    try:
        if args.mode == "positive-and-two-mutations":
            positive_and_mutations()
        else:
            retirement_and_owned_paths()
    except RuntimeError as error:
        print(f"verification failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
