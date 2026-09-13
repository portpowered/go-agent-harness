#!/usr/bin/env python3
"""Run bounded, credential-free C112 tool and interruption replay cases."""

from __future__ import annotations

import argparse
from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import threading
import time
from typing import Any


HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[4]
FIXTURES = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c38-interruption-audio-retention/fixtures"
CASES = {
    "trace-audio-tool-replay": {
        "fixture": FIXTURES / "c16-audio-tool.session.json", "fixture_sha256": "38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169",
        "provider_bytes": 4800, "provider_sha256": "0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502",
        "rendered_bytes": 3200, "rendered_sha256": "7d2d8221eb8ec0be3e1da4a3ed518e1e183aa56e4ac0140ca0cf761068555805", "runtime_min": 1, "required_taps": {"speaker_enqueued"},
    },
    "interruption-replay": {
        "fixture": FIXTURES / "c16-interruption.session.json", "fixture_sha256": "154477d4086c47f707441e19489dfa1a21d493475b4163e64a2833dca3f17206",
        "provider_bytes": 3840, "provider_sha256": "6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22",
        "rendered_bytes": 3360, "rendered_sha256": "302e7421a29a4868a0a1a2f1ca2e8432c9015a6475412ec63fe2b15414f469ff", "runtime_min": 1, "required_taps": {"speaker_enqueued"},
    },
}
SECRET_ENV_NAMES = ("OPENAI_API_KEY", "OPENROUTER_API_KEY", "GROK_API_KEY", "ANTHROPIC_API_KEY")
OUTPUT_LIMIT = 256 * 1024
TERM_GRACE = 2.0
KILL_GRACE = 2.0


class EvidenceFailure(RuntimeError):
    pass


@dataclass
class CappedOutput:
    data: bytearray
    total: int = 0
    truncated: bool = False

    def append(self, chunk: bytes) -> None:
        self.total += len(chunk)
        remaining = OUTPUT_LIMIT - len(self.data)
        if remaining > 0:
            self.data.extend(chunk[:remaining])
        self.truncated = self.truncated or self.total > OUTPUT_LIMIT

    def text(self) -> str:
        value = bytes(self.data).decode("utf-8", errors="replace")
        return value + (f"\n[output truncated after {OUTPUT_LIMIT} bytes]\n" if self.truncated else "")


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def read_stream(stream: Any, output: CappedOutput) -> None:
    while chunk := stream.read(8192):
        output.append(chunk)


def surviving_group(pgid: int) -> list[int]:
    try:
        result = subprocess.run(["ps", "-eo", "pid=,pgid="], capture_output=True, text=True, check=True, timeout=2)
    except (OSError, subprocess.SubprocessError):
        return []
    return [int(fields[0]) for line in result.stdout.splitlines() if len(fields := line.split()) == 2 and fields[1] == str(pgid) and fields[0].isdigit()]


def run_child(label: str, argv: list[str], cwd: Path, output_dir: Path, timeout: float) -> dict[str, Any]:
    output_dir.mkdir(parents=True, exist_ok=True)
    environment = dict(os.environ, GOWORK="off")
    for name in SECRET_ENV_NAMES:
        environment.pop(name, None)
    stdout, stderr = CappedOutput(bytearray()), CappedOutput(bytearray())
    started = time.monotonic()
    try:
        process = subprocess.Popen(argv, cwd=cwd, env=environment, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    except OSError as error:
        raise EvidenceFailure(f"could not launch {label}: {error}") from error
    assert process.stdout is not None and process.stderr is not None
    readers = [threading.Thread(target=read_stream, args=(process.stdout, stdout), daemon=True), threading.Thread(target=read_stream, args=(process.stderr, stderr), daemon=True)]
    for reader in readers:
        reader.start()
    timed_out = False
    sigterm_sent = sigkill_sent = False
    try:
        try:
            process.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            timed_out = True
            try:
                os.killpg(process.pid, signal.SIGTERM)
                sigterm_sent = True
            except ProcessLookupError:
                pass
            try:
                process.wait(timeout=TERM_GRACE)
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                    sigkill_sent = True
                except ProcessLookupError:
                    pass
                process.wait(timeout=KILL_GRACE)
    finally:
        if process.poll() is None:
            try:
                os.killpg(process.pid, signal.SIGKILL)
                sigkill_sent = True
            except ProcessLookupError:
                pass
            process.wait(timeout=KILL_GRACE)
        for reader in readers:
            reader.join(timeout=KILL_GRACE if timed_out else TERM_GRACE)
        process.stdout.close()
        process.stderr.close()
    result = {
        "label": label, "argv": argv, "cwd": str(cwd), "timeout_seconds": timeout,
        "elapsed_seconds": round(time.monotonic() - started, 6), "exit_code": process.returncode, "timed_out": timed_out,
        "stdout": stdout.text(), "stderr": stderr.text(), "stdout_bytes": stdout.total, "stderr_bytes": stderr.total,
        "stdout_truncated": stdout.truncated, "stderr_truncated": stderr.truncated,
        "cleanup": {"sigterm_sent": sigterm_sent, "sigkill_sent": sigkill_sent, "surviving_process_group_pids": surviving_group(process.pid)},
        "credential_free_environment": all(name not in environment for name in SECRET_ENV_NAMES),
    }
    (output_dir / "process.json").write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
    return result


def require_clean(result: dict[str, Any]) -> None:
    if result["timed_out"] or result["exit_code"] != 0:
        raise EvidenceFailure(f"{result['label']} did not exit cleanly: {result}")
    if result["cleanup"]["surviving_process_group_pids"] or not result["credential_free_environment"]:
        raise EvidenceFailure(f"{result['label']} violated cleanup or credential boundary: {result}")
    if result["stdout_truncated"] or result["stderr_truncated"]:
        raise EvidenceFailure(f"{result['label']} exceeded output cap")


def replay_argv(artifact: Path, fixture: Path, case_dir: Path) -> list[str]:
    workdir = case_dir / "workdir"
    (workdir / "evidence/runs").mkdir(parents=True, exist_ok=True)
    (case_dir / "config").mkdir(parents=True, exist_ok=True)
    return [
        str(artifact), "-C", str(case_dir / "config"), "--workdir", str(workdir), "--allow-path", str(workdir), "session",
        "--replay", str(fixture), "--audio-out", str(case_dir / "rendered.pcm"), "--record-dir", str(case_dir / "bundle"), "--trace-audio",
    ]


def read_timeline(path: Path) -> list[dict[str, Any]]:
    try:
        return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line]
    except (OSError, json.JSONDecodeError) as error:
        raise EvidenceFailure(f"invalid timeline {path}: {error}") from error


def run_case(name: str, artifact: Path, run_dir: Path, timeout: float, deadline: float) -> dict[str, Any]:
    if time.monotonic() >= deadline:
        raise EvidenceFailure(f"aggregate deadline exceeded before {name}")
    expected = CASES[name]
    fixture = expected["fixture"]
    if not fixture.is_file() or sha256_file(fixture) != expected["fixture_sha256"]:
        raise EvidenceFailure(f"fixture identity changed: {fixture}")
    case_dir = run_dir / name
    result = run_child(name, replay_argv(artifact, fixture, case_dir), case_dir / "workdir", case_dir / "process", min(timeout, deadline - time.monotonic()))
    require_clean(result)
    combined = result["stdout"] + "\n" + result["stderr"]
    if "replay mismatch" in combined.lower() or not any(marker in combined for marker in ("[session closed:", "[session replay complete]")):
        raise EvidenceFailure(f"{name} did not report a completed replay")
    if name == "trace-audio-tool-replay" and ("PROBE_TOOL_MARKER_9182" not in combined or "strict replay continuation" not in combined):
        raise EvidenceFailure("tool replay lost the credential-free tool continuation")
    marker = case_dir / "workdir/evidence/runs/exec-invocations-v4.log"
    if name == "trace-audio-tool-replay" and (not marker.is_file() or "PROBE_TOOL_MARKER_9182" not in marker.read_text(encoding="utf-8")):
        raise EvidenceFailure("tool replay did not preserve its tool side effect")
    bundle = case_dir / "bundle"
    manifest = bundle / "manifest.json"
    provider = bundle / "audio/out-000.pcm"
    rendered = case_dir / "rendered.pcm"
    timeline = bundle / "audio-trace/timeline.jsonl"
    speaker = bundle / "audio-trace/speaker-enqueued.wav"
    for path in (manifest, provider, rendered, timeline, speaker):
        if not path.is_file():
            raise EvidenceFailure(f"{name} missing finalized artifact: {path}")
    if provider.stat().st_size != expected["provider_bytes"] or sha256_file(provider) != expected["provider_sha256"]:
        raise EvidenceFailure(f"{name} provider PCM oracle changed")
    if rendered.stat().st_size != expected["rendered_bytes"] or sha256_file(rendered) != expected["rendered_sha256"]:
        raise EvidenceFailure(f"{name} rendered PCM oracle changed")
    events = read_timeline(timeline)
    taps = {event.get("tap") for event in events if event.get("kind") == "audio"}
    runtimes = [event for event in events if event.get("kind") == "runtime"]
    if not expected["required_taps"].issubset(taps) or len(runtimes) < expected["runtime_min"]:
        raise EvidenceFailure(f"{name} trace did not retain its replay-visible audio edge and runtime evidence")
    try:
        manifest_value = json.loads(manifest.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise EvidenceFailure(f"invalid manifest {manifest}: {error}") from error
    if not manifest_value.get("artifacts"):
        raise EvidenceFailure(f"{name} manifest has no artifacts")
    return {
        "case": name, "fixture": str(fixture), "fixture_sha256": expected["fixture_sha256"], "process": result,
        "manifest": str(manifest), "manifest_sha256": sha256_file(manifest), "timeline": str(timeline),
        "timeline_events": len(events), "runtime_events": len(runtimes), "provider_bytes": provider.stat().st_size,
        "provider_sha256": sha256_file(provider), "rendered_bytes": rendered.stat().st_size, "rendered_sha256": sha256_file(rendered),
        "speaker_trace_bytes": speaker.stat().st_size, "speaker_trace_sha256": sha256_file(speaker),
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--case", action="append", choices=tuple(CASES), dest="cases")
    parser.add_argument("--artifact", type=Path, default=HERE / "artifacts/yui")
    parser.add_argument("--child-timeout", type=float, default=60.0)
    parser.add_argument("--aggregate-timeout", type=float, default=180.0)
    args = parser.parse_args()
    cases = args.cases or list(CASES)
    started = time.monotonic()
    run_dir = HERE / "runs" / f"vertical-{time.strftime('%Y%m%dT%H%M%SZ', time.gmtime())}-{os.getpid()}"
    run_dir.mkdir(parents=True, exist_ok=False)
    try:
        artifact = args.artifact.expanduser().resolve()
        if not artifact.is_file():
            raise EvidenceFailure(f"source-pinned yui artifact is unavailable: {artifact}")
        if subprocess.run(["git", "rev-parse", "--show-toplevel"], cwd=ROOT, capture_output=True, text=True, check=True).stdout.strip() != str(ROOT):
            raise EvidenceFailure(f"probe source root is not the admitted checkout: {ROOT}")
        deadline = started + args.aggregate_timeout
        reports = [run_case(case, artifact, run_dir, args.child_timeout, deadline) for case in cases]
        outcome = {"status": "pass", "decision": "C112_REPLAY_PASS", "source_revision": subprocess.run(["git", "rev-parse", "HEAD"], cwd=ROOT, capture_output=True, text=True, check=True).stdout.strip(), "artifact": str(artifact), "artifact_sha256": sha256_file(artifact), "cases": reports, "elapsed_seconds": round(time.monotonic() - started, 6), "aggregate_timeout_seconds": args.aggregate_timeout, "physical_acoustic_claim": "OUT_OF_SCOPE"}
        (run_dir / "outcome.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")
        (HERE / "latest-vertical-probe.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")
        print(json.dumps(outcome, indent=2))
        return 0
    except (EvidenceFailure, OSError, subprocess.SubprocessError, json.JSONDecodeError) as error:
        outcome = {"status": "failed", "decision": "C112_REPLAY_FAIL", "error": str(error), "run_dir": str(run_dir)}
        (HERE / "latest-vertical-probe.json").write_text(json.dumps(outcome, indent=2) + "\n", encoding="utf-8")
        print(json.dumps(outcome, indent=2))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
