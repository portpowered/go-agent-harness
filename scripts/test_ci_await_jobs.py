"""Tests for scripts/ci-await-jobs.sh against a scripted fake gh."""

from __future__ import annotations

import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import textwrap
import unittest

SCRIPT = Path(__file__).resolve().parent / "ci-await-jobs.sh"

# The fake gh prints the next listing from $FAKE_GH_DIR/listing-N.tsv (the
# last one repeats) and records every call, so a test scripts how the
# sibling jobs progress between polls. A listing named listing-N.fail makes
# that call fail.
FAKE_GH = textwrap.dedent(
    """\
    #!/usr/bin/env bash
    set -euo pipefail
    dir="$FAKE_GH_DIR"
    count=$(( $(cat "$dir/calls" 2>/dev/null || echo 0) + 1 ))
    echo "$count" >"$dir/calls"
    echo "$*" >>"$dir/args"
    if [ -e "$dir/listing-$count.fail" ]; then exit 1; fi
    file="$dir/listing-$count.tsv"
    while [ ! -e "$file" ] && [ "$count" -gt 1 ]; do
        count=$((count - 1))
        file="$dir/listing-$count.tsv"
    done
    cat "$file"
    """
)


def row(name: str, status: str, conclusion: str = "", attempt: int = 1) -> str:
    return f"{name}\t{status}\t{conclusion}\t{attempt}\n"


class AwaitJobsTest(unittest.TestCase):
    def setUp(self) -> None:
        self.dir = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.dir, True)
        gh = self.dir / "gh"
        gh.write_text(FAKE_GH)
        gh.chmod(0o755)

    def listings(self, *listings: str | None) -> None:
        for index, listing in enumerate(listings, start=1):
            if listing is None:
                (self.dir / f"listing-{index}.fail").write_text("")
            else:
                (self.dir / f"listing-{index}.tsv").write_text(listing)

    def run_script(self, *names: str, timeout: int = 30) -> subprocess.CompletedProcess[str]:
        env = dict(
            os.environ,
            GH=str(self.dir / "gh"),
            FAKE_GH_DIR=str(self.dir),
            GITHUB_REPOSITORY="owner/repo",
            GITHUB_RUN_ID="42",
        )
        return subprocess.run(
            ["bash", str(SCRIPT), "--interval", "0", "--timeout", str(timeout), *names],
            env=env,
            capture_output=True,
            text=True,
            timeout=60,
        )

    def calls(self) -> int:
        return int((self.dir / "calls").read_text())

    def test_waits_until_every_job_succeeds(self) -> None:
        self.listings(
            row("A", "queued"),
            row("A", "in_progress") + row("B", "queued"),
            row("A", "completed", "success") + row("B", "completed", "success") + row("C", "in_progress"),
        )
        result = self.run_script("A", "B")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("all 2 sibling jobs succeeded", result.stdout)
        self.assertEqual(self.calls(), 3)
        self.assertIn("repos/owner/repo/actions/runs/42/jobs?filter=latest", (self.dir / "args").read_text())

    def test_fails_as_soon_as_a_job_fails(self) -> None:
        self.listings(row("A", "completed", "failure") + row("B", "in_progress"))
        result = self.run_script("A", "B")
        self.assertEqual(result.returncode, 1)
        self.assertIn("sibling job A (failure) did not succeed", result.stdout)
        self.assertEqual(self.calls(), 1)

    def test_cancelled_and_skipped_jobs_fail(self) -> None:
        for conclusion in ("cancelled", "skipped", "timed_out"):
            with self.subTest(conclusion=conclusion):
                self.setUp()
                self.listings(row("A", "completed", conclusion))
                self.assertEqual(self.run_script("A").returncode, 1)

    def test_uses_the_newest_attempt_of_a_rerun_job(self) -> None:
        self.listings(row("A", "completed", "failure", 1) + row("A", "completed", "success", 2))
        self.assertEqual(self.run_script("A").returncode, 0)
        self.setUp()
        self.listings(row("A", "completed", "success", 1) + row("A", "in_progress", "", 2), row("A", "completed", "failure", 2))
        self.assertEqual(self.run_script("A").returncode, 1)

    def test_name_must_match_exactly(self) -> None:
        self.listings(row("CI (coverage agent-cli unit) extra", "completed", "success"))
        result = self.run_script("CI (coverage agent-cli unit)", timeout=0)
        self.assertEqual(result.returncode, 1)
        self.assertIn("timed out", result.stdout)
        self.assertIn("CI (coverage agent-cli unit) (not started)", result.stdout)

    def test_retries_a_failed_listing(self) -> None:
        self.listings(None, row("A", "completed", "success"))
        result = self.run_script("A")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("could not list", result.stdout)

    def test_requires_a_job_name(self) -> None:
        self.assertEqual(self.run_script().returncode, 2)


if __name__ == "__main__":
    unittest.main()
