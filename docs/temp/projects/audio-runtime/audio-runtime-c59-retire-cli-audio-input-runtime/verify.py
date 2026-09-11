#!/usr/bin/env python3
"""Independent, task-local checks for the C59 audio-input retirement slice."""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
from pathlib import Path


EVIDENCE_ROOT = Path(__file__).resolve().parent
ROOT = EVIDENCE_ROOT.parents[4]
TASK = "audio-runtime-c59-retire-cli-audio-input-runtime"
BRANCH = "codex/audio-runtime-c59-retire-cli-audio-input-runtime"
BASELINE = "3194edd97aed588f7cdf2f8c58a69ac21da4c9ad"
STARTUP = "8bdafc7f947a3a2c9856220abdc539437035bd21"
MAIN = "904e1f4c3be6c1e629138632573bd2fb55d50938"
CLI_FILES = {
    "agent-cli/internal/services/internal/agentruntime/session_audio_in.go": 1033,
    "agent-cli/internal/services/internal/agentruntime/session_audio_dispatch.go": 37,
    "agent-cli/internal/services/internal/agentruntime/session_audio_format.go": 84,
    "agent-cli/internal/services/internal/agentruntime/session_audio_rate.go": 68,
    "agent-cli/internal/services/internal/agentruntime/session_audio_trace.go": 96,
}
CLI_ALLOWED = {
    "agent-cli/internal/services/internal/agentruntime/session_audio_in.go",
    # These retired adapters are intentionally deleted by this task and must
    # remain in the provenance allowlist as deleted, task-owned paths.
    "agent-cli/internal/services/internal/agentruntime/session_audio_dispatch.go",
    "agent-cli/internal/services/internal/agentruntime/session_audio_format.go",
    "agent-cli/internal/services/internal/agentruntime/session_audio_rate.go",
    "agent-cli/internal/services/internal/agentruntime/session_audio_trace.go",
    "agent-cli/internal/services/internal/agentruntime/session_audio_in_internal_test.go",
    "agent-cli/internal/services/internal/agentruntime/session_audio_rate_test.go",
    "agent-cli/internal/services/internal/agentruntime/session_live_termination_test.go",
    # This is a shrinking compatibility alias for the existing scheduled-input
    # name; it does not add a second CLI lifecycle implementation.
    "agent-cli/internal/services/internal/agentruntime/session_diagnostics.go",
}
FORBIDDEN_RUNTIME = (
    "agent-cli/",
    "internal/webmcp",
    "github.com/spf13/pflag",
    "github.com/spf13/cobra",
    "go-device-gateway",
    '"os"',
)


def fail(message: str) -> "NoReturn":
    raise SystemExit(f"C59 verification failed: {message}")


def command_text(argv: list[str]) -> str:
    return " ".join(str(part) for part in argv)


def run(argv: list[str], cwd: Path = ROOT, *, env: dict[str, str] | None = None, timeout: int = 180) -> subprocess.CompletedProcess[str]:
    print(f"$ {command_text(argv)}")
    merged = os.environ.copy()
    if env:
        merged.update(env)
    try:
        result = subprocess.run(argv, cwd=cwd, env=merged, text=True, capture_output=True, timeout=timeout)
    except subprocess.TimeoutExpired as exc:
        fail(f"timeout after {timeout}s: {command_text(argv)} ({exc})")
    if result.stdout:
        print(result.stdout.rstrip())
    if result.stderr:
        print(result.stderr.rstrip(), file=sys.stderr)
    if result.returncode != 0:
        fail(f"exit {result.returncode}: {command_text(argv)}")
    return result


def expect_failure(argv: list[str], cwd: Path, env: dict[str, str]) -> None:
    print(f"$ (expected failure) {command_text(argv)}")
    merged = os.environ.copy()
    merged.update(env)
    result = subprocess.run(argv, cwd=cwd, env=merged, text=True, capture_output=True, timeout=180)
    if result.stdout:
        print(result.stdout.rstrip())
    if result.stderr:
        print(result.stderr.rstrip(), file=sys.stderr)
    if result.returncode == 0:
        fail(f"mutation oracle unexpectedly passed: {command_text(argv)}")


def line_count(path: Path) -> int:
    return len(path.read_text(encoding="utf-8").splitlines())


def baseline_counts() -> dict[str, int]:
    counts: dict[str, int] = {}
    for relative, expected in CLI_FILES.items():
        result = run(["git", "show", f"{MAIN}:{relative}"], timeout=30)
        counts[relative] = len(result.stdout.splitlines())
        if counts[relative] != expected:
            fail(f"baseline {relative} = {counts[relative]} lines, want {expected}")
    return counts


def current_cli_census(args: argparse.Namespace) -> None:
    baseline_counts()
    existing = [relative for relative in CLI_FILES if (ROOT / relative).exists()]
    current_lines = sum(line_count(ROOT / relative) for relative in existing)
    retired = len(CLI_FILES) - len(existing)
    baseline_lines = sum(CLI_FILES.values())
    if len(existing) > args.max_files or current_lines > args.max_lines:
        fail(f"CLI census is {len(existing)} files/{current_lines} lines; limit is {args.max_files}/{args.max_lines}")
    if retired < args.min_file_reduction or baseline_lines - current_lines < args.min_line_reduction:
        fail(f"retirement is {retired} files/{baseline_lines - current_lines} lines; minimum is {args.min_file_reduction}/{args.min_line_reduction}")
    print(f"retirement census: baseline={len(CLI_FILES)} files/{baseline_lines} lines current={len(existing)} files/{current_lines} lines retired={retired} files/{baseline_lines-current_lines} lines")


def assert_ancestors() -> None:
    for revision in (BASELINE, STARTUP, MAIN):
        run(["git", "merge-base", "--is-ancestor", revision, "HEAD"], timeout=30)


def execution_main_revision() -> str:
    """Return the accepted execution base used for current-scope accounting.

    MAIN remains the pinned planning revision for the admitted baseline and
    read-only caller oracle.  Once accepted work lands on main, provenance
    accounting must start at the refreshed remote main instead of charging
    those already-accepted production changes to this task.
    """
    return run(["git", "rev-parse", "--verify", "origin/main^{commit}"], timeout=30).stdout.strip()


def assert_runtime_scope() -> None:
    root = ROOT / "go-agent-runtime/services/audioinput"
    if not root.exists():
        fail("public audioinput runtime directory is missing")
    for path in root.rglob("*.go"):
        if path.name.endswith("_test.go"):
            continue
        text = path.read_text(encoding="utf-8")
        for forbidden in FORBIDDEN_RUNTIME:
            if forbidden in text:
                fail(f"forbidden runtime dependency {forbidden!r} in {path.relative_to(ROOT)}")
    if "webmcp" in "\n".join(path.read_text(encoding="utf-8") for path in root.rglob("*.go")):
        fail("runtime audioinput package mentions webmcp")


def focused_runtime_tests(pattern: str, race: bool = False) -> None:
    argv = ["go", "test"]
    if race:
        argv.append("-race")
    argv += ["-count=1", "-timeout=180s", "./services/audioinput/...", "-run", pattern]
    run(argv, ROOT / "go-agent-runtime", timeout=240 if race else 180)


def focused_cli_tests(race: bool = False) -> None:
    argv = ["go", "test"]
    if race:
        argv.append("-race")
    argv += ["-count=1", "-timeout=240s", "./internal/services/internal/agentruntime", "-run", "AudioInput|ScheduledAudio|AudioFormat|AudioRate|AudioTrace|ImagesAndAudio|Recording.*Audio"]
    run(argv, ROOT / "agent-cli", timeout=300 if race else 240)


def consumer_mutations(names: list[str]) -> None:
    consumer = EVIDENCE_ROOT / "consumer"
    argv = ["go", "test", "-count=1", "-timeout=120s", "./..."]
    for name in names:
        expect_failure(argv, consumer, {"GOWORK": "off", "C59_MUTATION": name})


def baseline_callers_oracles() -> None:
    baseline_counts()
    assert_ancestors()
    run(["git", "diff", "--exit-code", MAIN, "--", "agent-cli/internal/services/internal/agentruntime/service.go", "agent-cli/internal/services/internal/agentruntime/session_image.go", "agent-cli/internal/services/internal/agentruntime/session_recording.go", "agent-cli/internal/services/internal/agentruntime/session_live.go", "agent-cli/internal/services/internal/agentruntime/session_browser_scenario.go"], timeout=30)
    focused_runtime_tests("Validate|Source|PCM|WAV|Format|Rate|Reader|Close")
    print("baseline, ancestry, read-only caller and runtime oracle checks passed")


def source_format_rate_mutations() -> None:
    assert_runtime_scope()
    focused_runtime_tests("Validate|Source|PCM|WAV|Format|Rate|Reader|Close")
    focused_runtime_tests("Validate|Source|PCM|WAV|Format|Rate|Reader|Close", race=True)
    consumer_mutations(["pcm-order", "rate"])
    print("source/format/rate positive and deliberate wrong-oracle controls passed")


def stream_turn_cleanup_mutations() -> None:
    assert_runtime_scope()
    focused_runtime_tests("Stream|Dispatch|Convert|Pace|Frame|EndOfTurn|EOF|Cancellation|Read|Send|Close")
    focused_runtime_tests("Stream|Dispatch|Convert|Pace|Frame|EndOfTurn|EOF|Cancellation|Read|Send|Close", race=True)
    consumer_mutations(["end-of-turn", "cleanup"])
    print("stream/turn/cleanup positive and deliberate wrong-oracle controls passed")


def scheduled_trace_mutations() -> None:
    assert_runtime_scope()
    focused_runtime_tests("Scheduled|Dispatch|Observation|Trace|Fanout")
    focused_runtime_tests("Scheduled|Dispatch|Observation|Trace|Fanout", race=True)
    focused_cli_tests()
    consumer_mutations(["redaction"])
    print("scheduled/trace positive and deliberate wrong-oracle controls passed")


def wire_embedding_scope() -> None:
    assert_runtime_scope()
    providers = (ROOT / "go-agent-runtime/services/audioinput/wire/providers.go").read_text(encoding="utf-8")
    generated = (ROOT / "go-agent-runtime/services/audioinput/wire/wire_gen.go").read_text(encoding="utf-8")
    if "internal/stream" not in providers or "stream.New" not in generated:
        fail("audioinput Wire graph does not explicitly embed the private stream service")
    if "Code generated by Wire" not in generated:
        fail("wire_gen.go is missing the generated header")
    mod = (EVIDENCE_ROOT / "consumer/go.mod").read_text(encoding="utf-8")
    allowed = ("go-agent-runtime", "go-audio", "go-agent-loop")
    for line in mod.splitlines():
        if line.startswith("require github.com/portpowered/go-agent-harness/") and not any(item in line for item in allowed):
            fail(f"consumer imports non-public contract: {line}")
    consumer_mutations(["end-of-turn"])
    print("Wire and separate GOWORK=off public-contract scope passed")


def formatting_owned_go() -> None:
    paths = [ROOT / relative for relative in ("agent-cli/internal/services/internal/agentruntime/session_audio_in.go", "agent-cli/internal/services/internal/agentruntime/session_audio_trace.go")]
    paths += sorted((ROOT / "go-agent-runtime/services/audioinput").rglob("*.go"))
    result = subprocess.run(["gofmt", "-l", *map(str, paths)], cwd=ROOT, text=True, capture_output=True, timeout=60)
    if result.returncode != 0 or result.stdout.strip():
        fail(f"gofmt reported owned files: {result.stdout or result.stderr}")
    total = sum(line_count(path) for path in paths[:2])
    if total > 400:
        fail(f"owned CLI production is {total} lines")
    print(f"owned formatting and census: {total} CLI production lines")


def final_scope_provenance_budget() -> None:
    branch = run(["git", "branch", "--show-current"], timeout=30).stdout.strip()
    if branch != BRANCH:
        fail(f"branch is {branch!r}, want {BRANCH!r}")
    manifest = json.loads((ROOT / "prd.json").read_text(encoding="utf-8"))
    if manifest.get("branchName") != BRANCH or manifest.get("project") != "audio-runtime":
        fail("admitted manifest branch/project mismatch")
    assert_ancestors()
    current_cli_census(argparse.Namespace(max_files=2, max_lines=400, min_file_reduction=3, min_line_reduction=918))
    run(["git", "diff", "--check"], timeout=30)
    execution_main = execution_main_revision()
    run(["git", "merge-base", "--is-ancestor", execution_main, "HEAD"], timeout=30)
    changed = set(run(["git", "diff", "--name-only", execution_main], timeout=30).stdout.splitlines())
    forbidden = []
    for path in changed:
        if path.startswith("agent-cli/") and path.endswith(".go") and not path.endswith("_test.go") and path not in CLI_ALLOWED:
            forbidden.append(path)
    if forbidden:
        fail(f"unowned CLI production changes: {sorted(forbidden)}")
    if (EVIDENCE_ROOT / "waiver.json").exists() or (EVIDENCE_ROOT / "second-project").exists():
        fail("task evidence contains a waiver or second project")
    print(f"final branch, ancestry, manifest, scope, diff and provenance budget passed (execution main {execution_main})")


def cli_retirement(args: argparse.Namespace) -> None:
    current_cli_census(args)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", required=True, choices=["baseline-callers-oracles", "source-format-rate-mutations", "stream-turn-cleanup-mutations", "scheduled-trace-mutations", "cli-retirement", "wire-embedding-scope", "formatting-owned-go", "final-scope-provenance-budget"])
    parser.add_argument("--baseline-files", type=int, default=5)
    parser.add_argument("--baseline-lines", type=int, default=1318)
    parser.add_argument("--max-files", type=int, default=2)
    parser.add_argument("--max-lines", type=int, default=400)
    parser.add_argument("--min-file-reduction", type=int, default=3)
    parser.add_argument("--min-line-reduction", type=int, default=918)
    args = parser.parse_args()
    if args.baseline_files != 5 or args.baseline_lines != 1318:
        fail("this verifier only accepts the admitted five-file/1,318-line baseline")
    modes = {
        "baseline-callers-oracles": baseline_callers_oracles,
        "source-format-rate-mutations": source_format_rate_mutations,
        "stream-turn-cleanup-mutations": stream_turn_cleanup_mutations,
        "scheduled-trace-mutations": scheduled_trace_mutations,
        "wire-embedding-scope": wire_embedding_scope,
        "formatting-owned-go": formatting_owned_go,
        "final-scope-provenance-budget": final_scope_provenance_budget,
    }
    if args.mode == "cli-retirement":
        cli_retirement(args)
    else:
        modes[args.mode]()
    print(f"PASS {args.mode}")


if __name__ == "__main__":
    main()
