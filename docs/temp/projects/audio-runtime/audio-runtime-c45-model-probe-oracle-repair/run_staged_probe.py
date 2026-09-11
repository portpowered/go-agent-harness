#!/usr/bin/env python3
"""Run C44's public controls against immutable staged artifacts without building."""

from __future__ import annotations

import argparse
import ctypes
import errno
import hashlib
import inspect
import json
import os
import pathlib
import runpy
import shutil
import stat
import subprocess
import sys
import tarfile
import threading
import time
from typing import Any, Callable


HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parents[4]
VERIFY = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c44-retire-cli-model-admission/verify.py"
C47_OUTPUT_ROOT = ROOT / "docs/temp/projects/audio-runtime/audio-runtime-c47-live-probe-output-bound"
RUNS = HERE / "runs"
EXPECTED_ARTIFACTS = {
    "artifact-0-yui": {"name": "artifact-0", "sha256": "d8820356f3d1020875c013553aa5614af44f319b8c2b701a36f0e7c6882a8efe", "bytes": 51042338},
    "artifact-1-consumer": {"name": "artifact-1", "sha256": "5d18828a28f7169c06b280ae9b023335126bd8252c9296d8802cab8b2137449b", "bytes": 6403602},
    "artifact-2-source-snapshot": {"name": "artifact-2.tar", "sha256": "9a803dab9a439211ecf90617c5f063d8f3e27a4e1f8950d7006bd729170ff399", "bytes": 245760},
    "artifact-3-build-descriptor": {"name": "artifact-3.json", "sha256": "e62c5da67ff259dfdfe5ade71f2fb2d654b12617b58e84767d853c95a2319d11", "bytes": 50257},
}
ORIGINAL_TESTED_SOURCE_REVISION = "9f869d1db0a1724128f7c7d083a0054270def68"
INTEGRATED_C44_SOURCE_REVISION = "5f14c45313cfdc71e000fda209e3408fcf863faf"
REQUIRED_FIXTURES = {
    "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures/c16-audio-tool.session.json",
    "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures/c16-interruption.session.json",
}
COPY_CHUNK_BYTES = 1024 * 1024
REPORT_RESERVE_BYTES = 16 * 1024 * 1024
REPLAY_METADATA_RESERVE_BYTES = 4 * 1024 * 1024
REPLAY_TEXT_OUTPUT_RESERVE_BYTES = 8 * 1024 * 1024
CONTROL_METADATA_RESERVE_BYTES = 4 * 1024 * 1024
LATEST_REPORT_RESERVE_BYTES = REPORT_RESERVE_BYTES
PCM_OUTPUT_BYTES = 4800 + 3840
AUDIO_WAV_BYTES = (4800 + 44) + (3840 + 44)
AUDIO_TRACE_WAV_BYTES = (4800 + 44) + (3840 + 44)
RESERVE_SAMPLE_INTERVAL_SECONDS = 0.1
CLEANUP_GRACE_SECONDS = 2.0
STAGING_METHODS: dict[str, str] = {}


class StorageBlocked(RuntimeError):
    """A measured reserve failure that requires operator recovery."""


class _OutputBudget:
    def __init__(self, limit_bytes: int) -> None:
        if limit_bytes <= 0:
            raise ValueError("output budget must be positive")
        self.limit_bytes = limit_bytes
        self.used_bytes = 0
        self._lock = threading.Lock()

    @property
    def remaining_bytes(self) -> int:
        with self._lock:
            return max(0, self.limit_bytes - self.used_bytes)

    def require_available(self, label: str) -> None:
        with self._lock:
            if self.used_bytes >= self.limit_bytes:
                raise RuntimeError(
                    f"aggregate private output limit exhausted before launching {label}: "
                    f"{self.used_bytes} >= {self.limit_bytes}"
                )

    def reserve(self, observed_bytes: int) -> tuple[int, bool]:
        with self._lock:
            available = max(0, self.limit_bytes - self.used_bytes)
            accepted = min(available, observed_bytes)
            self.used_bytes += accepted
            return accepted, observed_bytes > accepted


class ReserveMonitor:
    def __init__(self, root: pathlib.Path, minimum_free_bytes: int) -> None:
        self.root = root
        self.minimum_free_bytes = minimum_free_bytes
        self.samples: list[dict[str, Any]] = []
        self.minimum_observed_bytes: int | None = None
        self._last_sample_monotonic: float | None = None

    def sample(self, phase: str, projected_growth_bytes: int = 0) -> int:
        sampled_at = time.monotonic()
        available = free_bytes(self.root)
        required = self.minimum_free_bytes + max(0, projected_growth_bytes)
        self.minimum_observed_bytes = available if self.minimum_observed_bytes is None else min(self.minimum_observed_bytes, available)
        interval = None if self._last_sample_monotonic is None else max(0.0, sampled_at - self._last_sample_monotonic)
        self._last_sample_monotonic = sampled_at
        self.samples.append(
            {
                "phase": phase,
                "timestamp_monotonic": round(sampled_at, 6),
                "interval_seconds": None if interval is None else round(interval, 6),
                "free_bytes": available,
                "projected_growth_bytes": max(0, projected_growth_bytes),
                "required_free_bytes": required,
            }
        )
        if available < required:
            raise StorageBlocked(
                f"storage reserve unavailable during {phase}: available={available}, "
                f"needed={required}, reserve={self.minimum_free_bytes}, "
                f"projected_growth={max(0, projected_growth_bytes)}"
            )
        return available


def sha256(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def artifact_facts(path: pathlib.Path) -> dict[str, Any]:
    return {"path": str(path), "bytes": path.stat().st_size, "sha256": sha256(path)}


def free_bytes(path: pathlib.Path) -> int:
    return shutil.disk_usage(path).free


def tree_bytes(path: pathlib.Path) -> int:
    return sum(item.stat().st_size for item in path.rglob("*") if item.is_file())


def path_bytes(path: pathlib.Path) -> int:
    if path.is_symlink():
        return 0
    if path.is_file():
        return path.stat().st_size
    if path.is_dir():
        return tree_bytes(path)
    return 0


def _safe_path_facts(path: pathlib.Path, phase: str, errors: list[dict[str, str]]) -> dict[str, Any]:
    try:
        return {"path": str(path), "bytes": path_bytes(path)}
    except BaseException as exc:
        errors.append({"operation": f"{phase}-inspection", "path": str(path), "error": str(exc)})
        return {"path": str(path), "bytes": None, "inspection_error": str(exc)}


def _known_bytes(entries: list[dict[str, Any]]) -> int | None:
    values = [entry.get("bytes") for entry in entries]
    if any(not isinstance(value, int) for value in values):
        return None
    return sum(values)


def cleanup_scratch(paths: list[pathlib.Path], deadline: float | None = None) -> dict[str, Any]:
    errors: list[dict[str, str]] = []
    removed: list[str] = []
    deadline_exceeded = False
    cleanup_grace_deadline: float | None = None

    def check_cleanup_budget(path: pathlib.Path) -> bool:
        nonlocal cleanup_grace_deadline, deadline_exceeded
        now = time.monotonic()
        if deadline is not None and now >= deadline and not deadline_exceeded:
            deadline_exceeded = True
            cleanup_grace_deadline = now + CLEANUP_GRACE_SECONDS
        if cleanup_grace_deadline is not None and now >= cleanup_grace_deadline:
            errors.append({"operation": "cleanup-grace-deadline", "path": str(path), "error": "bounded cleanup grace exceeded"})
            return False
        return True

    def safe_facts(path: pathlib.Path, phase: str) -> dict[str, Any]:
        if not check_cleanup_budget(path):
            return {"path": str(path), "bytes": None, "inspection_skipped": True}
        return _safe_path_facts(path, phase, errors)

    before = [safe_facts(path, "before") for path in paths]
    for path in paths:
        if not check_cleanup_budget(path):
            break
        try:
            if not path.exists() and not path.is_symlink():
                continue
            if path.is_symlink() or path.is_file():
                path.unlink()
            elif path.is_dir():
                shutil.rmtree(path)
            removed.append(str(path))
        except BaseException as exc:
            errors.append({"operation": "remove", "path": str(path), "error": str(exc)})
    after = [safe_facts(path, "after") for path in paths]
    return {
        "paths": [str(path) for path in paths],
        "bytes_before": _known_bytes(before),
        "bytes_after": _known_bytes(after),
        "before": before,
        "after": after,
        "removed": removed,
        "errors": errors,
        "deadline_exceeded": deadline_exceeded,
    }


def require_deadline(started: float, total_timeout: float, phase: str) -> None:
    if time.monotonic() - started > total_timeout:
        raise RuntimeError(f"aggregate deadline exceeded during {phase}")


def _path_is_within(path: pathlib.Path, parent: pathlib.Path) -> bool:
    try:
        path.relative_to(parent)
        return True
    except ValueError:
        return False


def validate_output_root(output_root: pathlib.Path, explicit: bool) -> None:
    output_root = output_root.resolve()
    allowed_root = C47_OUTPUT_ROOT.resolve()
    if not _path_is_within(output_root, allowed_root):
        raise ValueError(
            "output root must be within the C47-owned evidence tree and must not overwrite "
            f"historical or peer evidence: {output_root}"
        )


def _clonefile(source: pathlib.Path, destination: pathlib.Path) -> bool:
    if sys.platform != "darwin":
        return False
    try:
        libc = ctypes.CDLL(None, use_errno=True)
        clonefile = libc.clonefile
    except (AttributeError, OSError):
        return False
    clonefile.argtypes = [ctypes.c_char_p, ctypes.c_char_p, ctypes.c_uint32]
    clonefile.restype = ctypes.c_int
    result = clonefile(os.fsencode(source), os.fsencode(destination), 0)
    if result == 0:
        return True
    error_number = ctypes.get_errno()
    if error_number in (errno.ENOTSUP, errno.EOPNOTSUPP, errno.ENOSYS):
        return False
    raise OSError(error_number, os.strerror(error_number), str(source), str(destination))


def _copy_independent(
    source: pathlib.Path,
    destination: pathlib.Path,
    reserve_check: Callable[[str], None] | None,
) -> None:
    with source.open("rb") as source_handle, destination.open("wb") as destination_handle:
        while True:
            if reserve_check is not None:
                reserve_check("before-copy-chunk")
            block = source_handle.read(COPY_CHUNK_BYTES)
            if not block:
                break
            destination_handle.write(block)
            if reserve_check is not None:
                reserve_check("after-copy-chunk")
    shutil.copystat(source, destination)


def stage_executables(
    staged_root: pathlib.Path,
    run_dir: pathlib.Path,
    reserve_check: Callable[[str], None] | None = None,
) -> dict[str, pathlib.Path]:
    staging_dir = run_dir / "staged-binaries"
    staging_dir.mkdir(parents=True, exist_ok=True)
    STAGING_METHODS.clear()
    staged: dict[str, pathlib.Path] = {}
    for key in ("artifact-0-yui", "artifact-1-consumer"):
        source = staged_root / EXPECTED_ARTIFACTS[key]["name"]
        destination = staging_dir / source.name
        if reserve_check is not None:
            reserve_check("before-stage-executable")
        cloned = _clonefile(source, destination)
        if not cloned:
            _copy_independent(source, destination, reserve_check)
        STAGING_METHODS[key] = "apfs-clone" if cloned else "independent-copy"
        source_stat = source.stat()
        destination_stat = destination.stat()
        if destination_stat.st_ino == source_stat.st_ino or destination_stat.st_nlink != 1:
            raise RuntimeError(f"staged executable is not an independent copy: {source} -> {destination}")
        os.chmod(destination, destination_stat.st_mode | stat.S_IWUSR | stat.S_IXUSR)
        facts = artifact_facts(destination)
        if facts["bytes"] != source_stat.st_size or facts["sha256"] != sha256(source):
            raise RuntimeError(f"staged executable changed while copying: {facts}, source={source}")
        if not os.access(destination, os.X_OK | os.W_OK):
            raise RuntimeError(f"staged executable is not writable and executable: {destination}")
        if reserve_check is not None:
            reserve_check("after-stage-executable")
        staged[key] = destination
    return staged


def check_staged_artifacts(staged_root: pathlib.Path) -> dict[str, Any]:
    before: dict[str, Any] = {}
    for key, expected in EXPECTED_ARTIFACTS.items():
        path = staged_root / expected["name"]
        if not path.is_file():
            raise RuntimeError(f"missing staged {key}: {path}")
        facts = artifact_facts(path)
        if facts["bytes"] != expected["bytes"] or facts["sha256"] != expected["sha256"]:
            raise RuntimeError(f"staged {key} changed: {facts}, expected {expected}")
        before[key] = facts
    return before


def _safe_fixture_members(archive: pathlib.Path) -> dict[str, tarfile.TarInfo]:
    with tarfile.open(archive, "r") as handle:
        return _safe_fixture_members_from_handle(handle)


def _safe_fixture_members_from_handle(handle: tarfile.TarFile) -> dict[str, tarfile.TarInfo]:
    members = {member.name: member for member in handle.getmembers()}
    missing = sorted(REQUIRED_FIXTURES - members.keys())
    if missing:
        raise RuntimeError(f"staged source snapshot is missing fixtures: {missing}")
    selected = {}
    for name in sorted(REQUIRED_FIXTURES):
        member = members[name]
        if not member.isfile() or pathlib.PurePosixPath(name).is_absolute() or ".." in pathlib.PurePosixPath(name).parts:
            raise RuntimeError(f"unsafe fixture member in staged snapshot: {name}")
        selected[name] = member
    return selected


def fixture_member_facts(archive: pathlib.Path) -> dict[str, dict[str, Any]]:
    members = _safe_fixture_members(archive)
    return {name: {"bytes": member.size, "mode": member.mode} for name, member in members.items()}


def extract_required_fixtures(
    archive: pathlib.Path,
    destination: pathlib.Path,
    reserve_check: Callable[[str], None] | None = None,
) -> pathlib.Path:
    with tarfile.open(archive, "r") as handle:
        members = _safe_fixture_members_from_handle(handle)
        for name, member in members.items():
            output = destination / name
            output.parent.mkdir(parents=True, exist_ok=True)
            source = handle.extractfile(member)
            if source is None:
                raise RuntimeError(f"staged source snapshot fixture cannot be read: {name}")
            with source, output.open("wb") as destination_handle:
                while True:
                    if reserve_check is not None:
                        reserve_check("before-extract-chunk")
                    block = source.read(COPY_CHUNK_BYTES)
                    if not block:
                        break
                    destination_handle.write(block)
                    if reserve_check is not None:
                        reserve_check("after-extract-chunk")
            os.chmod(output, member.mode & 0o777)
    return destination / "docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures"


def replay_output_budget(fixture_bytes: int) -> dict[str, int]:
    provider_fixture_bytes = fixture_bytes
    total = (
        provider_fixture_bytes
        + PCM_OUTPUT_BYTES
        + AUDIO_WAV_BYTES
        + AUDIO_TRACE_WAV_BYTES
        + REPLAY_TEXT_OUTPUT_RESERVE_BYTES
        + REPLAY_METADATA_RESERVE_BYTES
    )
    return {
        "provider_fixture_copy_bytes": provider_fixture_bytes,
        "pcm_output_bytes": PCM_OUTPUT_BYTES,
        "audio_wav_bytes": AUDIO_WAV_BYTES,
        "audio_trace_wav_bytes": AUDIO_TRACE_WAV_BYTES,
        "text_output_reserve_bytes": REPLAY_TEXT_OUTPUT_RESERVE_BYTES,
        "metadata_reserve_bytes": REPLAY_METADATA_RESERVE_BYTES,
        "total_bytes": total,
    }


def prospective_storage_budget(staged_root: pathlib.Path, max_output_bytes: int) -> dict[str, Any]:
    staged = check_staged_artifacts(staged_root)
    archive = staged_root / EXPECTED_ARTIFACTS["artifact-2-source-snapshot"]["name"]
    if not archive.is_file():
        # Existing C45 unit tests replace the staging helpers with tiny fakes.
        # Real missions always reach this point only after check_staged_artifacts
        # has verified the archive, so the synthetic seam cannot bypass a mission.
        return {
            "synthetic_input": True,
            "prospective_binary_copy_bytes": 0,
            "prospective_fixture_bytes": 0,
            "prospective_scratch_bytes": 0,
            "prospective_output_bytes": 0,
            "prospective_replay_output_bytes": 0,
            "prospective_metadata_bytes": 0,
            "prospective_report_bytes": 0,
            "prospective_latest_report_bytes": 0,
            "projected_growth_bytes": 0,
            "artifact_input_bytes": sum(item["bytes"] for item in staged.values()),
        }
    fixture_members = fixture_member_facts(archive)
    binary_bytes = sum(staged[key]["bytes"] for key in ("artifact-0-yui", "artifact-1-consumer"))
    fixture_bytes = sum(item["bytes"] for item in fixture_members.values())
    scratch_bytes = binary_bytes + fixture_bytes
    replay_outputs = replay_output_budget(fixture_bytes)
    metadata_bytes = CONTROL_METADATA_RESERVE_BYTES
    projected_growth = (
        scratch_bytes
        + max_output_bytes
        + replay_outputs["total_bytes"]
        + metadata_bytes
        + REPORT_RESERVE_BYTES
        + LATEST_REPORT_RESERVE_BYTES
    )
    return {
        "synthetic_input": False,
        "prospective_binary_copy_bytes": binary_bytes,
        "prospective_fixture_bytes": fixture_bytes,
        "prospective_scratch_bytes": scratch_bytes,
        "prospective_output_bytes": max_output_bytes,
        "prospective_replay_output_bytes": replay_outputs["total_bytes"],
        "replay_output_breakdown": replay_outputs,
        "prospective_metadata_bytes": metadata_bytes,
        "prospective_report_bytes": REPORT_RESERVE_BYTES,
        "prospective_latest_report_bytes": LATEST_REPORT_RESERVE_BYTES,
        "projected_growth_bytes": projected_growth,
        "fixture_members": fixture_members,
        "artifact_input_bytes": sum(item["bytes"] for item in staged.values()),
    }


def _call_with_reserve(
    function: Callable[..., Any],
    *args: Any,
    reserve_check: Callable[[str], None],
) -> Any:
    try:
        supports_reserve = "reserve_check" in inspect.signature(function).parameters
    except (TypeError, ValueError):
        supports_reserve = True
    if supports_reserve:
        return function(*args, reserve_check=reserve_check)
    # The compatibility branch is only for the preserved C45 unit-test fakes;
    # the real staging functions above always expose the guard.
    return function(*args)


def import_verify() -> dict[str, Any]:
    loaded = runpy.run_path(str(VERIFY), run_name="c45_staged_probe_import")
    verifier_globals = loaded["run_wrong_replay_oracles"].__globals__
    forbidden_calls: list[str] = []

    def forbidden(name: str) -> Callable[..., Any]:
        def fail(*_args: Any, **_kwargs: Any) -> Any:
            forbidden_calls.append(name)
            raise RuntimeError(f"forbidden verifier helper called: {name}")

        return fail

    for name in ("main", "source_facts", "build_consumer", "build_yui", "run_consumer_tests"):
        verifier_globals[name] = forbidden(name)
    loaded["_c45_globals"] = verifier_globals
    loaded["_c45_forbidden_calls"] = forbidden_calls
    return loaded


def run_controls(
    module: dict[str, Any],
    run_dir: pathlib.Path,
    fixture_root: pathlib.Path,
    staged_root: pathlib.Path,
    started: float,
    child_timeout: float,
    total_timeout: float,
    max_output_bytes: int,
    executable_paths: dict[str, pathlib.Path] | None = None,
    reserve_check: Callable[[str], None] | None = None,
) -> dict[str, Any]:
    verifier_globals = module["_c45_globals"]
    executable_paths = executable_paths or {}
    verifier_globals["CONSUMER"] = executable_paths.get("artifact-1-consumer", executable_paths.get("artifact-1", staged_root / "artifact-1"))
    verifier_globals["YUI"] = executable_paths.get("artifact-0-yui", executable_paths.get("artifact-0", staged_root / "artifact-0"))
    verifier_globals["FIXTURES"] = fixture_root
    verifier_globals["ARTIFACTS"] = run_dir / "unused-artifacts"
    verifier_globals["RUNS"] = run_dir / "unused-runs"
    original_run_process = verifier_globals["run_process"]
    output_budget = verifier_globals.get("OutputBudget", _OutputBudget)(max_output_bytes)

    def bounded_run_process(*args: Any, **kwargs: Any) -> dict[str, Any]:
        kwargs["output_budget"] = output_budget
        if reserve_check is not None:
            kwargs["reserve_check"] = reserve_check
        previous_budget_bytes = output_budget.used_bytes
        result = original_run_process(*args, **kwargs)
        child_output_bytes = result["stdout_bytes"] + result["stderr_bytes"]
        if output_budget.used_bytes == previous_budget_bytes:
            _accepted, overflow = output_budget.reserve(child_output_bytes)
            if overflow:
                aggregate = previous_budget_bytes + child_output_bytes
                raise verifier_globals["EvidenceFailure"](
                    f"staged controls exceeded the aggregate private output limit: {aggregate} > {max_output_bytes}"
                )
        result["aggregate_output_bytes"] = output_budget.used_bytes
        result["remaining_output_bytes"] = output_budget.remaining_bytes
        return result

    verifier_globals["run_process"] = bounded_run_process
    public_consumer = module["run_public_consumer"](run_dir, started, total_timeout, child_timeout)
    require_deadline(started, total_timeout, "public consumer")
    observer = module["run_effect_observer_positive"](run_dir, started, total_timeout, child_timeout)
    require_deadline(started, total_timeout, "effect observer")
    cli_admission = module["run_cli_admission_controls"](run_dir, started, total_timeout, child_timeout)
    require_deadline(started, total_timeout, "CLI admission")
    replay = module["run_replay_controls"](run_dir, started, total_timeout, child_timeout)
    require_deadline(started, total_timeout, "replay")
    wrong_replay_oracles = module["run_wrong_replay_oracles"](run_dir, started, total_timeout, child_timeout, replay)
    require_deadline(started, total_timeout, "wrong replay oracles")
    timeout_cleanup = module["run_timeout_control"](run_dir, started, total_timeout, child_timeout)
    require_deadline(started, total_timeout, "timeout cleanup")
    if module["_c45_forbidden_calls"]:
        raise RuntimeError(f"forbidden verifier helpers called: {module['_c45_forbidden_calls']}")
    return {
        "public_consumer": public_consumer,
        "effect_observer": observer,
        "cli_admission": cli_admission,
        "replay": replay,
        "wrong_replay_oracles": wrong_replay_oracles,
        "timeout_cleanup": timeout_cleanup,
        "output_bytes": output_budget.used_bytes,
        "output_limit_bytes": output_budget.limit_bytes,
        "remaining_output_bytes": output_budget.remaining_bytes,
        "elapsed_seconds": round(time.monotonic() - started, 6),
    }


def free_space_report(monitor: ReserveMonitor) -> dict[str, Any]:
    return {
        "minimum_observed": monitor.minimum_observed_bytes,
        "sample_interval_seconds": RESERVE_SAMPLE_INTERVAL_SECONDS,
        "samples": monitor.samples,
    }


def write_outcome(outcome: dict[str, Any], run_dir: pathlib.Path, output_root: pathlib.Path) -> None:
    report_path = run_dir / "outcome.json"
    latest_path = output_root / "latest-staged-probe.json"
    for _ in range(8):
        serialized = json.dumps(outcome, indent=2) + "\n"
        report_path.write_text(serialized, encoding="utf-8")
        latest_path.write_text(serialized, encoding="utf-8")
        measured = tree_bytes(run_dir)
        latest_measured = latest_path.stat().st_size
        if (
            outcome.get("report_bytes") == measured
            and outcome.get("latest_report_bytes") == latest_measured
        ):
            break
        outcome["report_bytes"] = measured
        outcome["latest_report_bytes"] = latest_measured
    else:
        raise RuntimeError("final report size did not stabilize")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--staged-root", type=pathlib.Path, required=True)
    parser.add_argument("--output-root", type=pathlib.Path)
    parser.add_argument("--child-timeout", type=float, default=60)
    parser.add_argument("--total-timeout", type=float, default=1500)
    parser.add_argument("--max-output-bytes", type=int, default=64 * 1024 * 1024)
    parser.add_argument("--min-free-bytes", type=int, default=2 * 1024 * 1024 * 1024)
    args = parser.parse_args()
    if args.child_timeout <= 0 or args.child_timeout > 60:
        raise SystemExit("C45 child bound exceeds 60 seconds")
    if args.total_timeout <= 0 or args.total_timeout > 1500:
        raise SystemExit("C45 aggregate bound exceeds 1500 seconds")
    if args.max_output_bytes <= 0 or args.max_output_bytes > 64 * 1024 * 1024:
        raise SystemExit("C45 output bound exceeds 64 MiB")
    if args.min_free_bytes < 2 * 1024 * 1024 * 1024:
        raise SystemExit("C45 free-space reserve is below 2 GiB")

    started = time.monotonic()
    staged_root = args.staged_root.resolve()
    output_root = (args.output_root or HERE).resolve()
    validate_output_root(output_root, args.output_root is not None)
    output_root.mkdir(parents=True, exist_ok=True)
    runs_root = output_root / "runs"
    runs_root.mkdir(parents=True, exist_ok=True)
    run_dir = runs_root / f"staged-probe-{time.strftime('%Y%m%dT%H%M%SZ', time.gmtime())}-{os.getpid()}"
    run_dir.mkdir(parents=True, exist_ok=False)
    scratch_paths = [run_dir / "staged-source", run_dir / "staged-binaries", run_dir / "unused-artifacts", run_dir / "unused-runs"]
    outcome: dict[str, Any] = {
        "schema": "audio-runtime-c45-staged-probe/v1",
        "decision": "FAILED",
        "staged_root": str(staged_root),
        "output_root": str(output_root),
        "run_dir": str(run_dir),
        "child_timeout_seconds": args.child_timeout,
        "total_timeout_seconds": args.total_timeout,
        "max_output_bytes": args.max_output_bytes,
        "min_free_bytes": args.min_free_bytes,
        "reserve_filesystem_root": str(output_root),
        "forbidden_helpers_called": [],
        "scratch_paths": [str(path) for path in scratch_paths],
    }
    deadline = started + args.total_timeout
    monitor = ReserveMonitor(output_root, args.min_free_bytes)

    def reserve_check(phase: str) -> int:
        require_deadline(started, args.total_timeout, phase)
        return monitor.sample(phase, projected_growth_bytes)

    def record_cleanup() -> dict[str, Any]:
        cleanup = cleanup_scratch(scratch_paths, deadline=deadline)
        if "scratch_cleanup" in outcome:
            outcome["cleanup_after_failure"] = cleanup
        else:
            outcome["scratch_cleanup"] = cleanup
        return cleanup

    try:
        require_deadline(started, args.total_timeout, "artifact verification")
        staged_before = check_staged_artifacts(staged_root)
        require_deadline(started, args.total_timeout, "storage preflight")
        storage_budget = prospective_storage_budget(staged_root, args.max_output_bytes)
        outcome["storage_preflight"] = storage_budget
        projected_growth_bytes = storage_budget["projected_growth_bytes"]
        require_deadline(started, args.total_timeout, "storage reserve preflight")
        free_before = monitor.sample("preflight", storage_budget["projected_growth_bytes"])
        require_deadline(started, args.total_timeout, "descriptor read")
        descriptor = json.loads((staged_root / "artifact-3.json").read_text(encoding="utf-8"))
        executable_paths = _call_with_reserve(stage_executables, staged_root, run_dir, reserve_check=reserve_check)
        require_deadline(started, args.total_timeout, "executable staging")
        fixture_root = _call_with_reserve(
            extract_required_fixtures,
            staged_root / "artifact-2.tar",
            run_dir / "staged-source",
            reserve_check=reserve_check,
        )
        require_deadline(started, args.total_timeout, "fixture extraction")
        outcome["artifact_hashes_before"] = staged_before
        outcome["verifier_sha256"] = sha256(VERIFY)
        outcome["staged_binary_bytes"] = staged_before["artifact-0-yui"]["bytes"] + staged_before["artifact-1-consumer"]["bytes"]
        outcome["binary_output_bytes"] = 0
        outcome["binary_staging_bytes"] = sum(path.stat().st_size for path in executable_paths.values())
        outcome["execution_bindings"] = {key: str(path) for key, path in executable_paths.items()}
        outcome["free_space_before_bytes"] = free_before
        outcome["source_staging_bytes"] = tree_bytes(run_dir / "staged-source")
        outcome["fixture_bytes"] = outcome["source_staging_bytes"]
        outcome["staging_method"] = dict(STAGING_METHODS)
        outcome["original_artifact_provenance"] = {
            "descriptor_schema": descriptor.get("schema"),
            "descriptor_mode": descriptor.get("mode"),
            "descriptor_decision": descriptor.get("decision"),
            "descriptor_sha256": staged_before["artifact-3-build-descriptor"]["sha256"],
            "source_snapshot_sha256": staged_before["artifact-2-source-snapshot"]["sha256"],
        }
        outcome["toolchain_and_build_flags"] = [
            {key: step.get(key) for key in ("label", "argv", "cwd", "artifact")}
            for step in descriptor.get("steps", [])
            if isinstance(step, dict)
        ]
        module = import_verify()
        controls = run_controls(
            module,
            run_dir,
            fixture_root,
            staged_root,
            started,
            args.child_timeout,
            args.total_timeout,
            args.max_output_bytes,
            executable_paths,
            reserve_check,
        )
        outcome["controls"] = controls
        outcome["binary_output_bytes"] = controls.get("output_bytes", 0)
        outcome["forbidden_helpers_called"] = module["_c45_forbidden_calls"]
        if outcome["forbidden_helpers_called"]:
            raise RuntimeError(f"forbidden verifier helpers called: {outcome['forbidden_helpers_called']}")
        require_deadline(started, args.total_timeout, "staged controls")
        outcome["observed_run_tree_bytes_before_cleanup"] = tree_bytes(run_dir)
        outcome["observed_replay_output_bytes_before_cleanup"] = sum(
            tree_bytes(run_dir / label)
            for label in ("replay-audio-tool", "replay-interruption")
            if (run_dir / label).is_dir()
        )
        outcome["observed_control_metadata_bytes_before_cleanup"] = max(
            0,
            outcome["observed_run_tree_bytes_before_cleanup"]
            - outcome["observed_replay_output_bytes_before_cleanup"],
        )
        free_during = monitor.sample("after-controls", projected_growth_bytes)
        staged_after = check_staged_artifacts(staged_root)
        if staged_before != staged_after:
            raise RuntimeError(f"staged artifact changed during probe: before={staged_before}, after={staged_after}")
        outcome["artifact_hashes_after"] = staged_after
        outcome["artifact_input_equivalence"] = staged_before == staged_after
        outcome["fixture_bytes"] = tree_bytes(fixture_root)
        cleanup = cleanup_scratch(scratch_paths, deadline=deadline)
        outcome["scratch_bytes"] = cleanup["bytes_before"]
        outcome["scratch_cleanup"] = cleanup
        if cleanup["errors"] or cleanup["deadline_exceeded"] or cleanup["bytes_after"] != 0:
            raise RuntimeError(f"staged probe scratch cleanup incomplete: {cleanup}")
        outcome["observed_run_tree_bytes_after_cleanup"] = tree_bytes(run_dir)
        require_deadline(started, args.total_timeout, "scratch cleanup")
        free_after = monitor.sample("after-cleanup", projected_growth_bytes)
        outcome["free_space_samples_bytes"] = {
            "before": free_before,
            "during": free_during,
            "after": free_after,
            "minimum_observed": monitor.minimum_observed_bytes,
            "sample_interval_seconds": RESERVE_SAMPLE_INTERVAL_SECONDS,
            "samples": monitor.samples,
        }
        tested_source_revision = subprocess.run(
            ["rtk", "proxy", "git", "rev-parse", "HEAD"], cwd=ROOT, check=True, capture_output=True, text=True, timeout=10
        ).stdout.strip()
        outcome["tested_source_revision"] = tested_source_revision
        outcome["original_tested_source_revision"] = ORIGINAL_TESTED_SOURCE_REVISION
        outcome["integrated_c44_source_revision"] = INTEGRATED_C44_SOURCE_REVISION
        outcome["repaired_tooling_provenance"] = {
            "tested_source_revision": tested_source_revision,
            "verify_path": str(VERIFY),
            "verify_sha256": sha256(VERIFY),
            "run_staged_probe_path": str(pathlib.Path(__file__).resolve()),
            "run_staged_probe_sha256": sha256(pathlib.Path(__file__).resolve()),
            "original_tested_source_revision": ORIGINAL_TESTED_SOURCE_REVISION,
            "integrated_c44_source_revision": INTEGRATED_C44_SOURCE_REVISION,
        }
        outcome["binary_identity"] = {
            key: outcome["artifact_hashes_before"][key]
            for key in ("artifact-0-yui", "artifact-1-consumer")
        }
        outcome["decision"] = "ACCEPTED"
    except StorageBlocked as exc:
        outcome["decision"] = "BLOCKED"
        outcome["storage_status"] = "BLOCKED"
        outcome["error"] = f"{type(exc).__name__}: {exc}"
        cleanup = record_cleanup()
        outcome["scratch_bytes"] = cleanup["bytes_before"]
        outcome["free_space_samples_bytes"] = free_space_report(monitor)
        outcome["elapsed_seconds"] = round(time.monotonic() - started, 6)
        write_outcome(outcome, run_dir, output_root)
        print(json.dumps(outcome, indent=2))
        return 1
    except Exception as exc:
        outcome["error"] = f"{type(exc).__name__}: {exc}"
        cleanup = record_cleanup()
        outcome["scratch_bytes"] = cleanup["bytes_before"]
        outcome["free_space_samples_bytes"] = free_space_report(monitor)
        outcome["elapsed_seconds"] = round(time.monotonic() - started, 6)
        write_outcome(outcome, run_dir, output_root)
        print(json.dumps(outcome, indent=2))
        return 1
    outcome["elapsed_seconds"] = round(time.monotonic() - started, 6)
    write_outcome(outcome, run_dir, output_root)
    print(json.dumps(outcome, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
