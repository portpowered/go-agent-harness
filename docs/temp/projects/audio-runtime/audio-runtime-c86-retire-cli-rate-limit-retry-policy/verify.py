#!/usr/bin/env python3
"""Fail-closed C86 source and mutation verifiers."""

from __future__ import annotations

import argparse
import json
import sys

from run import Budget, EvidenceFailure, inventory, mutation_controls, retirement_and_scope, write_report


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("inventory", "mutations", "retirement-and-adapter", "owned-and-excluded-paths"), required=True)
    args = parser.parse_args()
    try:
        if args.mode == "inventory":
            report = inventory()
        elif args.mode == "mutations":
            report = mutation_controls(Budget())
        else:
            report = retirement_and_scope()
        print(json.dumps(write_report(f"verify-{args.mode}", report), indent=2, sort_keys=True))
        return 0
    except (EvidenceFailure, OSError, ValueError) as error:
        print(json.dumps({"status": "failed", "mode": args.mode, "error": str(error)}, indent=2), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
