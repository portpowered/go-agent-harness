#!/usr/bin/env python3
"""Wait for a pull request's current-head required checks to pass.

Usage: ``python3 factory/scripts/ci-wait.py <work-branch>``

The script is a bounded gate for the factory's process-to-review transition.
It looks up the pull request whose head branch is the supplied work branch and
then takes a reconciled ``pr view`` / ``pr checks`` / ``pr view`` observation.
The two views must identify the same open PR head and the check rows must agree
with the rollup.  A success result is emitted only after two equal observations
show that every observed required check is ``SUCCESS``.  A merged PR is a
separate, explicit success path.

Exit contract:

* exit 0 and JSON on stdout: the PR is merged, or current-head checks pass;
* exit 1 and a diagnostic on stderr: no PR, unavailable GitHub, a changed or
  uncertain head, missing checks, a failing check, or a bounded timeout.

The command never mutates a branch, PR, or check run.  It uses only the
standard library so the factory can run it without the Go workspace.
"""

import json
import subprocess
import sys
import time
from dataclasses import dataclass
from enum import Enum
from pathlib import Path


# Keep the polling budget below the factory worker execution limit.  Tests
# replace these constants and the clock/sleep functions, so no test waits on
# wall-clock time or contacts GitHub.
POLL_INTERVAL_SECONDS = 120
DEADLINE_SECONDS = 100 * 60
GH_CALL_TIMEOUT_SECONDS = 120
PR_LOOKUP_ATTEMPTS = 5
PR_LOOKUP_INTERVAL_SECONDS = 30
PR_LOOKUP_INFRASTRUCTURE_ATTEMPTS = 3
PR_LOOKUP_INFRASTRUCTURE_BACKOFF_SECONDS = 5
PR_LOOKUP_INFRASTRUCTURE_MAX_BACKOFF_SECONDS = 60
NO_CHECKS_GRACE_SECONDS = 10 * 60
CONVERGENCE_OBSERVATIONS = 2

PR_VIEW_JSON_FIELDS = "number,state,headRefOid,statusCheckRollup"
PR_CHECKS_JSON_FIELDS = "name,state,bucket,link,workflow,startedAt,completedAt"
REQUIRED_CHECKS_POLICY_PATH = (
    Path(__file__).resolve().parents[1] / "docs" / "required-checks.json"
)
NO_REQUIRED_CHECKS_MARKER = "no required checks reported"

NON_TERMINAL_BUCKETS = {"pending"}
NON_TERMINAL_STATES = {
    "PENDING",
    "QUEUED",
    "IN_PROGRESS",
    "WAITING",
    "REQUESTED",
    "EXPECTED",
}
TERMINAL_STATES = {
    "ACTION_REQUIRED",
    "CANCELLED",
    "ERROR",
    "FAILURE",
    "NEUTRAL",
    "SKIPPED",
    "STALE",
    "STARTUP_FAILURE",
    "SUCCESS",
    "TIMED_OUT",
}
KNOWN_CHECK_BUCKETS = {"cancel", "fail", "pass", "pending", "skipping"}
PR_STATE_PREFERENCE = ("OPEN", "MERGED", "CLOSED")


class PRLookupStatus(Enum):
    """Classification of a bounded ``gh pr list`` response."""

    FOUND = "found"
    NOT_FOUND = "not-found"
    INFRASTRUCTURE_FAILURE = "infrastructure-failure"


@dataclass(frozen=True)
class PRLookupResult:
    """Keep an empty successful lookup distinct from an unavailable lookup."""

    status: PRLookupStatus
    prs: tuple = ()


class JSONReadStatus(Enum):
    OK = "ok"
    EMPTY = "empty"
    MALFORMED = "malformed"
    UNAVAILABLE = "unavailable"


@dataclass(frozen=True)
class JSONRead:
    status: JSONReadStatus
    value: object = None


class PolicyError(ValueError):
    """The repository-authored required-check policy is unusable."""


class SnapshotStatus(Enum):
    VALID = "valid"
    EMPTY = "empty"
    UNCERTAIN = "uncertain"
    MERGED = "merged"


@dataclass(frozen=True)
class CurrentHeadSnapshot:
    """One reconciled observation of one PR head and its check set."""

    status: SnapshotStatus
    reason: str
    head_ref_oid: str = ""
    checks: tuple = ()
    observed_heads: tuple = ()

    def fingerprint(self):
        """Return only stable fields used for convergence."""
        return (
            self.head_ref_oid,
            tuple(
                (
                    check["identity"],
                    check["state"],
                    check["bucket"],
                    check.get("link"),
                    check.get("workflow"),
                )
                for check in self.checks
            ),
        )


def log(message):
    """Keep stdout reserved for the final success JSON document."""
    print(message, file=sys.stderr, flush=True)


def fail(message):
    """Report a bounded failure without exposing dependency command output."""
    print(f"ci-wait: {message}", file=sys.stderr, flush=True)
    raise SystemExit(1)


def load_required_check_policy(path=None):
    """Load the non-empty repository-authored check-name fallback policy.

    The checked-in manifest uses ``requiredChecks``.  Accepting a top-level
    list and the older ``checks``/``required`` spellings keeps this small
    loader useful to isolated harness fixtures without weakening validation:
    the result must still be a non-empty, duplicate-free list of names.
    """
    policy_path = Path(path or REQUIRED_CHECKS_POLICY_PATH)
    try:
        document = json.loads(policy_path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as error:
        raise PolicyError(f"cannot read required-check policy {policy_path}") from error

    if isinstance(document, list):
        names = document
    elif isinstance(document, dict):
        names = document.get("requiredChecks")
        if names is None:
            names = document.get("checks")
        if names is None:
            names = document.get("required")
    else:
        names = None

    if not isinstance(names, list) or not names:
        raise PolicyError("required-check policy must contain a non-empty name list")

    normalized = []
    seen = set()
    for name in names:
        if not isinstance(name, str) or not name.strip():
            raise PolicyError("required-check policy contains an invalid check name")
        name = name.strip()
        if name in seen:
            raise PolicyError(f"required-check policy repeats {name!r}")
        seen.add(name)
        normalized.append(name)
    return tuple(normalized)


def run_gh(*args):
    """Run one ``gh`` command with a per-call timeout and no exception on rc."""
    return subprocess.run(
        ["gh", *args],
        capture_output=True,
        text=True,
        timeout=GH_CALL_TIMEOUT_SECONDS,
    )


def _valid_pr_state(value):
    return isinstance(value, str) and value in PR_STATE_PREFERENCE


def list_prs_for_head(branch):
    """Return the PRs attached to ``branch`` with an explicit failure class."""
    try:
        result = run_gh(
            "pr",
            "list",
            "--head",
            branch,
            "--state",
            "all",
            "--json",
            "number,state",
            "--limit",
            "20",
        )
    except subprocess.TimeoutExpired:
        log("gh pr list timed out; treating lookup as unavailable")
        return PRLookupResult(PRLookupStatus.INFRASTRUCTURE_FAILURE)
    except OSError:
        log("gh pr list could not be executed; treating lookup as unavailable")
        return PRLookupResult(PRLookupStatus.INFRASTRUCTURE_FAILURE)

    stdout = (result.stdout or "").strip()
    if result.returncode != 0 or not stdout:
        log("gh pr list did not return a usable response")
        return PRLookupResult(PRLookupStatus.INFRASTRUCTURE_FAILURE)

    try:
        prs = json.loads(stdout)
    except (TypeError, json.JSONDecodeError):
        log("gh pr list returned unparseable JSON")
        return PRLookupResult(PRLookupStatus.INFRASTRUCTURE_FAILURE)

    if not isinstance(prs, list) or any(
        not isinstance(pr, dict)
        or isinstance(pr.get("number"), bool)
        or not isinstance(pr.get("number"), int)
        or not _valid_pr_state(pr.get("state"))
        for pr in prs
    ):
        log("gh pr list returned unusable JSON")
        return PRLookupResult(PRLookupStatus.INFRASTRUCTURE_FAILURE)

    return PRLookupResult(
        PRLookupStatus.FOUND if prs else PRLookupStatus.NOT_FOUND,
        tuple(prs),
    )


def infrastructure_backoff_seconds(attempt):
    """Return a bounded exponential retry delay."""
    return min(
        PR_LOOKUP_INFRASTRUCTURE_BACKOFF_SECONDS * (2 ** (attempt - 1)),
        PR_LOOKUP_INFRASTRUCTURE_MAX_BACKOFF_SECONDS,
    )


def release_for_infrastructure_requeue(branch, attempts):
    """Report exhausted lookup infrastructure retries as a hard failure.

    The reference factory used this name for a review requeue.  The harness
    gate has no successful requeue result: a worker may enter review only with
    passing evidence, so the same bounded condition is now an exit-1 failure.
    Keeping the helper preserves a small, testable seam for callers that
    classify lookup outcomes.
    """
    log(
        "GitHub PR lookup infrastructure retry budget exhausted "
        f"for head branch {branch!r} after {attempts} failures"
    )


def resolve_pr(branch):
    """Resolve the branch PR, failing closed after separate retry budgets.

    A successful empty lookup consumes the missing-PR budget.  A transport,
    timeout, or malformed response consumes only the infrastructure budget;
    one later successful lookup resets that budget.  ``None`` is returned only
    after infrastructure retries are exhausted so callers can keep that case
    distinct from the explicit missing-PR ``SystemExit(1)`` path.
    """
    successful_empty_lookups = 0
    infrastructure_failures = 0

    while successful_empty_lookups < PR_LOOKUP_ATTEMPTS:
        lookup = list_prs_for_head(branch)
        if lookup.status == PRLookupStatus.INFRASTRUCTURE_FAILURE:
            infrastructure_failures += 1
            if infrastructure_failures >= PR_LOOKUP_INFRASTRUCTURE_ATTEMPTS:
                release_for_infrastructure_requeue(branch, infrastructure_failures)
                return None
            backoff = infrastructure_backoff_seconds(infrastructure_failures)
            log(
                f"GitHub PR lookup unavailable for head branch {branch!r} "
                f"(attempt {infrastructure_failures}/"
                f"{PR_LOOKUP_INFRASTRUCTURE_ATTEMPTS}); retrying in {backoff}s"
            )
            time.sleep(backoff)
            continue

        # A valid response means transport recovered.  It must not inherit
        # failures from an earlier outage.
        infrastructure_failures = 0

        if lookup.status == PRLookupStatus.NOT_FOUND:
            successful_empty_lookups += 1
            log(
                f"successful PR lookup found no matching PR for head branch "
                f"{branch!r} ({successful_empty_lookups}/"
                f"{PR_LOOKUP_ATTEMPTS})"
            )
            if successful_empty_lookups < PR_LOOKUP_ATTEMPTS:
                time.sleep(PR_LOOKUP_INTERVAL_SECONDS)
            continue

        for state in PR_STATE_PREFERENCE:
            matches = [pr for pr in lookup.prs if pr.get("state") == state]
            if matches:
                return matches[0]

        # This is unreachable for a schema-valid response, but keep it bounded
        # if GitHub adds a new state before the script is updated.
        infrastructure_failures += 1
        log("gh pr list returned no selectable PR state")
        if infrastructure_failures >= PR_LOOKUP_INFRASTRUCTURE_ATTEMPTS:
            return None
        time.sleep(infrastructure_backoff_seconds(infrastructure_failures))

    fail(
        f"successful PR lookups found no PR for head branch {branch!r} after "
        f"{successful_empty_lookups} attempts"
    )


def read_gh_json(label, *args):
    """Parse one bounded JSON response without forwarding command diagnostics."""
    try:
        result = run_gh(*args)
    except subprocess.TimeoutExpired:
        log(f"{label} timed out; treating the observation as unavailable")
        return JSONRead(JSONReadStatus.UNAVAILABLE)
    except OSError:
        log(f"{label} could not be executed; treating the observation as unavailable")
        return JSONRead(JSONReadStatus.UNAVAILABLE)

    stdout = (result.stdout or "").strip()
    if not stdout:
        status = JSONReadStatus.UNAVAILABLE if result.returncode else JSONReadStatus.EMPTY
        log(f"{label} returned no JSON; treating the observation as {status.value}")
        return JSONRead(status)

    try:
        value = json.loads(stdout)
    except (TypeError, json.JSONDecodeError) as error:
        position = getattr(error, "pos", "?")
        log(f"{label} returned unparseable JSON at position {position}")
        return JSONRead(JSONReadStatus.MALFORMED)

    # ``gh pr checks`` uses nonzero statuses for pending and failing checks.
    # Valid stdout remains evidence; the check-state policy below decides it.
    return JSONRead(JSONReadStatus.OK, value)


def fetch_pr_view(pr_number):
    return read_gh_json(
        "gh pr view",
        "pr",
        "view",
        str(pr_number),
        "--json",
        PR_VIEW_JSON_FIELDS,
    )


def fetch_checks(pr_number):
    """Read every check row currently reported for the pull request."""
    return read_gh_json(
        "gh pr checks",
        "pr",
        "checks",
        str(pr_number),
        "--json",
        PR_CHECKS_JSON_FIELDS,
    )


def fetch_required_checks(pr_number):
    """Read GitHub's required subset, distinguishing absence from outages.

    GitHub returns exit 1 with the diagnostic ``no required checks reported``
    when branch protection has no required checks configured.  That is a
    legitimate policy absence and selects the repository-authored fallback.
    Other empty failures remain unavailable and cannot be treated as green.
    """
    try:
        result = run_gh(
            "pr",
            "checks",
            str(pr_number),
            "--required",
            "--json",
            PR_CHECKS_JSON_FIELDS,
        )
    except subprocess.TimeoutExpired:
        log(
            "gh pr checks --required timed out; treating required policy as unavailable"
        )
        return JSONRead(JSONReadStatus.UNAVAILABLE)
    except OSError:
        log(
            "gh pr checks --required could not be executed; "
            "treating required policy as unavailable"
        )
        return JSONRead(JSONReadStatus.UNAVAILABLE)

    stdout = (result.stdout or "").strip()
    if not stdout:
        diagnostic = (result.stderr or "").lower()
        if result.returncode == 0 or NO_REQUIRED_CHECKS_MARKER in diagnostic:
            log("gh reports no branch-protection required checks; using explicit policy")
            return JSONRead(JSONReadStatus.EMPTY)
        log("gh pr checks --required returned no JSON; treating required policy as unavailable")
        return JSONRead(JSONReadStatus.UNAVAILABLE)

    try:
        value = json.loads(stdout)
    except (TypeError, json.JSONDecodeError) as error:
        position = getattr(error, "pos", "?")
        log(f"gh pr checks --required returned unparseable JSON at position {position}")
        return JSONRead(JSONReadStatus.MALFORMED)
    return JSONRead(JSONReadStatus.OK, value)


def _non_empty_text(value):
    if not isinstance(value, str):
        return None
    value = value.strip()
    return value or None


def _check_kind(check, source):
    explicit_kind = check.get("__typename") or check.get("type") or check.get("kind")
    if explicit_kind is not None:
        if explicit_kind in {"CheckRun", "StatusContext"}:
            return explicit_kind
        return None
    if _non_empty_text(check.get("context")):
        return "StatusContext"
    if source == "checks" or _non_empty_text(check.get("name")):
        return "CheckRun"
    return None


def _check_state(check):
    raw_state = check.get("state")
    if raw_state is None:
        raw_state = check.get("status")
    state = _non_empty_text(raw_state)
    conclusion = _non_empty_text(check.get("conclusion"))
    if state:
        state = state.upper().replace("-", "_")
    if conclusion:
        conclusion = conclusion.upper().replace("-", "_")

    if state in {"COMPLETED", "COMPLETE", "DONE"}:
        state = conclusion
    elif state is None:
        state = conclusion
    if state == "CANCELED":
        state = "CANCELLED"
    return state


def _canonical_bucket(state):
    if state in NON_TERMINAL_STATES:
        return "pending"
    if state == "SUCCESS":
        return "pass"
    if state in {"NEUTRAL", "SKIPPED"}:
        return "skipping"
    return "fail"


def _normalize_check(check, source):
    """Return an identity-complete, terminality-normalized check row."""
    if not isinstance(check, dict):
        return None, "malformed-check-row"

    kind = _check_kind(check, source)
    if kind is None:
        return None, "unknown-check-type"
    name = _non_empty_text(check.get("name")) or _non_empty_text(check.get("context"))
    if name is None:
        return None, "unknown-check-name"

    link = None
    for field in ("link", "detailsUrl", "targetUrl", "url", "htmlUrl"):
        link = _non_empty_text(check.get(field))
        if link:
            break
    workflow = _non_empty_text(check.get("workflow")) or _non_empty_text(
        check.get("workflowName")
    )
    stable_ref = link
    if stable_ref is None and workflow is not None:
        stable_ref = f"workflow:{workflow}"
    if stable_ref is None and kind == "StatusContext":
        stable_ref = f"context:{name}"
    if stable_ref is None:
        return None, "unknown-check-identity"

    state = _check_state(check)
    if state not in NON_TERMINAL_STATES and state not in TERMINAL_STATES:
        return None, "unknown-check-state"
    bucket = _canonical_bucket(state)
    supplied_bucket = check.get("bucket")
    if supplied_bucket is not None:
        supplied_bucket = _non_empty_text(supplied_bucket)
        if supplied_bucket is None or supplied_bucket.lower() not in KNOWN_CHECK_BUCKETS:
            return None, "unknown-check-bucket"
        supplied_bucket = supplied_bucket.lower()
        if supplied_bucket != bucket and not (
            state == "CANCELLED" and supplied_bucket == "cancel"
        ):
            return None, "check-state-bucket-mismatch"

    identity = f"{kind}|{name}|{stable_ref}"
    return (
        {
            "identity": identity,
            "name": name,
            "workflow": workflow,
            "link": link,
            "state": state,
            "bucket": bucket,
        },
        None,
    )


def _normalize_check_list(checks, source):
    if not isinstance(checks, list):
        return None, "malformed-check-list"
    normalized = {}
    for check in checks:
        normalized_check, reason = _normalize_check(check, source)
        if normalized_check is None:
            return None, reason
        identity = normalized_check["identity"]
        if identity in normalized:
            return None, "duplicate-check-identity"
        normalized[identity] = normalized_check
    return normalized, None


def _merge_check_maps(left, right, require_same_keys=False):
    """Reconcile check sources without hiding a state or set change."""
    if require_same_keys and set(left) != set(right):
        return None, "check-set-changed-during-observation"

    merged = {}
    for identity in sorted(set(left) | set(right)):
        left_check = left.get(identity)
        right_check = right.get(identity)
        if left_check is not None and right_check is not None:
            if (
                left_check["state"] != right_check["state"]
                or left_check["bucket"] != right_check["bucket"]
            ):
                return None, "check-state-mismatch"
            check = dict(left_check)
            if check["link"] is None:
                check["link"] = right_check["link"]
            if check["workflow"] is None:
                check["workflow"] = right_check["workflow"]
            merged[identity] = check
        else:
            merged[identity] = dict(left_check or right_check)
    return merged, None


def _merge_required_checks(rollup, required):
    """Reconcile a required subset against the current-head check map.

    Every required identity must be present in the same-head map and agree on
    state and bucket.  The map may contain optional rows as well.
    """
    if not set(required).issubset(rollup):
        return None, "required-check-not-in-current-head-rollup"
    merged = {}
    for identity in sorted(required):
        pair, reason = _merge_check_maps(
            {identity: rollup[identity]},
            {identity: required[identity]},
            require_same_keys=True,
        )
        if reason is not None:
            return None, reason
        merged[identity] = pair[identity]
    return merged, None


def _select_policy_checks(checks, policy_names):
    """Select every current check row matching each authored policy name."""
    selected = {}
    missing = []
    for name in policy_names:
        matches = [
            (identity, check)
            for identity, check in checks.items()
            if check.get("name") == name
        ]
        if not matches:
            missing.append(name)
            continue
        for identity, check in matches:
            selected[identity] = check
    if missing:
        return None, f"policy-missing-check:{missing[0]}"
    return selected, None


def _enforced_checks(rollup, all_checks, github_required, policy_names):
    """Build the union of GitHub-required and authored-policy check rows."""
    policy_checks, reason = _select_policy_checks(all_checks, policy_names)
    if reason is not None:
        return None, reason

    enforced, reason = _merge_required_checks(rollup, policy_checks)
    if reason is not None:
        return None, reason
    if github_required:
        github_checks, reason = _merge_required_checks(rollup, github_required)
        if reason is not None:
            return None, reason
        # A row can be selected by both policies.  Use the same normalized
        # current-head value, and retain each distinct required identity.
        for identity, check in github_checks.items():
            existing = enforced.get(identity)
            if existing is not None and (
                existing["state"] != check["state"]
                or existing["bucket"] != check["bucket"]
            ):
                return None, "required-check-state-mismatch"
            enforced[identity] = check
    return enforced, None


def _view_parts(read, pr_number):
    """Validate and unpack an open PR view response."""
    if read.status != JSONReadStatus.OK:
        return None, f"view-{read.status.value}"
    if not isinstance(read.value, dict):
        return None, "malformed-pr-view"
    if read.value.get("number") != pr_number:
        return None, "pr-number-mismatch"
    if _non_empty_text(read.value.get("state")) != "OPEN":
        return None, "pr-state-changed"
    head_ref_oid = _non_empty_text(read.value.get("headRefOid"))
    if head_ref_oid is None:
        return None, "unknown-head"
    rollup = read.value.get("statusCheckRollup")
    if not isinstance(rollup, list):
        return None, "malformed-status-check-rollup"
    return (head_ref_oid, rollup), None


def _head_hint(read):
    if read.status == JSONReadStatus.OK and isinstance(read.value, dict):
        return _non_empty_text(read.value.get("headRefOid")) or ""
    return ""


def _observed_heads(*heads):
    return tuple(dict.fromkeys(head for head in heads if head))


def _merged_view(read, pr_number):
    """Return true only for a schema-valid merged view of this PR."""
    return (
        read.status == JSONReadStatus.OK
        and isinstance(read.value, dict)
        and read.value.get("number") == pr_number
        and _non_empty_text(read.value.get("state")) == "MERGED"
    )


def observe_current_head(pr_number, policy_names=None):
    """Take one reconciled all-checks observation of the PR's current head."""
    if policy_names is None:
        policy_names = load_required_check_policy()
    before_read = fetch_pr_view(pr_number)
    checks_read = fetch_checks(pr_number)
    required_read = fetch_required_checks(pr_number)
    after_read = fetch_pr_view(pr_number)

    # Once GitHub reports the exact PR as merged, it is an explicit success
    # condition even if the checks API no longer serves its old rows.
    if _merged_view(after_read, pr_number):
        return CurrentHeadSnapshot(
            SnapshotStatus.MERGED,
            "pr-merged",
            _head_hint(after_read),
            observed_heads=_observed_heads(_head_hint(before_read), _head_hint(after_read)),
        )

    before, before_reason = _view_parts(before_read, pr_number)
    after, after_reason = _view_parts(after_read, pr_number)
    before_head = before[0] if before else _head_hint(before_read)
    after_head = after[0] if after else _head_hint(after_read)
    heads = _observed_heads(before_head, after_head)
    head_ref_oid = after_head or before_head

    if before_reason is not None:
        return CurrentHeadSnapshot(
            SnapshotStatus.UNCERTAIN,
            f"before-{before_reason}",
            head_ref_oid,
            observed_heads=heads,
        )
    if after_reason is not None:
        return CurrentHeadSnapshot(
            SnapshotStatus.UNCERTAIN,
            f"after-{after_reason}",
            head_ref_oid,
            observed_heads=heads,
        )
    if before_head != after_head:
        log(f"PR #{pr_number} head changed during checks observation")
        return CurrentHeadSnapshot(
            SnapshotStatus.UNCERTAIN,
            "head-changed-during-observation",
            after_head,
            observed_heads=heads,
        )

    if required_read.status == JSONReadStatus.UNAVAILABLE:
        return CurrentHeadSnapshot(
            SnapshotStatus.UNCERTAIN,
            "required-checks-unavailable",
            head_ref_oid,
            observed_heads=heads,
        )
    if required_read.status == JSONReadStatus.MALFORMED:
        return CurrentHeadSnapshot(
            SnapshotStatus.UNCERTAIN,
            "required-checks-malformed",
            head_ref_oid,
            observed_heads=heads,
        )
    github_required = {}
    if required_read.status == JSONReadStatus.OK:
        if not isinstance(required_read.value, list):
            return CurrentHeadSnapshot(
                SnapshotStatus.UNCERTAIN,
                "required-checks-malformed-list",
                head_ref_oid,
                observed_heads=heads,
            )
        github_required, reason = _normalize_check_list(
            required_read.value, "checks"
        )
        if reason is not None:
            return CurrentHeadSnapshot(
                SnapshotStatus.UNCERTAIN,
                f"required-{reason}",
                head_ref_oid,
                observed_heads=heads,
            )

    checks = checks_read.value if checks_read.status == JSONReadStatus.OK else None
    if checks_read.status != JSONReadStatus.OK:
        if not before[1] and not after[1] and checks_read.status == JSONReadStatus.EMPTY:
            return CurrentHeadSnapshot(
                SnapshotStatus.EMPTY,
                "empty-current-head-check-set",
                head_ref_oid,
                observed_heads=heads,
            )
        return CurrentHeadSnapshot(
            SnapshotStatus.UNCERTAIN,
            f"checks-{checks_read.status.value}",
            head_ref_oid,
            observed_heads=heads,
        )
    if not isinstance(checks, list):
        return CurrentHeadSnapshot(
            SnapshotStatus.UNCERTAIN,
            "checks-malformed-check-list",
            head_ref_oid,
            observed_heads=heads,
        )
    if not before[1] or not after[1] or not checks:
        if not before[1] and not after[1] and not checks:
            return CurrentHeadSnapshot(
                SnapshotStatus.EMPTY,
                "empty-current-head-check-set",
                head_ref_oid,
                observed_heads=heads,
            )
        return CurrentHeadSnapshot(
            SnapshotStatus.UNCERTAIN,
            "incomplete-current-head-check-set",
            head_ref_oid,
            observed_heads=heads,
        )

    before_map, reason = _normalize_check_list(before[1], "rollup")
    if reason is not None:
        return CurrentHeadSnapshot(
            SnapshotStatus.UNCERTAIN,
            f"before-{reason}",
            head_ref_oid,
            observed_heads=heads,
        )
    after_map, reason = _normalize_check_list(after[1], "rollup")
    if reason is not None:
        return CurrentHeadSnapshot(
            SnapshotStatus.UNCERTAIN,
            f"after-{reason}",
            head_ref_oid,
            checks=tuple(sorted(before_map.values(), key=lambda check: check["identity"])),
            observed_heads=heads,
        )
    checks_map, reason = _normalize_check_list(checks, "checks")
    if reason is not None:
        return CurrentHeadSnapshot(
            SnapshotStatus.UNCERTAIN,
            f"checks-{reason}",
            head_ref_oid,
            checks=tuple(sorted(before_map.values(), key=lambda check: check["identity"])),
            observed_heads=heads,
        )

    rollup_map, reason = _merge_check_maps(
        before_map,
        after_map,
        require_same_keys=True,
    )
    if reason is not None:
        diagnostic_checks = before_map
        if reason == "check-set-changed-during-observation":
            diagnostic_checks, _ = _merge_check_maps(before_map, after_map)
        return CurrentHeadSnapshot(
            SnapshotStatus.UNCERTAIN,
            reason,
            head_ref_oid,
            checks=tuple(
                sorted(diagnostic_checks.values(), key=lambda check: check["identity"])
            ),
            observed_heads=heads,
        )

    all_checks, reason = _merge_check_maps(
        rollup_map,
        checks_map,
        require_same_keys=True,
    )
    if reason is not None:
        return CurrentHeadSnapshot(
            SnapshotStatus.UNCERTAIN,
            reason,
            head_ref_oid,
            checks=tuple(sorted(rollup_map.values(), key=lambda check: check["identity"])),
            observed_heads=heads,
        )

    reconciled, reason = _enforced_checks(
        all_checks,
        all_checks,
        github_required,
        policy_names,
    )
    if reason is not None:
        return CurrentHeadSnapshot(
            SnapshotStatus.UNCERTAIN,
            reason,
            head_ref_oid,
            checks=tuple(sorted(all_checks.values(), key=lambda check: check["identity"])),
            observed_heads=heads,
        )

    return CurrentHeadSnapshot(
        SnapshotStatus.VALID,
        "stable-current-head-observation",
        head_ref_oid,
        checks=tuple(reconciled[identity] for identity in sorted(reconciled)),
        observed_heads=heads,
    )


def non_terminal_checks(checks):
    pending = []
    for check in checks:
        if not isinstance(check, dict):
            pending.append(check)
            continue
        bucket = str(check.get("bucket", "")).lower()
        state = str(check.get("state", "")).upper()
        if bucket in NON_TERMINAL_BUCKETS or state in NON_TERMINAL_STATES:
            pending.append(check)
    return pending


def failed_checks(checks):
    """Return terminal non-success rows, including skipped/neutral checks."""
    failures = []
    for check in checks:
        if not isinstance(check, dict):
            failures.append(check)
            continue
        state = str(check.get("state", "")).upper()
        bucket = str(check.get("bucket", "")).lower()
        if state in NON_TERMINAL_STATES or bucket in NON_TERMINAL_BUCKETS:
            continue
        if state != "SUCCESS" or bucket != "pass":
            failures.append(check)
    return failures


def emit_result(**fields):
    print(json.dumps({"status": "ready", **fields}, indent=2, sort_keys=True))


def snapshot_fields(snapshot):
    fields = {
        "checks": len(snapshot.checks),
        "headRefOid": snapshot.head_ref_oid or None,
        "checkIdentities": list(snapshot.checks),
    }
    pending = non_terminal_checks(snapshot.checks)
    if pending:
        fields["pendingChecks"] = pending
    return fields


def snapshot_uncertainty(snapshot, reason=None):
    uncertainty = {"reason": reason or snapshot.reason}
    if snapshot.head_ref_oid:
        uncertainty["headRefOid"] = snapshot.head_ref_oid
    if snapshot.observed_heads:
        uncertainty["observedHeads"] = list(snapshot.observed_heads)
    return uncertainty


def _format_checks(checks):
    names = []
    for check in checks[:5]:
        if isinstance(check, dict):
            names.append(f"{check.get('name', '?')}={check.get('state', '?')}")
        else:
            names.append("malformed")
    return ", ".join(names)


def main():
    if len(sys.argv) != 2 or not sys.argv[1].strip():
        print(f"Usage: {sys.argv[0]} <work-branch>", file=sys.stderr)
        raise SystemExit(1)

    branch = sys.argv[1]
    pr = resolve_pr(branch)
    if pr is None:
        fail("GitHub PR lookup failed after bounded retries")

    pr_number = pr.get("number")
    pr_state = pr.get("state")
    if pr_state == "MERGED":
        log(f"PR #{pr_number} for {branch!r} is already MERGED")
        emit_result(pr=pr_number, prState=pr_state, reason="pr-merged")
        return
    if pr_state == "CLOSED":
        fail(f"only CLOSED PRs exist for head branch {branch!r}; no merged PR was found")

    try:
        policy_names = load_required_check_policy()
    except PolicyError as error:
        fail(str(error))

    start = time.monotonic()
    deadline = start + max(0, DEADLINE_SECONDS)
    no_checks_deadline = start + max(0, NO_CHECKS_GRACE_SECONDS)
    candidate_fingerprint = None
    convergence_count = 0

    while True:
        snapshot = observe_current_head(pr_number, policy_names)
        now = time.monotonic()

        if snapshot.status == SnapshotStatus.MERGED:
            log(f"PR #{pr_number} became MERGED while waiting")
            emit_result(pr=pr_number, prState="MERGED", reason="pr-merged")
            return

        if snapshot.status == SnapshotStatus.VALID:
            pending = non_terminal_checks(snapshot.checks)
            failures = failed_checks(snapshot.checks)
            if failures:
                fail(
                    f"required checks failed on current head "
                    f"{snapshot.head_ref_oid or 'unknown'}: {_format_checks(failures)}"
                )
            if pending:
                candidate_fingerprint = None
                convergence_count = 0
                log(
                    f"PR #{pr_number} current head {snapshot.head_ref_oid}: "
                    f"{len(pending)} check(s) still pending ({_format_checks(pending)})"
                )
            else:
                fingerprint = snapshot.fingerprint()
                if fingerprint == candidate_fingerprint:
                    convergence_count += 1
                else:
                    candidate_fingerprint = fingerprint
                    convergence_count = 1
                if convergence_count >= CONVERGENCE_OBSERVATIONS:
                    log(
                        f"all {len(snapshot.checks)} checks on current head "
                        f"{snapshot.head_ref_oid} are terminal and passing"
                    )
                    emit_result(
                        pr=pr_number,
                        prState=pr_state,
                        reason="checks-terminal",
                        **snapshot_fields(snapshot),
                        uncertainty=None,
                    )
                    return
                log(
                    f"PR #{pr_number} current head {snapshot.head_ref_oid}: "
                    f"passing candidate awaiting same-head convergence "
                    f"({convergence_count}/{CONVERGENCE_OBSERVATIONS})"
                )
        elif snapshot.status == SnapshotStatus.EMPTY:
            candidate_fingerprint = None
            convergence_count = 0
            log(
                f"PR #{pr_number} current head {snapshot.head_ref_oid or 'unknown'}: "
                "no checks reported yet"
            )
            # No checks are never terminal.  The grace period only controls
            # how long we distinguish delayed registration from a hard fail.
            if now >= no_checks_deadline:
                fail(
                    f"no required checks reported for current head "
                    f"{snapshot.head_ref_oid or 'unknown'} after bounded grace"
                )
        else:
            candidate_fingerprint = None
            convergence_count = 0
            log(
                f"PR #{pr_number} current-head observation uncertain "
                f"({snapshot.reason}); retrying"
            )

        # Do not start a full poll sleep when the next observation would land
        # at or beyond the deadline.  This keeps the wall-clock wait bounded
        # by the configured budget instead of adding one interval to it.
        if now + max(0, POLL_INTERVAL_SECONDS) >= deadline:
            fail(
                f"bounded CI wait timed out for PR #{pr_number} "
                f"({snapshot.reason})"
            )
        time.sleep(POLL_INTERVAL_SECONDS)


if __name__ == "__main__":
    main()
