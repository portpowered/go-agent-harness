#!/usr/bin/env python3
"""Run the unchanged credential-free audio/tool replay oracle on one binary."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import time
from typing import Any


HERE = Path(__file__).resolve().parent
REPO_ROOT = HERE.parents[4]
ORIGINAL_VERIFIER = REPO_ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c18-public-strict-replay/verify-public.py"
DEFAULT_FIXTURE = (
    Path(os.environ.get("FACTORY_ROOT", REPO_ROOT)).resolve()
    / "docs/temp/probes/audio-runtime-c11-hermetic-profile-vertical-probe-stage4/evidence/audio-tool-pass"
)
MAX_TOTAL_SECONDS = 600.0
MAX_CHILD_SECONDS = 50.0
MAX_OUTPUT_BYTES = 64 * 1024


class EvidenceFailure(RuntimeError):
    pass


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def fixture_hashes(root: Path) -> dict[str, str]:
    return {
        str(path.relative_to(root)): sha256_file(path)
        for path in sorted(root.rglob("*"))
        if path.is_file()
    }


def clipped(value: str, limit: int = MAX_OUTPUT_BYTES) -> str:
    if len(value.encode("utf-8")) <= limit:
        return value
    encoded = value.encode("utf-8")[:limit]
    return encoded.decode("utf-8", errors="replace") + "\n...[clipped]"


def run_case(
    case: str,
    binary: Path,
    fixture: Path,
    evidence_path: Path,
    deadline: float,
    timeout: float,
) -> dict[str, Any]:
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise EvidenceFailure("aggregate replay deadline exceeded before " + case)
    child_timeout = min(timeout, MAX_CHILD_SECONDS, remaining)
    command = [
        sys.executable,
        str(ORIGINAL_VERIFIER),
        "--case",
        case,
        "--yui",
        str(binary),
        "--fixture",
        str(fixture),
        "--evidence",
        str(evidence_path),
        "--timeout",
        f"{child_timeout:.3f}",
    ]
    environment = dict(os.environ)
    environment.pop("OPENAI_API_KEY", None)
    environment.pop("OPENAI_BASE_URL", None)
    started = time.monotonic()
    try:
        result = subprocess.run(
            command,
            cwd=REPO_ROOT,
            env=environment,
            capture_output=True,
            text=True,
            timeout=remaining,
            check=False,
        )
    except subprocess.TimeoutExpired as exc:
        raise EvidenceFailure(f"{case} verifier exceeded its bounded deadline") from exc
    output = {
        "case": case,
        "command": command,
        "cwd": str(REPO_ROOT),
        "duration_seconds": round(time.monotonic() - started, 6),
        "exit_code": result.returncode,
        "stdout": clipped(result.stdout or ""),
        "stderr": clipped(result.stderr or ""),
        "evidence_path": str(evidence_path),
    }
    if result.returncode != 0:
        raise EvidenceFailure(f"{case} replay verifier failed; see {evidence_path}")
    report = json.loads(evidence_path.read_text(encoding="utf-8"))
    if report.get("status") != "passed":
        raise EvidenceFailure(f"{case} replay report did not pass: {evidence_path}")
    output["report"] = report
    return output


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--fixture", type=Path, default=DEFAULT_FIXTURE)
    parser.add_argument("--timeout", type=float, default=MAX_CHILD_SECONDS)
    parser.add_argument("--total-timeout", type=float, default=MAX_TOTAL_SECONDS)
    args = parser.parse_args()
    if args.timeout <= 0 or args.timeout > MAX_CHILD_SECONDS:
        raise EvidenceFailure(f"--timeout must be within 0 < timeout <= {MAX_CHILD_SECONDS:g}")
    if args.total_timeout <= 0 or args.total_timeout > MAX_TOTAL_SECONDS:
        raise EvidenceFailure(f"--total-timeout must be within 0 < timeout <= {MAX_TOTAL_SECONDS:g}")
    binary = args.binary.expanduser().resolve()
    fixture = args.fixture.expanduser().resolve()
    if not binary.is_file():
        raise EvidenceFailure(f"binary is unavailable: {binary}")
    if not ORIGINAL_VERIFIER.is_file():
        raise EvidenceFailure(f"original replay verifier is unavailable: {ORIGINAL_VERIFIER}")
    if not (fixture / "config").is_dir() or not (fixture / "run/bundle").is_dir():
        raise EvidenceFailure(f"fixture is not a complete replay fixture: {fixture}")

    before = fixture_hashes(fixture)
    source_revision = subprocess.run(
        ["git", "rev-parse", "HEAD"], cwd=REPO_ROOT, check=True, capture_output=True, text=True
    ).stdout.strip()
    run_dir = HERE / "runs" / f"replay-{source_revision[:12]}-{os.getpid()}"
    run_dir.mkdir(parents=True, exist_ok=True)
    deadline = time.monotonic() + args.total_timeout
    cases = [
        run_case("provider", binary, fixture, run_dir / "provider.json", deadline, args.timeout),
        run_case("strict", binary, fixture, run_dir / "strict.json", deadline, args.timeout),
    ]
    after = fixture_hashes(fixture)
    if before != after:
        raise EvidenceFailure("unchanged replay fixture was modified")
    report = {
        "status": "passed",
        "source_revision": source_revision,
        "binary": {"path": str(binary), "bytes": binary.stat().st_size, "sha256": sha256_file(binary)},
        "fixture": {"path": str(fixture), "before": before, "after": after},
        "credentials_removed": True,
        "realtime_sessions": 0,
        "bounded": {"child_seconds": args.timeout, "aggregate_seconds": args.total_timeout},
        "cases": cases,
    }
    report_path = run_dir / "report.json"
    report_path.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"status": "passed", "report": str(report_path)}, indent=2))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (EvidenceFailure, OSError, subprocess.SubprocessError, json.JSONDecodeError) as exc:
        print(f"FAIL: {exc}", file=sys.stderr)
        raise SystemExit(1) from exc
