#!/usr/bin/env python3
"""Bounded synthetic controls for the C47 process and staging repairs."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import pathlib
import runpy
import shutil
import signal
import stat
import subprocess
import sys
import tarfile
import tempfile
import time
from typing import Any, Callable


HERE = pathlib.Path(__file__).resolve().parent
VERIFY = HERE.parents[4] / "docs/temp/projects/audio-runtime/audio-runtime-c44-retire-cli-model-admission/verify.py"
STAGED_PROBE = HERE.parents[4] / "docs/temp/projects/audio-runtime/audio-runtime-c45-model-probe-oracle-repair/run_staged_probe.py"
MIN_FREE_BYTES = 2 * 1024 * 1024 * 1024


def load_verify() -> dict[str, Any]:
    return runpy.run_path(str(VERIFY), run_name="c47_verify_controls")


def load_staged_probe() -> dict[str, Any]:
    return runpy.run_path(str(STAGED_PROBE), run_name="c47_staged_probe_controls")


def expect_failure(callable_: Callable[[], Any], label: str) -> tuple[str, dict[str, Any] | None]:
    try:
        callable_()
    except BaseException as exc:
        return str(exc), getattr(exc, "result", None)
    raise AssertionError(f"{label} unexpectedly passed")


def run_child(
    module: dict[str, Any],
    run_dir: pathlib.Path,
    label: str,
    source: str,
    budget: Any | None = None,
    timeout: float = 1.0,
    reserve_check: Callable[[str], None] | None = None,
) -> dict[str, Any]:
    return module["run_process"](
        label,
        [sys.executable, "-c", source],
        HERE,
        run_dir,
        timeout,
        output_budget=budget,
        reserve_check=reserve_check,
    )


def assert_clean_result(result: dict[str, Any]) -> None:
    assert result["parent_reaped"], result
    assert result["surviving_process_group_pids"] == [], result
    assert result["process_group_inspection_available"] is True, result


def capture_controls(timeout: float) -> dict[str, Any]:
    module = load_verify()
    with tempfile.TemporaryDirectory(prefix="c47-capture-", dir=HERE) as temporary:
        root = pathlib.Path(temporary)
        below = run_child(
            module,
            root,
            "below-cap",
            "import sys; sys.stdout.buffer.write(b'O' * 8); sys.stderr.buffer.write(b'E' * 8)",
            module["OutputBudget"](32),
            timeout,
        )
        assert below["exit_code"] == 0 and below["stdout_bytes"] == 8 and below["stderr_bytes"] == 8
        assert below["retained_output_bytes"] == 16
        assert_clean_result(below)

        exact = run_child(
            module,
            root,
            "exact-cap",
            "import sys; sys.stdout.buffer.write(b'O' * 32); sys.stderr.buffer.write(b'E' * 32)",
            module["OutputBudget"](64),
            timeout,
        )
        assert exact["exit_code"] == 0 and exact["retained_output_bytes"] == 64
        assert exact["output_budget_used_bytes"] == 64 and exact["output_budget_remaining_bytes"] == 0
        assert_clean_result(exact)

        fast_overrun_message, fast_overrun = expect_failure(
            lambda: run_child(
                module,
                root,
                "fast-exit-overflow",
                "import sys; sys.stdout.buffer.write(b'X' * 4096); sys.stdout.flush()",
                module["OutputBudget"](1024),
                timeout,
            ),
            "fast exit overflow",
        )
        assert "aggregate private output limit" in fast_overrun_message
        assert fast_overrun is not None and fast_overrun["output_overflow"]
        assert fast_overrun["retained_output_bytes"] <= 1024
        assert fast_overrun["capture_buffer_high_water_bytes"] <= 1025
        assert_clean_result(fast_overrun)

        continuous_message, continuous = expect_failure(
            lambda: run_child(
                module,
                root,
                "continuing-overflow",
                "import sys, time;\nwhile True:\n sys.stdout.buffer.write(b'Y' * 256); sys.stdout.flush(); time.sleep(.001)",
                module["OutputBudget"](1024),
                timeout,
            ),
            "continuing producer overflow",
        )
        assert "aggregate private output limit" in continuous_message
        assert continuous is not None and continuous["output_overflow"]
        assert continuous["retained_output_bytes"] <= 1024
        assert_clean_result(continuous)

        shared_budget = module["OutputBudget"](16)
        first = run_child(
            module,
            root,
            "shared-first",
            "import sys; sys.stdout.buffer.write(b'A' * 8); sys.stderr.buffer.write(b'B' * 8)",
            shared_budget,
            timeout,
        )
        assert first["output_budget_used_bytes"] == 16
        second_message, second = expect_failure(
            lambda: run_child(
                module,
                root,
                "shared-second-overflow",
                "import pathlib, sys; pathlib.Path('must-not-launch').write_text('launched'); sys.stdout.buffer.write(b'Z')",
                shared_budget,
                timeout,
            ),
            "shared exhausted child",
        )
        assert "before launching" in second_message
        assert second is not None and second["not_started"]
        assert not (root / "must-not-launch").exists()

        combined_budget = module["OutputBudget"](12)
        run_child(
            module,
            root,
            "combined-first",
            "import sys; sys.stdout.buffer.write(b'C' * 6)",
            combined_budget,
            timeout,
        )
        combined_message, combined = expect_failure(
            lambda: run_child(
                module,
                root,
                "combined-second",
                "import sys; sys.stderr.buffer.write(b'D' * 7)",
                combined_budget,
                timeout,
            ),
            "combined stdout stderr overflow",
        )
        assert "aggregate private output limit" in combined_message
        assert combined is not None and combined["retained_output_bytes"] <= 12

        descendant_message, descendant = expect_failure(
            lambda: run_child(
                module,
                root,
                "descendant-holds-pipe",
                "import os, signal, sys, time;\nif os.fork() == 0:\n signal.signal(signal.SIGTERM, signal.SIG_IGN)\n while True: time.sleep(1)\nelse:\n sys.stdout.write('parent\\n'); sys.stdout.flush(); os._exit(0)",
                module["OutputBudget"](4096),
                timeout,
            ),
            "descendant holding pipe",
        )
        assert "surviving process-group" in descendant_message
        assert descendant is not None and descendant["survivors_before_final_kill"]
        assert descendant["kill_sent"] and descendant["surviving_process_group_pids"] == []

        nonzero_descendant = run_child(
            module,
            root,
            "nonzero-parent-with-descendant",
            "import os, signal, time;\nif os.fork() == 0:\n signal.signal(signal.SIGTERM, signal.SIG_IGN)\n time.sleep(30)\nelse:\n os._exit(7)",
            module["OutputBudget"](4096),
            timeout,
        )
        assert nonzero_descendant["primary_failure"]["kind"] == "exit_code"
        assert nonzero_descendant["primary_failure"]["exit_code"] == 7
        assert nonzero_descendant["kill_sent"]
        assert_clean_result(nonzero_descendant)

        timeout_result = run_child(
            module,
            root,
            "quiet-timeout",
            "import signal, time; signal.signal(signal.SIGTERM, signal.SIG_IGN); time.sleep(30)",
            module["OutputBudget"](4096),
            min(timeout, 0.25),
        )
        assert timeout_result["timed_out"] and timeout_result["term_sent"] and timeout_result["kill_sent"]
        assert_clean_result(timeout_result)

        original_killpg = module["os"].killpg

        def injected_killpg(pgid: int, signum: int) -> None:
            if signum == module["signal"].SIGTERM:
                raise OSError("injected TERM cleanup failure")
            original_killpg(pgid, signum)

        module["os"].killpg = injected_killpg
        try:
            cleanup_message, cleanup_failure = expect_failure(
                lambda: run_child(
                    module,
                    root,
                    "overflow-cleanup-injection",
                    "import sys; sys.stdout.buffer.write(b'Q' * 4096); sys.stdout.flush()",
                    module["OutputBudget"](64),
                    timeout,
                ),
                "injected cleanup failure",
            )
        finally:
            module["os"].killpg = original_killpg
        assert "aggregate private output limit" in cleanup_message
        assert cleanup_failure is not None
        assert cleanup_failure["primary_failure"]["kind"] == "output_overflow"
        assert any(item["operation"] == "SIGTERM" for item in cleanup_failure["cleanup_failures"])

        verifier_globals = module["run_process"].__globals__
        original_inspector = verifier_globals["process_group_pids"]
        verifier_globals["process_group_pids"] = lambda _pgid: (_ for _ in ()).throw(module["EvidenceFailure"]("synthetic process inspection unavailable"))
        try:
            inspection_message, inspection = expect_failure(
                lambda: run_child(
                    module,
                    root,
                    "inspection-failure",
                    "pass",
                    module["OutputBudget"](64),
                    timeout,
                ),
                "process inspection failure",
            )
        finally:
            verifier_globals["process_group_pids"] = original_inspector
        assert "process inspection unavailable" in inspection_message
        assert inspection is not None and inspection["primary_failure"]["kind"] == "process_inspection"

        verifier_globals["process_group_pids"] = lambda _pgid: (_ for _ in ()).throw(
            module["EvidenceFailure"]("synthetic final process inspection unavailable")
        )
        try:
            overflow_inspection_message, overflow_inspection = expect_failure(
                lambda: run_child(
                    module,
                    root,
                    "overflow-inspection-failure",
                    "import sys; sys.stdout.buffer.write(b'R' * 4096); sys.stdout.flush()",
                    module["OutputBudget"](64),
                    timeout,
                ),
                "overflow with final process inspection failure",
            )
        finally:
            verifier_globals["process_group_pids"] = original_inspector
        assert "aggregate private output limit" in overflow_inspection_message
        assert overflow_inspection is not None
        assert overflow_inspection["primary_failure"]["kind"] == "output_overflow"
        assert any(
            item["operation"] == "final-process-inspection"
            for item in overflow_inspection["cleanup_failures"]
        )

        original_thread_start = module["threading"].Thread.start

        def fail_reader_start(thread: Any, *args: Any, **kwargs: Any) -> Any:
            if thread.name == "capture-reader-start-failure":
                time.sleep(0.2)
                raise RuntimeError("injected capture reader start failure")
            return original_thread_start(thread, *args, **kwargs)

        module["threading"].Thread.start = fail_reader_start
        try:
            post_popen_message, post_popen_failure = expect_failure(
                lambda: run_child(
                    module,
                    root,
                    "reader-start-failure",
                    "import os, signal, time;\nr, w = os.pipe()\nif os.fork() == 0:\n os.close(r)\n signal.signal(signal.SIGTERM, signal.SIG_IGN)\n os.write(w, b'1')\n os.close(w)\n while True: time.sleep(1)\nelse:\n os.close(w)\n os.read(r, 1)\n time.sleep(30)",
                    module["OutputBudget"](4096),
                    timeout,
                ),
                "post-Popen reader start failure",
            )
        finally:
            module["threading"].Thread.start = original_thread_start
        assert "injected capture reader start failure" in post_popen_message
        assert post_popen_failure is not None
        assert post_popen_failure["primary_failure"]["kind"] == "runner"
        assert post_popen_failure["term_sent"] and post_popen_failure["kill_sent"]
        assert_clean_result(post_popen_failure)

        original_ps_run = verifier_globals["subprocess"].run
        try:
            for ps_output in ("", "424242 only\n"):
                verifier_globals["subprocess"].run = lambda *args, _output=ps_output, **kwargs: subprocess.CompletedProcess(
                    args[0], 0, stdout=_output, stderr=""
                )
                message, _ = expect_failure(lambda: module["process_group_pids"](424242), "invalid ps output")
                assert "process inspection unavailable" in message
        finally:
            verifier_globals["subprocess"].run = original_ps_run

        reserve_calls: list[str] = []

        def reserve_failure(phase: str) -> None:
            reserve_calls.append(phase)
            if phase == "during-execution":
                raise module["StorageBlocked"]("synthetic reserve exhausted: available=1 needed=2")

        reserve_message, reserve_result = expect_failure(
            lambda: run_child(
                module,
                root,
                "quiet-reserve-failure",
                "import time; time.sleep(30)",
                module["OutputBudget"](4096),
                timeout,
                reserve_failure,
            ),
            "quiet reserve failure",
        )
        assert "synthetic reserve exhausted" in reserve_message
        assert reserve_result is not None and reserve_result["primary_failure"]["kind"] == "reserve"
        assert "during-execution" in reserve_calls
        assert_clean_result(reserve_result)

        return {
            "below": below,
            "exact": exact,
            "fast_overflow": fast_overrun,
            "continuing_overflow": continuous,
            "shared_budget_not_started": second,
            "combined_overflow": combined,
            "descendant_cleanup": descendant,
            "nonzero_descendant": nonzero_descendant,
            "timeout_cleanup": timeout_result,
            "injected_cleanup": cleanup_failure,
            "inspection_failure": inspection,
            "post_popen_group_cleanup": post_popen_failure,
            "reserve_failure": reserve_result,
        }


def sha256(path: pathlib.Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def make_staged_fixture(module: dict[str, Any], root: pathlib.Path) -> pathlib.Path:
    staged_root = root / "staged"
    staged_root.mkdir()
    (staged_root / "artifact-0").write_bytes(b"tiny-yui")
    (staged_root / "artifact-1").write_bytes(b"tiny-consumer")
    (staged_root / "artifact-3.json").write_text(
        json.dumps({"schema": "c47-test-descriptor/v1", "mode": "reused", "decision": "ACCEPTED", "steps": []}) + "\n",
        encoding="utf-8",
    )
    with tarfile.open(staged_root / "artifact-2.tar", "w") as archive:
        for name, content in (
            ("docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures/c16-audio-tool.session.json", b"audio fixture"),
            ("docs/temp/projects/audio-runtime/audio-runtime-c21-correlated-device-consumption/fixtures/c16-interruption.session.json", b"interrupt fixture"),
        ):
            info = tarfile.TarInfo(name)
            info.size = len(content)
            info.mode = 0o644
            archive.addfile(info, __import__("io").BytesIO(content))
    expected_artifacts = {
        "artifact-0-yui": {"name": "artifact-0", "sha256": sha256(staged_root / "artifact-0"), "bytes": 8},
        "artifact-1-consumer": {"name": "artifact-1", "sha256": sha256(staged_root / "artifact-1"), "bytes": 13},
        "artifact-2-source-snapshot": {"name": "artifact-2.tar", "sha256": sha256(staged_root / "artifact-2.tar"), "bytes": (staged_root / "artifact-2.tar").stat().st_size},
        "artifact-3-build-descriptor": {"name": "artifact-3.json", "sha256": sha256(staged_root / "artifact-3.json"), "bytes": (staged_root / "artifact-3.json").stat().st_size},
    }
    module["EXPECTED_ARTIFACTS"] = expected_artifacts
    module["main"].__globals__["EXPECTED_ARTIFACTS"] = expected_artifacts
    return staged_root


def staging_controls() -> dict[str, Any]:
    module = load_staged_probe()
    with tempfile.TemporaryDirectory(prefix="c47-staging-", dir=HERE) as temporary:
        root = pathlib.Path(temporary)
        staged_root = make_staged_fixture(module, root)
        budget = module["prospective_storage_budget"](staged_root, 1024)
        assert not budget["synthetic_input"]
        assert budget["prospective_binary_copy_bytes"] == 21
        assert budget["prospective_fixture_bytes"] == len(b"audio fixture") + len(b"interrupt fixture")
        assert budget["prospective_replay_output_bytes"] > budget["prospective_fixture_bytes"]
        assert budget["replay_output_breakdown"]["pcm_output_bytes"] == module["PCM_OUTPUT_BYTES"]
        assert budget["replay_output_breakdown"]["audio_wav_bytes"] == module["AUDIO_WAV_BYTES"]
        assert budget["replay_output_breakdown"]["audio_trace_wav_bytes"] == module["AUDIO_TRACE_WAV_BYTES"]
        assert budget["prospective_metadata_bytes"] == module["CONTROL_METADATA_RESERVE_BYTES"]
        assert budget["prospective_report_bytes"] == module["REPORT_RESERVE_BYTES"]
        assert budget["prospective_latest_report_bytes"] == module["LATEST_REPORT_RESERVE_BYTES"]
        assert budget["projected_growth_bytes"] == (
            budget["prospective_scratch_bytes"]
            + budget["prospective_output_bytes"]
            + budget["prospective_replay_output_bytes"]
            + budget["prospective_metadata_bytes"]
            + budget["prospective_report_bytes"]
            + budget["prospective_latest_report_bytes"]
        )
        assert budget["projected_growth_bytes"] > budget["prospective_scratch_bytes"]

        run_dir = root / "run"
        run_dir.mkdir()
        reserve_phases: list[str] = []
        staged = module["stage_executables"](staged_root, run_dir, reserve_phases.append)
        assert set(staged) == {"artifact-0-yui", "artifact-1-consumer"}
        for key, destination in staged.items():
            source = staged_root / module["EXPECTED_ARTIFACTS"][key]["name"]
            source_hash = sha256(source)
            destination_stat = destination.stat()
            assert destination_stat.st_ino != source.stat().st_ino and destination_stat.st_nlink == 1
            assert os.access(destination, os.W_OK | os.X_OK) and sha256(destination) == source_hash
            original = source.read_bytes()
            destination.write_bytes(b"changed")
            assert source.read_bytes() == original and sha256(source) == source_hash
        assert "before-stage-executable" in reserve_phases and "after-stage-executable" in reserve_phases

        fixture_destination = root / "fixture-staged"
        fixture_root = module["extract_required_fixtures"](
            staged_root / "artifact-2.tar", fixture_destination, reserve_phases.append
        )
        assert fixture_root.is_dir() and (fixture_root / "c16-audio-tool.session.json").read_bytes() == b"audio fixture"

        cleanup_path = root / "cleanup-me"
        cleanup_path.mkdir()
        (cleanup_path / "owned.txt").write_bytes(b"owned")
        cleanup = module["cleanup_scratch"]([cleanup_path])
        assert cleanup["bytes_before"] == 5 and cleanup["bytes_after"] == 0 and cleanup["errors"] == []

        inspect_path = root / "cleanup-inspection-failure"
        inspect_path.mkdir()
        (inspect_path / "owned.txt").write_bytes(b"owned")
        cleanup_globals = module["cleanup_scratch"].__globals__
        original_path_bytes = cleanup_globals["path_bytes"]

        def failing_path_bytes(_path: pathlib.Path) -> int:
            raise OSError("injected cleanup inspection failure")

        cleanup_globals["path_bytes"] = failing_path_bytes
        try:
            inspection_cleanup = module["cleanup_scratch"]([inspect_path])
        finally:
            cleanup_globals["path_bytes"] = original_path_bytes
        assert inspection_cleanup["bytes_before"] is None and inspection_cleanup["bytes_after"] is None
        assert any(item["operation"] == "before-inspection" for item in inspection_cleanup["errors"])
        assert any(item["operation"] == "after-inspection" for item in inspection_cleanup["errors"])

        deadline_path = root / "cleanup-deadline"
        deadline_path.write_bytes(b"owned")
        deadline_cleanup = module["cleanup_scratch"]([deadline_path], deadline=time.monotonic() - 1)
        assert deadline_cleanup["deadline_exceeded"]
        assert not deadline_path.exists() and deadline_cleanup["bytes_after"] == 0

        observed_free_paths: list[pathlib.Path] = []

        def synthetic_free_bytes(path: pathlib.Path) -> int:
            observed_free_paths.append(path.resolve())
            return MIN_FREE_BYTES + budget["projected_growth_bytes"] - 1

        original_free_bytes = module["main"].__globals__["free_bytes"]
        original_main_path_bytes = module["main"].__globals__["path_bytes"]
        module["main"].__globals__["free_bytes"] = synthetic_free_bytes
        module["main"].__globals__["path_bytes"] = failing_path_bytes
        output_root = root / "private-output"
        original_argv = sys.argv
        sys.argv = [
            str(STAGED_PROBE),
            "--staged-root",
            str(staged_root),
            "--output-root",
            str(output_root),
            "--child-timeout",
            "1",
            "--total-timeout",
            "10",
            "--max-output-bytes",
            "1024",
            "--min-free-bytes",
            str(MIN_FREE_BYTES),
        ]
        try:
            exit_code = module["main"]()
        finally:
            sys.argv = original_argv
            module["main"].__globals__["free_bytes"] = original_free_bytes
            module["main"].__globals__["path_bytes"] = original_main_path_bytes
        report = json.loads((output_root / "latest-staged-probe.json").read_text(encoding="utf-8"))
        assert exit_code == 1 and report["decision"] == "BLOCKED"
        assert "available=" in report["error"] and "needed=" in report["error"]
        assert observed_free_paths and all(path == output_root.resolve() for path in observed_free_paths)
        assert report["scratch_cleanup"]["errors"]
        run_dir = pathlib.Path(report["run_dir"])
        assert report["report_bytes"] == module["tree_bytes"](run_dir)
        samples = report["free_space_samples_bytes"]["samples"]
        assert samples and all("timestamp_monotonic" in sample and "interval_seconds" in sample for sample in samples)
        assert not (pathlib.Path(report["run_dir"]) / "staged-binaries").exists()

        module["validate_output_root"](root / "private-output-allowed", True)

        for forbidden in (
            module["HERE"],
            module["ROOT"] / "docs/temp/probes/peer-output",
            module["ROOT"] / "docs/temp/projects/audio-runtime/audio-runtime-c45-model-probe-oracle-repair",
        ):
            try:
                module["validate_output_root"](forbidden, True)
            except ValueError as exc:
                assert "historical or peer" in str(exc)
            else:
                raise AssertionError(f"forbidden output root accepted: {forbidden}")

        return {
            "preflight": budget,
            "staged": {key: str(path) for key, path in staged.items()},
            "reserve_phases": reserve_phases,
            "blocked_report": report,
        }


def refresh_controls() -> dict[str, Any]:
    module = runpy.run_path(str(HERE / "refresh_live_evidence.py"), run_name="c47_refresh_controls")
    repository_root = HERE.parents[4]
    with tempfile.TemporaryDirectory(prefix="c47-refresh-", dir=HERE) as temporary:
        root = pathlib.Path(temporary)
        output_root = root / "private-output"
        run_dir = output_root / "runs" / "refresh-01"
        run_dir.mkdir(parents=True)
        stderr_path = run_dir / "invalid-self-play-admission.stderr"
        stderr_path.write_bytes(b"invalid\n\n")
        stderr_reference = str(stderr_path)
        (run_dir / "invalid-self-play-admission.json").write_text(
            json.dumps({"stderr_path": stderr_reference, "stderr_bytes": 0, "stderr_sha256": "stale"}) + "\n",
            encoding="utf-8",
        )
        (run_dir / "outcome.json").write_text(
            json.dumps(
                {
                    "stderr_path": stderr_reference,
                    "report_bytes": 0,
                    "latest_report_bytes": 0,
                    "decision": "ACCEPTED",
                }
            )
            + "\n",
            encoding="utf-8",
        )
        latest_path = output_root / "latest-staged-probe.json"
        latest_path.write_text(
            json.dumps(
                {
                    "stderr_path": stderr_reference,
                    "report_bytes": 0,
                    "latest_report_bytes": 0,
                    "decision": "ACCEPTED",
                }
            )
            + "\n",
            encoding="utf-8",
        )

        refreshed = module["refresh_run"](run_dir)
        outcome = json.loads((run_dir / "outcome.json").read_text(encoding="utf-8"))
        latest = json.loads(latest_path.read_text(encoding="utf-8"))
        measured_report = module["tree_bytes"](run_dir)
        measured_latest = latest_path.stat().st_size
        assert outcome["report_bytes"] == measured_report
        assert outcome["latest_report_bytes"] == measured_latest
        assert latest["report_bytes"] == measured_report
        assert latest["latest_report_bytes"] == measured_latest
        assert refreshed["report_bytes"] == measured_report
        assert refreshed["latest_report_bytes"] == measured_latest
        assert outcome["stderr_bytes"] == len(b"invalid\n")
        assert latest["stderr_sha256"] == hashlib.sha256(b"invalid\n").hexdigest()

        for forbidden in (
            repository_root / "progress.txt",
            HERE.parent / "audio-runtime-c45-model-probe-oracle-repair" / "runs" / "foreign-run",
        ):
            try:
                module["refresh_run"](forbidden)
            except ValueError as exc:
                assert "C47-owned" in str(exc)
            else:
                raise AssertionError(f"foreign evidence path accepted: {forbidden}")

        escape_link = root / "escape"
        escape_link.symlink_to(HERE.parent, target_is_directory=True)
        try:
            module["refresh_run"](escape_link / "runs" / "foreign-run")
        except ValueError as exc:
            assert "C47-owned" in str(exc)
        else:
            raise AssertionError("symlink escape accepted")

        historical_bytes = (repository_root / "progress.txt").read_bytes()
        latest_path.unlink()
        latest_path.symlink_to(repository_root / "progress.txt")
        try:
            module["refresh_run"](run_dir)
        except ValueError as exc:
            assert "symlink" in str(exc)
        else:
            raise AssertionError("symlink latest report accepted")
        assert (repository_root / "progress.txt").read_bytes() == historical_bytes

        return {
            "report_bytes": measured_report,
            "latest_report_bytes": measured_latest,
            "foreign_paths_rejected": True,
            "symlink_escape_rejected": True,
        }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--group", choices=("capture", "staging", "all"), default="all")
    parser.add_argument("--child-timeout", type=float, default=10)
    parser.add_argument("--total-timeout", type=float, default=120)
    args = parser.parse_args()
    if args.child_timeout <= 0 or args.child_timeout > 10:
        raise SystemExit("C47 synthetic child bound exceeds 10 seconds")
    if args.total_timeout <= 0 or args.total_timeout > 120:
        raise SystemExit("C47 synthetic aggregate bound exceeds 120 seconds")
    started = time.monotonic()
    results: dict[str, Any] = {}
    timeout = min(args.child_timeout, 1.0)
    if args.group in ("capture", "all"):
        results["capture"] = capture_controls(timeout)
    if args.group in ("staging", "all"):
        results["staging"] = staging_controls()
        results["refresh"] = refresh_controls()
    elapsed = time.monotonic() - started
    if elapsed > args.total_timeout:
        raise SystemExit(f"C47 synthetic aggregate deadline exceeded: {elapsed:.3f}s")
    print(json.dumps({"schema": "audio-runtime-c47-resource-bounds/v1", "decision": "ACCEPTED", "elapsed_seconds": round(elapsed, 6), "results": results}, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
