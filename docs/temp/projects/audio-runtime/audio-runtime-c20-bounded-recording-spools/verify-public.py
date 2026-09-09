#!/usr/bin/env python3
"""Run the C20 public consumer against candidate and pinned pre-fix source."""

import argparse
import hashlib
import json
import os
import signal
import shutil
import subprocess
import sys
import tarfile
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[5]
CONSUMER = Path(__file__).with_name("consumer.go")
BASELINE = "e4137eba6a6499142f50701609c1149afc71db84"
SMALL_LIMITS = {
    "transcript_limit": 32768,
    "transcript_items": 128,
    "audio_limit": 32768,
    "audio_items": 32,
    "provider_limit": 4096,
    "provider_items": 4,
}


def run_bounded(argv, cwd, timeout=60, env=None):
    process = subprocess.Popen(
        argv, cwd=cwd, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        text=True, start_new_session=True, env=env,
    )
    try:
        stdout, stderr = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        os.killpg(process.pid, signal.SIGKILL)
        stdout, stderr = process.communicate()
        raise RuntimeError(f"timeout after {timeout}s: {' '.join(argv)}\n{stderr[-2000:]}")
    if process.returncode != 0:
        raise RuntimeError(f"exit {process.returncode}: {' '.join(argv)}\n{stderr[-4000:]}")
    return stdout, stderr


def archive_revision(revision, destination):
    archive = subprocess.Popen(["git", "archive", revision], cwd=ROOT, stdout=subprocess.PIPE)
    try:
        with archive.stdout:
            with tarfile.open(fileobj=archive.stdout, mode="r|") as stream:
                stream.extractall(destination)
        if archive.wait() != 0:
            raise RuntimeError(f"git archive failed for {revision}")
    finally:
        if archive.poll() is None:
            archive.kill()


def module_file(root):
    return f'''module c20-public-consumer

go 1.26.7

require (
    github.com/portpowered/go-agent-harness/go-agent-runtime v0.0.0
    github.com/portpowered/go-agent-harness/go-agent-loop v0.0.0
    github.com/portpowered/go-agent-harness/go-audio v0.0.0
    github.com/portpowered/go-agent-harness/go-llm-gateway v0.0.0
)

replace github.com/portpowered/go-agent-harness/go-agent-runtime => {root / 'go-agent-runtime'}
replace github.com/portpowered/go-agent-harness/go-agent-loop => {root / 'go-agent-loop'}
replace github.com/portpowered/go-agent-harness/go-audio => {root / 'go-audio'}
replace github.com/portpowered/go-agent-harness/go-llm-gateway => {root / 'go-llm-gateway'}
'''


def build_and_run(source_root, case_name, args):
    with tempfile.TemporaryDirectory(prefix="c20-public-consumer-") as temp:
        temp_root = Path(temp)
        module_root = temp_root / "module"
        module_root.mkdir()
        shutil.copy2(CONSUMER, module_root / "consumer.go")
        (module_root / "go.mod").write_text(module_file(source_root), encoding="utf-8")
        binary = module_root / "consumer"
        env = dict(os.environ)
        env["GOWORK"] = "off"
        run_bounded(["go", "build", "-trimpath", "-mod=mod", "-o", str(binary), "consumer.go"], module_root, 60, env)
        output_root = temp_root / "outputs"
        output_root.mkdir()
        destination = output_root / "capture"
        command = [str(binary), "--case", case_name, "--destination", str(destination), "--source-revision", args.revision]
        if args.transcript_limit:
            command += ["--transcript-limit", str(args.transcript_limit)]
        if args.transcript_items:
            command += ["--transcript-items", str(args.transcript_items)]
        if args.audio_limit:
            command += ["--audio-limit", str(args.audio_limit)]
        if args.audio_items:
            command += ["--audio-items", str(args.audio_items)]
        if args.provider_limit:
            command += ["--provider-limit", str(args.provider_limit)]
        if args.provider_items:
            command += ["--provider-items", str(args.provider_items)]
        stdout, _ = run_bounded(command, module_root, 60, env)
        line = stdout.strip().splitlines()[-1]
        return json.loads(line), hashlib.sha256(binary.read_bytes()).hexdigest()


def candidate_result(case_name, args):
    return build_and_run(ROOT, case_name, args)


def baseline_result(case_name, revision, args):
    with tempfile.TemporaryDirectory(prefix="c20-baseline-") as temp:
        source_root = Path(temp) / "source"
        source_root.mkdir()
        archive_revision(revision, source_root)
        return build_and_run(source_root, case_name, args)


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def apply_small_limits(args):
    values = vars(args).copy()
    for name, value in SMALL_LIMITS.items():
        if values[name] == 0:
            values[name] = value
    return argparse.Namespace(**values)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--case", required=True)
    parser.add_argument("--revision", default="")
    parser.add_argument("--transcript-limit", type=int, default=0)
    parser.add_argument("--transcript-items", type=int, default=0)
    parser.add_argument("--audio-limit", type=int, default=0)
    parser.add_argument("--audio-items", type=int, default=0)
    parser.add_argument("--provider-limit", type=int, default=0)
    parser.add_argument("--provider-items", type=int, default=0)
    args = parser.parse_args()
    run_args = apply_small_limits(args) if args.case in {"baseline", "many-small", "large-record", "provider-overflow"} else args
    if args.case == "baseline":
        revision = args.revision or BASELINE
        before, before_binary = baseline_result("many-small", revision, run_args)
        after, after_binary = candidate_result("many-small", run_args)
        require(before["total_bytes"] > after["total_bytes"], f"baseline did not expose cumulative growth: before={before} after={after}")
        require(before.get("status") == "complete", f"baseline control was unexpectedly partial: {before}")
        require(after.get("status") == "partial", f"candidate did not publish partial overflow: {after}")
        output = {"case": "baseline", "before": before, "after": after, "before_binary": before_binary, "after_binary": after_binary}
    else:
        after, binary = candidate_result(args.case, run_args)
        if args.case == "normal":
            require(after.get("status") == "complete", f"normal public capture was not complete: {after}")
            require(not after.get("finalization_error"), f"normal finalization failed: {after}")
        if args.case in {"many-small", "large-record"}:
            require(after.get("status") == "partial", f"overflow case status: {after}")
        if args.case == "provider-overflow":
            require(bool(after.get("provider_error")), f"provider overflow was not causal: {after}")
        if args.case == "default-overflow":
            require(after.get("status") == "partial", f"default-limit overflow status: {after}")
            require("budget" in after.get("finalization_error", "").lower(), f"default-limit overflow was not causal: {after}")
            require(all(value == 0 for value in after.get("limits_applied", {}).values()), f"default-limit case did not use zero requested limits: {after}")
        output = {"case": args.case, "result": after, "binary": binary}
    print(json.dumps(output, sort_keys=True))


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
