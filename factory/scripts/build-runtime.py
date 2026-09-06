#!/usr/bin/env python3
"""Build the reviewed factory runtime privately, without replacing global you."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import uuid

from project_contract import digest, root_path
import project_admission

REVISION = "a82f2e5a532a25b3e163014b28e72190ac28c354"


def build(source, root):
    revision = subprocess.check_output(["git", "-C", str(source), "rev-parse", "HEAD"], text=True).strip()
    dirty = subprocess.check_output(["git", "-C", str(source), "status", "--porcelain", "--untracked-files=no"], text=True)
    if revision != REVISION or dirty:
        raise ValueError("runtime source must be the clean pinned revision " + REVISION)
    directory = project_admission.common_dir(root) / "factory-bin"
    directory.mkdir(mode=0o700, exist_ok=True)
    temporary = directory / ("you-" + uuid.uuid4().hex)
    try:
        subprocess.run(["go", "build", "-o", str(temporary), "./cmd/factory"], cwd=source,
                       env=dict(os.environ, GOWORK="off"), check=True)
        temporary.chmod(0o700)
        proof = {"sourceRevision":revision, "sha256":digest(temporary)}
        os.replace(temporary, directory / "you")
        # Launcher validates both files together and fails closed during replacement.
        (directory / "runtime.json").write_text(json.dumps(proof, indent=2) + "\n")
        return proof
    finally:
        temporary.unlink(missing_ok=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--source", required=True, type=Path)
    args = parser.parse_args()
    try:
        print(json.dumps(build(args.source.resolve(), root_path())))
    except (ValueError, OSError, subprocess.SubprocessError) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
