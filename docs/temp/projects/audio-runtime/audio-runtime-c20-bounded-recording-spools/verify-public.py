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
CASE_LIMITS = {
    "semantic-boundaries": {"transcript_limit": 500, "transcript_items": 8},
    "encoded-expansion": {"transcript_limit": 262144, "transcript_items": 8},
    "overflow-healthy-terminal": {"transcript_limit": 4096, "transcript_items": 8, "audio_limit": 32768, "audio_items": 32},
    "provider-overflow": {"provider_limit": 4096, "provider_items": 4},
    "provider-overflow-controls": {"provider_limit": 4096, "provider_items": 4},
}
SMALL_LIMIT_CASES = {"baseline", "many-small", "large-record"}
DEFAULT_TRANSCRIPT_BYTES = 64 << 20
DEFAULT_SUMMARY_BYTES = 2 << 20
DEFAULT_QUEUE_BYTES = 16 << 20


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
    values = vars(args).copy()
    values["revision"] = git_revision()
    return build_and_run(ROOT, case_name, argparse.Namespace(**values))


def baseline_result(case_name, revision, args):
    with tempfile.TemporaryDirectory(prefix="c20-baseline-") as temp:
        source_root = Path(temp) / "source"
        source_root.mkdir()
        archive_revision(revision, source_root)
        return build_and_run(source_root, case_name, args)


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def apply_case_limits(args):
    values = vars(args).copy()
    if args.case in SMALL_LIMIT_CASES:
        for name, value in SMALL_LIMITS.items():
            if values[name] == 0:
                values[name] = value
    for name, value in CASE_LIMITS.get(args.case, {}).items():
        if values[name] == 0:
            values[name] = value
    return argparse.Namespace(**values)


def git_revision():
    return subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()


def usage(result, name="semantic_usage"):
    value = result.get(name)
    require(isinstance(value, dict), f"{name} instrumentation is missing: {result}")
    return value


def assert_common_instrumentation(result):
    semantic = usage(result)
    disk = result.get("disk_usage", {})
    finalization = result.get("finalization", {})
    require(semantic.get("queue_bytes") == 0 and semantic.get("queue_items") == 0, f"semantic queue did not drain: {result}")
    require(semantic.get("peak_queue_bytes", 0) <= DEFAULT_QUEUE_BYTES, f"semantic queue exceeded bound: {result}")
    require(semantic.get("accepted_items", 0) >= semantic.get("processed_items", 0), f"semantic accounting regressed: {result}")
    require(semantic.get("processed_items", 0) > 0, f"semantic processing was not measured: {result}")
    require(disk.get("samples", 0) > 0, f"disk sampling was not measured: {result}")
    require(disk.get("peak_owned_bytes", 0) >= disk.get("peak_final_bytes", 0), f"disk ownership accounting regressed: {result}")
    require("duration_ms" in finalization and "allocated_bytes" in finalization, f"finalization instrumentation is incomplete: {result}")


def assert_paired_transcripts(result):
    lines = result.get("transcript_lines", {})
    require(lines.get("client.transcript.jsonl") == lines.get("agent.transcript.jsonl"), f"paired transcript line counts diverged: {result}")
    hashes = result.get("transcript_sha256", {})
    require(set(hashes) == {"client.transcript.jsonl", "agent.transcript.jsonl"}, f"paired transcript hashes are incomplete: {result}")
    require(all(len(value) == 64 for value in hashes.values()), f"transcript hashes are not SHA-256: {result}")


def assert_partial_budget(result):
    require(result.get("status") == "partial", f"expected truthful partial status: {result}")
    require("budget" in result.get("finalization_error", "").lower(), f"missing causal budget error: {result}")
    assert_common_instrumentation(result)
    assert_paired_transcripts(result)


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
    run_args = apply_case_limits(args)
    if args.case == "baseline":
        revision = args.revision or BASELINE
        before, before_binary = baseline_result("many-small", revision, run_args)
        after, after_binary = candidate_result("many-small", run_args)
        require(before["total_bytes"] > after["total_bytes"], f"baseline did not expose cumulative growth: before={before} after={after}")
        require(before.get("status") == "complete", f"baseline control was unexpectedly partial: {before}")
        assert_partial_budget(after)
        require(before.get("source_revision") == revision, f"baseline provenance drifted: {before}")
        require(after.get("source_revision") == git_revision(), f"candidate provenance drifted: {after}")
        output = {"case": "baseline", "before": before, "after": after, "before_binary": before_binary, "after_binary": after_binary}
    else:
        after, binary = candidate_result(args.case, run_args)
        if args.case == "normal":
            require(after.get("status") == "complete", f"normal public capture was not complete: {after}")
            require(not after.get("finalization_error"), f"normal finalization failed: {after}")
            assert_common_instrumentation(after)
            assert_paired_transcripts(after)
            require(after.get("provider_capture_valid"), f"normal provider capture was not valid: {after}")
            require(after.get("provider_sequences") == [1, 2, 3], f"normal provider order changed: {after}")
            semantic = usage(after)
            require(semantic.get("accepted_messages") == 1 and semantic.get("accepted_audio") == 1 and semantic.get("accepted_events") == 1, f"normal event accounting is incomplete: {after}")
            require(semantic.get("transcript_bytes", 0) > 0 and semantic.get("audio_bytes") == 8 and semantic.get("terminal_items") == 1, f"normal cumulative accounting is incomplete: {after}")
            require(0 < semantic.get("summary_bytes", 0) <= DEFAULT_SUMMARY_BYTES, f"normal summary accounting is unbounded: {after}")
        if args.case in {"many-small", "large-record"}:
            assert_partial_budget(after)
            limit = run_args.transcript_limit
            semantic = usage(after)
            require(semantic.get("transcript_bytes", 0) <= limit and semantic.get("transcript_items", 0) <= run_args.transcript_items, f"small transcript limit was exceeded: {after}")
            require(semantic.get("terminal_items") == 1, f"terminal reserve was not retained: {after}")
        if args.case == "semantic-boundaries":
            assert_partial_budget(after)
            semantic = usage(after)
            require(semantic.get("transcript_items") == 1, f"transcript byte boundary did not preserve one record: {after}")
            require(semantic.get("transcript_bytes") == run_args.transcript_limit == 500, f"transcript byte boundary was not exact: {after}")
            require(semantic.get("terminal_items") == 1, f"terminal boundary was not retained: {after}")
            item_values = vars(run_args).copy()
            item_values["transcript_limit"] = 4096
            item_values["transcript_items"] = 1
            item_after, _ = candidate_result(args.case, argparse.Namespace(**item_values))
            assert_partial_budget(item_after)
            item_semantic = usage(item_after)
            require(item_semantic.get("transcript_items") == 1 and item_semantic.get("transcript_bytes", 0) < 4096, f"transcript item boundary was not exact: {item_after}")
            require(item_semantic.get("terminal_items") == 1, f"terminal item boundary lost terminal evidence: {item_after}")
            output = {"case": args.case, "byte_boundary": after, "item_boundary": item_after, "binary": binary}
            print(json.dumps(output, sort_keys=True))
            return
        if args.case == "encoded-expansion":
            assert_partial_budget(after)
            semantic = usage(after)
            require(semantic.get("transcript_items", 0) == 1, f"encoded expansion did not preserve one complete prefix record: {after}")
            require(semantic.get("transcript_bytes", 0) <= run_args.transcript_limit, f"encoded transcript bytes exceeded limit: {after}")
        if args.case == "overflow-healthy-terminal":
            assert_partial_budget(after)
            semantic = usage(after)
            require(semantic.get("terminal_items") == 1, f"healthy terminal was not retained after overflow: {after}")
            require(semantic.get("terminal_bytes", 0) > 0, f"terminal evidence was not measured: {after}")
        if args.case == "summary-only-overflow":
            require(after.get("status") == "partial", f"summary-only overflow status: {after}")
            require("summary" in after.get("finalization_error", "").lower(), f"summary-only overflow was not causal: {after}")
            assert_common_instrumentation(after)
            assert_paired_transcripts(after)
            semantic = usage(after)
            require(0 < semantic.get("transcript_bytes", 0) < DEFAULT_TRANSCRIPT_BYTES, f"summary-only overflow consumed raw default: {after}")
            require(0 < semantic.get("summary_bytes", 0) <= DEFAULT_SUMMARY_BYTES, f"summary retention exceeded its bound: {after}")
            require(semantic.get("audio_bytes") == 8 and semantic.get("terminal_items") == 1, f"raw evidence after summary overflow was lost: {after}")
        if args.case in {"provider-boundaries", "provider-settlement"}:
            require(after.get("status") == "complete" and not after.get("provider_error") and not after.get("finalization_error"), f"provider settlement was not complete: {after}")
            assert_common_instrumentation(after)
            require(after.get("provider_capture_valid"), f"provider settlement did not publish a valid capture: {after}")
            require(after.get("provider_sequences") == [1, 3], f"discarded provider event was published: {after}")
            provider = usage(after, "provider_usage")
            require(provider.get("provider_items") == 2 and provider.get("provider_accepted_items") == 3, f"provider settlement accounting is incomplete: {after}")
            require(provider.get("queue_items") == 0 and provider.get("provider_queue_items") == 0, f"provider queue did not drain: {after}")
        if args.case in {"provider-overflow", "provider-overflow-controls"}:
            require(bool(after.get("provider_error")), f"provider overflow was not causal: {after}")
            require("budget" in after.get("provider_error", "").lower(), f"provider overflow error lost its budget cause: {after}")
            assert_common_instrumentation(after)
            provider = usage(after, "provider_usage")
            require(provider.get("queue_items") == 0 and provider.get("provider_queue_items") == 0, f"provider overflow stranded queue state: {after}")
            require(not after.get("provider_capture_valid") and not after.get("provider_sequences"), f"provider overflow published a normal envelope: {after}")
        if args.case == "cleanup-failures":
            require(after.get("status") == "complete" and not after.get("finalization_error") and not after.get("provider_error"), f"cleanup case was not complete: {after}")
            assert_common_instrumentation(after)
            require(not after.get("repeat_finalization_error") and not after.get("repeat_provider_error"), f"repeated cleanup was not idempotent: {after}")
            require(after.get("provider_capture_valid"), f"cleanup case lost provider capture: {after}")
        if args.case == "publication-resources":
            require(after.get("status") == "complete" and not after.get("finalization_error"), f"publication resource case was not complete: {after}")
            assert_common_instrumentation(after)
            semantic = usage(after)
            require(semantic.get("audio_bytes") == 8 and after.get("disk_usage", {}).get("peak_owned_bytes", 0) > 0, f"publication resource measurements are incomplete: {after}")
        if args.case == "composition":
            require(after.get("status") == "complete" and not after.get("finalization_error"), f"composition case was not complete: {after}")
            assert_common_instrumentation(after)
            require(usage(after).get("accepted_events") == 2, f"composition checkpoint event was not recorded: {after}")
        if args.case == "audio-tool":
            require(after.get("status") == "complete" and not after.get("finalization_error"), f"audio/tool case was not complete: {after}")
            assert_common_instrumentation(after)
            require(usage(after).get("audio_bytes") == 16 and usage(after).get("audio_items") == 2, f"audio/tool PCM accounting is incomplete: {after}")
        if args.case == "interruption":
            require(after.get("status") == "complete" and not after.get("finalization_error"), f"interruption case was not complete: {after}")
            assert_common_instrumentation(after)
            require(len(after.get("input_errors", [])) == 1 and "canceled" in after["input_errors"][0], f"cancellation was not observed: {after}")
        if args.case == "ask":
            require(after.get("status") == "complete" and not after.get("finalization_error"), f"ask case was not complete: {after}")
            assert_common_instrumentation(after)
            require(usage(after).get("accepted_messages") == 2 and usage(after).get("summary_bytes", 0) > 0, f"ask input/output evidence is incomplete: {after}")
        if args.case in {"default-overflow", "cumulative-overflow"}:
            assert_partial_budget(after)
            require(all(value == 0 for value in after.get("limits_applied", {}).values()), f"default-limit case did not use zero requested limits: {after}")
            semantic = usage(after)
            require(DEFAULT_TRANSCRIPT_BYTES * 9 // 10 <= semantic.get("transcript_bytes", 0) < DEFAULT_TRANSCRIPT_BYTES, f"default raw transcript accounting was not bounded near its protected budget: {after}")
            require(f"/{DEFAULT_TRANSCRIPT_BYTES}" in after.get("finalization_error", ""), f"default overflow did not report its protected limit: {after}")
            require(semantic.get("transcript_items", 0) > 0 and semantic.get("terminal_items") == 1, f"default overflow evidence accounting is incomplete: {after}")
            disk = after.get("disk_usage", {})
            require(disk.get("peak_staging_bytes", 0) > 0 and disk.get("peak_temporary_bytes", 0) > 0, f"default publication copies were not measured: {after}")
            require(after.get("finalization", {}).get("allocated_bytes", 0) > 0, f"default finalization allocation was not measured: {after}")
        output = {"case": args.case, "result": after, "binary": binary}
    print(json.dumps(output, sort_keys=True))


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
