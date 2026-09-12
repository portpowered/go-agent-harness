#!/usr/bin/env python3
"""Run focused causal tests and prove three lifecycle mutations are caught."""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import tempfile
import argparse
from pathlib import Path


ROOT = Path(__file__).resolve().parents[5]
SERVICE = ROOT / "go-agent-runtime/services/sessionfinalization/internal/service/service.go"
PACKAGE = "./services/sessionfinalization/..."


def run(command: list[str], cwd: Path) -> subprocess.CompletedProcess[str]:
    env = os.environ.copy()
    env["GOWORK"] = "off"
    return subprocess.run(command, cwd=cwd, env=env, text=True, capture_output=True, check=False)


def focused() -> list[dict[str, object]]:
    results = []
    for args in (["go", "test", PACKAGE, "-count=1"], ["go", "test", "-race", PACKAGE, "-count=1"]):
        result = run(args, ROOT / "go-agent-runtime")
        results.append({"command": " ".join(args), "status": result.returncode, "output": result.stdout + result.stderr})
        if result.returncode:
            raise SystemExit(result.stdout + result.stderr)
    return results


def mutation_results() -> list[dict[str, object]]:
    source = SERVICE.read_text()
    mutations = {
        "drop-cleanup-error": (
            "return errors.Join(primary, f.Cleanup(ctx, out))",
            "return primary",
        ),
        "reverse-finalization-order": (
            '\tadd(f.phase("close session capabilities", call(f.req.CloseCapabilities)))\n\tadd(f.phase("close WebRTC provider session", call(f.req.CloseSession)))',
            '\tadd(f.phase("close WebRTC provider session", call(f.req.CloseSession)))\n\tadd(f.phase("close session capabilities", call(f.req.CloseCapabilities)))',
        ),
        "mixed-tree-is-clean": (
            "if !errorTreeOnly(cause, allowed) {\n\t\t\t\treturn false\n\t\t\t}",
            "if !errorTreeOnly(cause, allowed) {\n\t\t\t\tcontinue\n\t\t\t}",
        ),
    }
    results = []
    for name, (needle, replacement) in mutations.items():
        if needle not in source:
            raise SystemExit(f"mutation anchor missing: {name}")
        workspace = Path(tempfile.mkdtemp(prefix="c104-mutant-"))
        worktree = workspace / "tree"
        try:
            added = run(["git", "worktree", "add", "--detach", str(worktree), "HEAD"], ROOT)
            if added.returncode:
                raise SystemExit(added.stdout + added.stderr)
            mutant = worktree / SERVICE.relative_to(ROOT)
            mutant.write_text(source.replace(needle, replacement, 1))
            result = run(["go", "test", PACKAGE, "-count=1"], worktree / "go-agent-runtime")
            results.append({"mutation": name, "status": result.returncode, "caught": result.returncode != 0, "output": result.stdout + result.stderr})
            if result.returncode == 0:
                raise SystemExit(f"mutation survived: {name}")
        finally:
            run(["git", "worktree", "remove", "--force", str(worktree)], ROOT)
            shutil.rmtree(workspace, ignore_errors=True)
    return results


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", default="retirement-and-owned-paths")
    parser.parse_args()
    evidence = {"focused": focused(), "mutations": mutation_results()}
    (Path(__file__).with_name("mutation-results.json")).write_text(json.dumps(evidence, indent=2) + "\n")
    print(json.dumps({"focused": "pass", "mutations": "all-caught"}))


if __name__ == "__main__":
    main()
