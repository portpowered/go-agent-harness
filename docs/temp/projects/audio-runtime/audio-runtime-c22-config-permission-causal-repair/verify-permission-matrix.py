#!/usr/bin/env python3
"""Run the bounded C22 config permission cause-and-control matrix.

The exact existing regression runs against the requested source tree without
instrumentation. Diagnostic observations run against a temporary copy with a
temporary same-package test, so the immutable source is never represented as
instrumented evidence.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile
import time


EXACT_TEST = "TestConfigStorageCommitPreservesPermissionsAndPublishesAtomically"
DIAGNOSTIC_TEST = "TestC22PermissionDiagnostic"
EXACT_COMMAND = [
    "go",
    "test",
    "./agent-cli/internal/config",
    "-tags=nomicrophone",
    "-run",
    f"^{EXACT_TEST}$",
    "-count=1",
    "-timeout=90s",
]
DIAGNOSTIC_COMMAND = [
    "go",
    "test",
    "./agent-cli/internal/config",
    "-tags=nomicrophone",
    "-v",
    "-run",
    f"^{DIAGNOSTIC_TEST}$",
    "-count=1",
    "-timeout=90s",
]
DIAGNOSTIC_PREFIX = "C22_DIAGNOSTIC "


DIAGNOSTIC_SOURCE = r'''package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestC22PermissionDiagnostic(t *testing.T) {
	records := []map[string]interface{}{
		runC22PermissionDiagnostic(t, "raw_requested_0640", true, 0o640, false, 0),
		runC22PermissionDiagnostic(t, "explicit_0640", true, 0o600, true, 0o640),
		runC22PermissionDiagnostic(t, "explicit_0600", true, 0o600, true, 0o600),
		runC22PermissionDiagnostic(t, "absent_default", false, 0, false, 0),
	}
	data, err := json.Marshal(records)
	if err != nil {
		t.Fatalf("marshal diagnostic records: %v", err)
	}
	fmt.Printf("C22_DIAGNOSTIC %s\n", data)
}

func runC22PermissionDiagnostic(t *testing.T, name string, exists bool, requested os.FileMode, establish bool, established os.FileMode) map[string]interface{} {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	if exists {
		if err := os.WriteFile(path, []byte("before\n"), requested); err != nil {
			t.Fatalf("%s seed: %v", name, err)
		}
		if establish {
			if err := os.Chmod(path, established); err != nil {
				t.Fatalf("%s chmod %o: %v", name, established, err)
			}
		}
	}
	before := c22PermissionObservation(path)
	storage := NewConfigStorage(path)
	expected, err := storage.Revision()
	if err != nil {
		t.Fatalf("%s revision: %v", name, err)
	}
	if err := storage.Commit(expected, []byte("after\n")); err != nil {
		t.Fatalf("%s commit: %v", name, err)
	}
	after := c22PermissionObservation(path)
	return map[string]interface{}{
		"name":           name,
		"requested_mode": fmt.Sprintf("%03o", requested.Perm()),
		"established_mode": func() string {
			if !establish {
				return ""
			}
			return fmt.Sprintf("%03o", established.Perm())
		}(),
		"before": before,
		"after":  after,
	}
}

func c22PermissionObservation(path string) map[string]interface{} {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]interface{}{"exists": false}
	}
	if err != nil {
		panic(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		panic(err)
	}
	digest := sha256.Sum256(data)
	return map[string]interface{}{
		"exists": true,
		"mode":   fmt.Sprintf("%03o", info.Mode().Perm()),
		"bytes":  len(data),
		"sha256": hex.EncodeToString(digest[:]),
	}
}
'''


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--phase", choices=("baseline", "candidate"), required=True)
    parser.add_argument("--source", required=True, type=Path)
    parser.add_argument("--deadline-seconds", type=float, default=90.0)
    parser.add_argument("--output", type=Path)
    return parser.parse_args()


def git_value(source: Path, *args: str) -> str:
    result = subprocess.run(
        ["git", "-C", str(source), *args],
        check=True,
        capture_output=True,
        text=True,
    )
    return result.stdout.strip()


def child_environment(home: Path) -> dict[str, str]:
    environment = os.environ.copy()
    for name in list(environment):
        upper = name.upper()
        if any(token in upper for token in ("API_KEY", "TOKEN", "PASSWORD", "SECRET")):
            del environment[name]
    environment["HOME"] = str(home)
    environment["USERPROFILE"] = str(home)
    environment["XDG_CONFIG_HOME"] = str(home / ".config")
    return environment


def kill_process_group(process: subprocess.Popen[bytes]) -> None:
    if process.poll() is not None:
        return
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        return
    process.wait()


def run_child(
    command: list[str],
    source: Path,
    mask: int,
    started: float,
    deadline_seconds: float,
    environment: dict[str, str],
) -> dict[str, object]:
    remaining = deadline_seconds - (time.monotonic() - started)
    record: dict[str, object] = {
        "command": command,
        "umask": f"{mask:03o}",
        "cwd": str(source),
    }
    if remaining <= 0:
        record.update({"exit_status": None, "timed_out": True, "stdout": "", "stderr": "deadline exhausted"})
        return record

    launch = time.monotonic()
    process = subprocess.Popen(
        command,
        cwd=source,
        env=environment,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=True,
        preexec_fn=lambda: os.umask(mask),
    )
    timed_out = False
    try:
        stdout, stderr = process.communicate(timeout=remaining)
    except subprocess.TimeoutExpired as error:
        timed_out = True
        kill_process_group(process)
        stdout, stderr = process.communicate()
        if error.stdout:
            stdout = error.stdout
        if error.stderr:
            stderr = error.stderr
    record.update(
        {
            "exit_status": process.returncode,
            "timed_out": timed_out,
            "elapsed_seconds": round(time.monotonic() - launch, 3),
            "stdout": stdout.decode(errors="replace"),
            "stderr": stderr.decode(errors="replace"),
        }
    )
    return record


def copy_source(source: Path, destination: Path) -> None:
    ignored = shutil.ignore_patterns(
        ".git",
        ".claude",
        ".codex",
        "node_modules",
        "coverage",
        "bin",
        "*.test",
    )
    shutil.copytree(source, destination, symlinks=True, ignore=ignored)


def diagnostic_records(source: Path, started: float, deadline_seconds: float, environment: dict[str, str]) -> tuple[list[dict[str, object]], list[dict[str, object]]]:
    process_records: list[dict[str, object]] = []
    records: list[dict[str, object]] = []
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c22-permission-") as temporary:
        overlay = Path(temporary) / "source"
        copy_source(source, overlay)
        diagnostic_path = overlay / "agent-cli/internal/config/c22_permission_diagnostic_test.go"
        diagnostic_path.write_text(DIAGNOSTIC_SOURCE, encoding="utf-8")
        for mask in (0o022, 0o077):
            result = run_child(DIAGNOSTIC_COMMAND, overlay, mask, started, deadline_seconds, environment)
            process_records.append(result)
            for line in str(result.get("stdout", "")).splitlines():
                if line.startswith(DIAGNOSTIC_PREFIX):
                    try:
                        parsed = json.loads(line[len(DIAGNOSTIC_PREFIX) :])
                    except json.JSONDecodeError:
                        continue
                    if isinstance(parsed, list):
                        records.extend({"umask": f"{mask:03o}", **item} for item in parsed if isinstance(item, dict))
    return process_records, records


def validate(phase: str, exact: list[dict[str, object]], diagnostics: list[dict[str, object]]) -> list[str]:
    failures: list[str] = []
    by_mask = {str(record["umask"]): record for record in exact}
    if set(by_mask) != {"022", "077"}:
        failures.append(f"exact test masks = {sorted(by_mask)}, want ['022', '077']")
    for mask in ("022", "077"):
        record = by_mask.get(mask)
        if record is None:
            continue
        expected_status = 0 if phase == "candidate" or mask == "022" else 1
        if record.get("timed_out"):
            failures.append(f"exact test umask {mask} timed out")
        if record.get("exit_status") != expected_status:
            failures.append(f"exact test umask {mask} exit={record.get('exit_status')}, want {expected_status}")
        if phase == "baseline" and mask == "077" and "config mode = 600, want 640" not in str(record.get("stderr", "")) + str(record.get("stdout", "")):
            failures.append("baseline restrictive-mask failure did not identify mode 600 versus 640")

    expected = {
        ("022", "raw_requested_0640"): ("640", "640"),
        ("077", "raw_requested_0640"): ("600", "600"),
        ("022", "explicit_0640"): ("640", "640"),
        ("077", "explicit_0640"): ("640", "640"),
        ("022", "explicit_0600"): ("600", "600"),
        ("077", "explicit_0600"): ("600", "600"),
        ("022", "absent_default"): ("missing", "600"),
        ("077", "absent_default"): ("missing", "600"),
    }
    seen: set[tuple[str, str]] = set()
    for record in diagnostics:
        key = (str(record.get("umask")), str(record.get("name")))
        seen.add(key)
        before = record.get("before")
        after = record.get("after")
        want = expected.get(key)
        if want is None or not isinstance(before, dict) or not isinstance(after, dict):
            failures.append(f"malformed diagnostic record {key}")
            continue
        before_mode = str(before.get("mode", "missing"))
        after_mode = str(after.get("mode", "missing"))
        if (before_mode, after_mode) != want:
            failures.append(f"diagnostic {key}: before/after={(before_mode, after_mode)}, want={want}")
        if after.get("exists") is not True or after.get("bytes") != len("after\n"):
            failures.append(f"diagnostic {key}: published bytes/exists are not complete")
    missing = set(expected) - seen
    if missing:
        failures.append(f"missing diagnostic records: {sorted(missing)}")
    return failures


def main() -> int:
    args = parse_args()
    if os.name != "posix":
        print("C22 permission matrix requires POSIX child umask isolation", file=sys.stderr)
        return 2
    if args.deadline_seconds <= 0 or args.deadline_seconds > 90:
        print("--deadline-seconds must be in (0, 90]", file=sys.stderr)
        return 2
    source = args.source.resolve()
    if not source.is_dir():
        print(f"source is not a directory: {source}", file=sys.stderr)
        return 2
    started = time.monotonic()
    with tempfile.TemporaryDirectory(prefix="audio-runtime-c22-home-") as home:
        environment = child_environment(Path(home))
        exact = [
            run_child(EXACT_COMMAND, source, mask, started, args.deadline_seconds, environment)
            for mask in (0o022, 0o077)
        ]
        diagnostic_processes, diagnostics = diagnostic_records(source, started, args.deadline_seconds, environment)
    failures = validate(args.phase, exact, diagnostics)
    report = {
        "schema": "audio-runtime-c22-permission-matrix-v1",
        "phase": args.phase,
        "source": str(source),
        "source_revision": git_value(source, "rev-parse", "HEAD"),
        "deadline_seconds": args.deadline_seconds,
        "elapsed_seconds": round(time.monotonic() - started, 3),
        "exact_test": exact,
        "diagnostic_processes": diagnostic_processes,
        "diagnostics": diagnostics,
        "status": "passed" if not failures else "failed",
        "failures": failures,
    }
    encoded = json.dumps(report, indent=2, sort_keys=True) + "\n"
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(encoded, encoding="utf-8")
    print(encoded, end="")
    return 0 if not failures else 1


if __name__ == "__main__":
    raise SystemExit(main())
