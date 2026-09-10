#!/usr/bin/env python3
"""Run the public CI gate against isolated, read-only fake GitHub commands.

The child is the shipped ``ci-gate.py`` entry point.  The fake ``git`` and
``gh`` commands expose only the reads that the gate is expected to perform and
record every invocation.  A process-local ``sitecustomize`` advances logical
time when the waiter requests a poll sleep, so the UNKNOWN case reaches its
real deadline without changing the shipped constants.
"""

import argparse
import hashlib
import json
import os
import signal
import shutil
import stat
import subprocess
import sys
import tempfile
from pathlib import Path


WORK_NAME = "audio-runtime-c24-public-controls"
PR_NUMBER = 412
CANDIDATE_HEAD = "0123456789abcdef0123456789abcdef01234567"
STALE_HEAD = "fedcba9876543210fedcba9876543210fedcba98"
REQUIRED_CHECKS = (
    "CI (static)",
    "CI (unit)",
    "CI (integration)",
    "CI (coverage)",
    "CI (race)",
    "CI (hermetic)",
    "CI (WebMCP Chrome)",
    "CI (macOS audio release)",
)
EXPECTED_DECISIONS = {
    "conflict": "REJECTED",
    "unknown": "FAILED",
    "stale-head": "REJECTED",
    "green": "ACCEPTED",
}
MAX_OUTPUT_BYTES = 32 * 1024


def _write_executable(path, content):
    path.write_text(content, encoding="utf-8")
    path.chmod(path.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)


def _write_json(path, value):
    path.write_text(json.dumps(value, sort_keys=True) + "\n", encoding="utf-8")


def _sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _source_revision(repo_root):
    result = subprocess.run(
        ["git", "rev-parse", "HEAD"],
        cwd=repo_root,
        capture_output=True,
        text=True,
        check=False,
        timeout=10,
    )
    if result.returncode != 0:
        raise RuntimeError("could not pin the public-control source revision")
    return result.stdout.strip()


def _clock_shim():
    return r'''"""Logical clock shim for one isolated child process."""
import json
import os
import time

_clock = [0.0]
_factor = float(os.environ.get("CI_CLOCK_ACCELERATION", "1"))
_log_path = os.environ.get("CI_CLOCK_SLEEP_LOG")


def monotonic():
    return _clock[0]


def sleep(requested_seconds):
    requested_seconds = float(requested_seconds)
    advanced_seconds = requested_seconds * _factor
    _clock[0] += advanced_seconds
    if _log_path:
        with open(_log_path, "a", encoding="utf-8") as stream:
            stream.write(
                json.dumps(
                    {
                        "pid": os.getpid(),
                        "requestedSeconds": requested_seconds,
                        "advancedSeconds": advanced_seconds,
                        "acceleration": _factor,
                    },
                    sort_keys=True,
                )
                + "\n"
            )


time.monotonic = monotonic
time.sleep = sleep
'''


def _fake_git(worktree, state_path, trace_path):
    return f'''#!{sys.executable}
import json
import os
import sys

WORKTREE = {str(worktree)!r}
STATE_PATH = {str(state_path)!r}
TRACE_PATH = {str(trace_path)!r}


def record(status, argv, **fields):
    event = {{"tool": "git", "status": status, "argv": argv, "pid": os.getpid()}}
    event.update(fields)
    with open(TRACE_PATH, "a", encoding="utf-8") as stream:
        stream.write(json.dumps(event, sort_keys=True) + "\\n")


argv = sys.argv[1:]
record("started", argv)
state = json.loads(open(STATE_PATH, encoding="utf-8").read())
expected_prefix = ["-C", WORKTREE]
if argv[:2] != expected_prefix:
    record("rejected", argv, reason="unexpected git worktree selector")
    raise SystemExit(97)

command = argv[2:]
if command == ["rev-parse", "HEAD"]:
    record("allowed", argv, operation="read-head")
    print(state["candidateHead"])
elif command == ["branch", "--show-current"]:
    record("allowed", argv, operation="read-branch")
    print("codex/" + state["workName"])
else:
    record("rejected", argv, reason="mutation-or-unexpected-command")
    raise SystemExit(97)
record("finished", argv)
'''


def _check_rows():
    rows = []
    for index, name in enumerate(REQUIRED_CHECKS):
        link = f"https://github.com/portpowered/go-agent-harness/actions/runs/900/jobs/{index + 1}"
        rows.append(
            {
                "name": name,
                "state": "SUCCESS",
                "bucket": "pass",
                "link": link,
                "workflow": "CI",
            }
        )
    return rows


def _rollup_rows():
    rows = []
    for row in _check_rows():
        rows.append(
            {
                "__typename": "CheckRun",
                "name": row["name"],
                "detailsUrl": row["link"],
                "workflowName": row["workflow"],
                "status": "COMPLETED",
                "conclusion": "SUCCESS",
            }
        )
    return rows


def _fake_gh(state_path, trace_path):
    check_rows = json.dumps(_check_rows(), sort_keys=True)
    rollup_rows = json.dumps(_rollup_rows(), sort_keys=True)
    return f'''#!{sys.executable}
import json
import os
import sys

STATE_PATH = {str(state_path)!r}
TRACE_PATH = {str(trace_path)!r}
CHECK_ROWS = {check_rows}
ROLLUP_ROWS = {rollup_rows}


def record(status, argv, **fields):
    event = {{"tool": "gh", "status": status, "argv": argv, "pid": os.getpid()}}
    event.update(fields)
    with open(TRACE_PATH, "a", encoding="utf-8") as stream:
        stream.write(json.dumps(event, sort_keys=True) + "\\n")


def output(value, returncode=0, stderr=""):
    if value is not None:
        print(json.dumps(value, sort_keys=True))
    if stderr:
        print(stderr, file=sys.stderr)
    record("finished", argv)
    raise SystemExit(returncode)


argv = sys.argv[1:]
record("started", argv)
state = json.loads(open(STATE_PATH, encoding="utf-8").read())
case = state["case"]
if argv == [
    "pr", "list", "--head", "codex/" + state["workName"], "--state", "all",
    "--json", "number,state", "--limit", "20",
]:
    record("allowed", argv, operation="read-pr-list")
    output([{{"number": state["pr"], "state": "OPEN"}}])

if len(argv) == 5 and argv[:3] == ["pr", "view", str(state["pr"])] and argv[3] == "--json":
    requested = argv[4]
    expected = "number,state,headRefOid,statusCheckRollup,mergeable,mergeStateStatus"
    if requested != expected:
        record("rejected", argv, reason="unexpected-pr-view-fields")
        raise SystemExit(97)
    record("allowed", argv, operation="read-pr-view")
    if case == "stale-head":
        head = state["staleHead"]
        mergeable = "CONFLICTING"
        merge_state = "DIRTY"
    elif case == "conflict":
        head = state["candidateHead"]
        mergeable = "CONFLICTING"
        merge_state = "DIRTY"
    elif case == "unknown":
        head = state["candidateHead"]
        mergeable = "UNKNOWN"
        merge_state = "UNKNOWN"
    else:
        head = state["candidateHead"]
        mergeable = "MERGEABLE"
        merge_state = "CLEAN"
    output({{
        "number": state["pr"],
        "state": "OPEN",
        "headRefOid": head,
        "statusCheckRollup": ROLLUP_ROWS if case == "green" else [],
        "mergeable": mergeable,
        "mergeStateStatus": merge_state,
    }})

if len(argv) >= 5 and argv[:3] == ["pr", "checks", str(state["pr"])] and argv[-2:] == ["--json", "name,state,bucket,link,workflow,startedAt,completedAt"]:
    required = "--required" in argv
    record("allowed", argv, operation="read-required-checks" if required else "read-checks")
    output(CHECK_ROWS if case == "green" else [])

record("rejected", argv, reason="mutation-or-unexpected-command")
raise SystemExit(97)
'''


def _run_process(command, env, cwd, timeout_seconds):
    process = subprocess.Popen(
        command,
        cwd=cwd,
        env=env,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        start_new_session=True,
    )
    try:
        stdout, stderr = process.communicate(timeout=timeout_seconds)
    except subprocess.TimeoutExpired:
        os.killpg(process.pid, signal.SIGKILL)
        stdout, stderr = process.communicate()
        raise RuntimeError(
            f"child timed out after {timeout_seconds}s; stdout={stdout!r}; stderr={stderr!r}"
        )
    if len(stdout.encode()) > MAX_OUTPUT_BYTES or len(stderr.encode()) > MAX_OUTPUT_BYTES:
        raise RuntimeError("child output exceeded the bounded public-control capture")
    return process.returncode, stdout, stderr


def _load_lines(path):
    if not path.exists():
        return []
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines()]


def _assert_trace(trace, case):
    if not trace:
        raise AssertionError(f"{case}: fake command trace is empty")
    if any(event.get("status") == "rejected" for event in trace):
        raise AssertionError(f"{case}: fake command rejected an invocation: {trace}")
    started = [(event["tool"], event["pid"], event["argv"]) for event in trace if event.get("status") == "started"]
    finished = [(event["tool"], event["pid"], event["argv"]) for event in trace if event.get("status") == "finished"]
    if sorted(started) != sorted(finished):
        raise AssertionError(f"{case}: child command did not exit naturally: {trace}")
    if any(
        any(token in {"push", "merge", "reset", "checkout", "commit", "write"} for token in event.get("argv", []))
        for event in trace
    ):
        raise AssertionError(f"{case}: mutation-like command appeared in trace")


def _assert_envelope(case, returncode, stdout, stderr, sleep_events):
    if returncode != 0:
        raise AssertionError(f"{case}: gate exit={returncode}, stderr={stderr!r}")
    if stderr:
        raise AssertionError(f"{case}: unexpected child stderr={stderr!r}")
    lines = stdout.splitlines()
    if len(lines) != 1:
        raise AssertionError(f"{case}: expected exactly one envelope, got {stdout!r}")
    envelope = json.loads(lines[0])
    if set(envelope) != {"decision", "feedback"}:
        raise AssertionError(f"{case}: unexpected envelope keys {envelope!r}")
    expected = EXPECTED_DECISIONS[case]
    if envelope["decision"] != expected:
        raise AssertionError(f"{case}: expected {expected}, got {envelope!r}")
    feedback = envelope["feedback"]
    if not isinstance(feedback, str) or len(feedback) > 2400:
        raise AssertionError(f"{case}: feedback is not bounded")
    if any(ord(character) < 32 and character not in "\t" for character in feedback):
        raise AssertionError(f"{case}: feedback contains a control character")
    if "token=public-control-secret" in feedback or "password=public-control-secret" in feedback:
        raise AssertionError(f"{case}: secret leaked into feedback")
    if case == "conflict":
        for fragment in (
            "PR #412",
            CANDIDATE_HEAD,
            "mergeable=CONFLICTING",
            "fetch origin main",
            "merge main",
            "preserving both sides",
            "focused regressions",
            "changed head",
            "same PR",
        ):
            if fragment not in feedback:
                raise AssertionError(f"{case}: missing feedback fragment {fragment!r}")
        if sleep_events:
            raise AssertionError(f"{case}: conflict requested poll sleeps {sleep_events!r}")
    elif case == "unknown":
        if "Meta-planner intervention" not in feedback:
            raise AssertionError(f"{case}: unknown evidence did not remain infrastructure failure")
        if "CONFLICTING" in feedback or not sleep_events:
            raise AssertionError(f"{case}: unknown classification/sleep evidence is wrong")
    elif case == "stale-head":
        for fragment in ("stale", STALE_HEAD, CANDIDATE_HEAD):
            if fragment not in feedback:
                raise AssertionError(f"{case}: missing stale-head fragment {fragment!r}")
        if "fetch origin main" in feedback or "current candidate head" in feedback:
            raise AssertionError(f"{case}: stale evidence was described as a current conflict")
        if sleep_events:
            raise AssertionError(f"{case}: stale conflict requested poll sleeps {sleep_events!r}")
    elif case == "green":
        if "ready for independent review" not in feedback or not sleep_events:
            raise AssertionError(f"{case}: stable green convergence evidence is incomplete")
    return envelope


def _run_case(gate_path, case, child_timeout_seconds):
    with tempfile.TemporaryDirectory(prefix=f"audio-runtime-c24-{case}-") as temp:
        outer = Path(temp)
        factory_root = (outer / "factory").resolve()
        worktree = (factory_root / ".claude" / "worktrees" / WORK_NAME).resolve()
        fake_bin = (outer / "bin").resolve()
        shim_dir = (outer / "shim").resolve()
        factory_root.mkdir()
        worktree.mkdir(parents=True)
        fake_bin.mkdir()
        shim_dir.mkdir()
        (worktree / ".git").write_text("gitdir: isolated-fake-worktree\n", encoding="utf-8")

        state_path = outer / "state.json"
        trace_path = outer / "commands.jsonl"
        sleep_path = outer / "logical-sleeps.jsonl"
        _write_json(
            state_path,
            {
                "case": case,
                "pr": PR_NUMBER,
                "workName": WORK_NAME,
                "candidateHead": CANDIDATE_HEAD,
                "staleHead": STALE_HEAD,
            },
        )
        _write_executable(fake_bin / "git", _fake_git(worktree, state_path, trace_path))
        _write_executable(fake_bin / "gh", _fake_gh(state_path, trace_path))
        (shim_dir / "sitecustomize.py").write_text(_clock_shim(), encoding="utf-8")

        env = {
            "FACTORY_ROOT": str(factory_root),
            "PATH": str(fake_bin) + os.pathsep + os.environ.get("PATH", ""),
            "PYTHONPATH": str(shim_dir),
            "CI_CLOCK_SLEEP_LOG": str(sleep_path),
            "CI_CLOCK_ACCELERATION": "50",
            "LC_ALL": "C",
            "LANG": "C",
        }
        returncode, stdout, stderr = _run_process(
            [sys.executable, str(gate_path), WORK_NAME],
            env,
            factory_root,
            child_timeout_seconds,
        )
        trace = _load_lines(trace_path)
        sleep_events = _load_lines(sleep_path)
        _assert_trace(trace, case)
        envelope = _assert_envelope(case, returncode, stdout, stderr, sleep_events)
        return {
            "decision": envelope["decision"],
            "feedback": envelope["feedback"],
            "gateExit": returncode,
            "stdout": stdout,
            "stderr": stderr,
            "commandEvents": len(trace),
            "sleepEvents": sleep_events,
            "trace": trace,
        }


def _run_mutation_control(gate_path, child_timeout_seconds):
    """Prove the conflict assertion is causal by disabling it in a copy."""
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c24-mutation-") as temp:
        root = Path(temp)
        script_dir = root / "factory" / "scripts"
        policy_dir = root / "factory" / "docs"
        script_dir.mkdir(parents=True)
        policy_dir.mkdir(parents=True)
        mutated_waiter = script_dir / "ci-wait.py"
        shutil.copy2(gate_path.with_name("ci-wait.py"), mutated_waiter)
        shutil.copy2(
            gate_path.parent.parent / "docs" / "required-checks.json",
            policy_dir / "required-checks.json",
        )
        waiter_source = mutated_waiter.read_text(encoding="utf-8")
        marker = 'if before[2] == "CONFLICTING":'
        if waiter_source.count(marker) != 1:
            raise AssertionError("mutation control marker was not unique")
        mutated_waiter.write_text(
            waiter_source.replace(
                marker,
                'if False and before[2] == "CONFLICTING":',
                1,
            ),
            encoding="utf-8",
        )
        mutated_gate = script_dir / "ci-gate.py"
        shutil.copy2(gate_path, mutated_gate)
        try:
            _run_case(mutated_gate, "conflict", child_timeout_seconds)
        except AssertionError as error:
            if "expected REJECTED" not in str(error):
                raise
            return {
                "status": "killed",
                "mutatedConflictDecisionWasNotRejected": True,
                "detail": str(error),
            }
        raise AssertionError("disabling conflict classification did not break the public control")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--gate", required=True, type=Path)
    parser.add_argument(
        "--cases",
        default=",".join(EXPECTED_DECISIONS),
        help="comma-separated cases: conflict,unknown,stale-head,green",
    )
    parser.add_argument("--child-timeout-seconds", type=float, default=30.0)
    parser.add_argument("--report", type=Path)
    args = parser.parse_args(argv)

    gate_path = args.gate.resolve()
    if not gate_path.is_file():
        raise SystemExit(f"gate does not exist: {gate_path}")
    cases = [case.strip() for case in args.cases.split(",") if case.strip()]
    if not cases or any(case not in EXPECTED_DECISIONS for case in cases):
        raise SystemExit("--cases contains an unsupported case")
    timeout = max(1.0, args.child_timeout_seconds)
    repo_root = Path(__file__).resolve().parents[5]
    waiter_path = gate_path.with_name("ci-wait.py")
    report = {
        "sourceRevision": _source_revision(repo_root),
        "gate": {"path": str(gate_path), "sha256": _sha256(gate_path)},
        "waiter": {"path": str(waiter_path), "sha256": _sha256(waiter_path)},
        "clockShim": {
            "kind": "sitecustomize-process-local-monotonic",
            "requestedSleepIsRecorded": True,
            "acceleration": 50,
            "productionConstantsChanged": False,
        },
        "mutationControl": _run_mutation_control(gate_path, timeout),
        "cases": {},
    }
    for case in cases:
        report["cases"][case] = _run_case(gate_path, case, timeout)

    report_path = args.report.resolve() if args.report else Path(__file__).with_name("public-controls-report.json")
    _write_json(report_path, report)
    print(json.dumps({"status": "passed", "report": str(report_path), "cases": cases}, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
