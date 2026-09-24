#!/usr/bin/env python3
"""Small process-group controls for the C23 bounded child runner."""

from __future__ import annotations

import argparse
import subprocess
import sys
import time


def spawn_sleeping_child() -> subprocess.Popen[bytes]:
    return subprocess.Popen([sys.executable, "-c", "import time; time.sleep(30)"])


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=("normal", "overflow", "deadline"))
    args = parser.parse_args()
    child = spawn_sleeping_child()
    print(f"CHILD_SPAWNED pid={child.pid}", file=sys.stderr, flush=True)
    if args.mode == "normal":
        print("bounded normal output", flush=True)
        child.terminate()
        child.wait(timeout=2)
        return 0
    if args.mode == "overflow":
        sys.stdout.buffer.write(b"x" * (128 * 1024))
        sys.stdout.flush()
        time.sleep(30)
        return 0
    time.sleep(30)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
