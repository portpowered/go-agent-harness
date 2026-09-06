#!/usr/bin/env python3
"""Reconcile waiting Project Leads without creating overlapping cycles.

The script is intended for a SCRIPT_RUN workstation.  It reads the selected
Factory Session through the public ``you`` CLI, then makes only deliberate,
idempotent moves of stranded existing ``project`` Work from ``waiting`` to
``init``.  Blocked Projects are inspect-only. It never submits a new Project
or cycle.

Usage:
    python3 factory/scripts/reconcile-projects.py --session SESSION_ID

The worker workstation should pass ``--session {{ .Context.SessionID }}`` and
may override ``--server`` when the Factory host is not on the local default
port.  ``--dry-run`` is useful for local probes and tests.
"""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess
import sys
from typing import Any, Callable, Iterable, Mapping, Optional


# Keep the script directly executable while also allowing tests and other
# scripts to load it by path. The admission and contract helpers deliberately
# live beside this entry point.
_SCRIPT_DIR = Path(__file__).resolve().parent
if str(_SCRIPT_DIR) not in sys.path:
    sys.path.insert(0, str(_SCRIPT_DIR))

import project_admission
from project_contract import ContractError, manifest, root_path


DEFAULT_SERVER = "http://127.0.0.1:7439"
CLI_TIMEOUT_SECONDS = 30
MAX_WORK_RESULTS_PER_PAGE = 500
MAX_WORK_LIST_PAGES = 20
CHILD_WORK_TYPES = frozenset({"idea", "plan", "task", "review", "validation"})
ACTIVE_WORKER_SESSION_STATES = frozenset({"RESERVED", "STARTING", "RUNNING", "PAUSED"})
NONTERMINAL_STATE_TYPES = frozenset({"INITIAL", "PROCESSING"})
REQUEST_ID_LIMIT = 180


class ReconcileError(RuntimeError):
    """A public CLI read or move failed before reconciliation could finish."""


CommandRunner = Callable[[list[str]], subprocess.CompletedProcess[str]]


def _work_id(work: Mapping[str, Any]) -> str:
    return _string(work.get("workId") or work.get("id"))


def _work_name(work: Mapping[str, Any]) -> str:
    return _string(work.get("name"))


def _work_type(work: Mapping[str, Any]) -> str:
    return _string(work.get("workTypeName") or work.get("workType"))


def _state(work: Mapping[str, Any]) -> tuple[str, str]:
    state = work.get("state")
    if not isinstance(state, Mapping):
        return "", ""
    return _string(state.get("name")), _string(state.get("type")).upper()


def _string(value: Any) -> str:
    return value.strip() if isinstance(value, str) else ""


def _payload_object(work: Mapping[str, Any]) -> Mapping[str, Any]:
    payload = work.get("payload")
    if isinstance(payload, Mapping):
        return payload
    if isinstance(payload, str):
        try:
            decoded = json.loads(payload)
        except json.JSONDecodeError:
            return {}
        return decoded if isinstance(decoded, Mapping) else {}
    return {}


def _run_command(command: list[str]) -> subprocess.CompletedProcess[str]:
    try:
        return subprocess.run(
            command,
            capture_output=True,
            text=True,
            encoding="utf-8",
            errors="replace",
            timeout=CLI_TIMEOUT_SECONDS,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as error:
        raise ReconcileError(f"could not execute {' '.join(command[:2])}: {error}") from error


def _cli_json(
    runner: CommandRunner,
    server: str,
    *arguments: str,
) -> Any:
    command = ["you", "--server", server, "--json", *arguments]
    result = runner(command)
    if result.returncode != 0:
        details = (result.stderr or result.stdout or "").strip()
        if len(details) > 400:
            details = details[:400] + "..."
        raise ReconcileError(
            f"{' '.join(command)} failed with exit {result.returncode}"
            + (f": {details}" if details else "")
        )
    try:
        return json.loads(result.stdout or "")
    except json.JSONDecodeError as error:
        raise ReconcileError(
            f"{' '.join(command)} returned invalid JSON"
        ) from error


def _session_is_running(session: Any) -> bool:
    if not isinstance(session, Mapping):
        raise ReconcileError("session show returned a non-object JSON value")
    runtime = session.get("runtime")
    if not isinstance(runtime, Mapping):
        raise ReconcileError("session show omitted runtime")
    progress = runtime.get("progress")
    if not isinstance(progress, Mapping):
        raise ReconcileError("session show omitted runtime.progress")
    # Factory state is the authoritative scheduler lifecycle.  Runtime status
    # is IDLE while a healthy server has no dispatch in flight, so it must not
    # be used as the sole liveness test.
    return _string(progress.get("factoryState")).upper() == "RUNNING"


def _work_results(response: Any) -> list[Mapping[str, Any]]:
    if not isinstance(response, Mapping):
        raise ReconcileError("work list returned a non-object JSON value")
    if "results" not in response:
        # A successful command with no JSON results is an incomplete
        # observation. Treating it as an empty board could authorize a move.
        raise ReconcileError("work list omitted results")
    results = response.get("results")
    if not isinstance(results, list) or any(not isinstance(item, Mapping) for item in results):
        raise ReconcileError("work list returned an invalid results array")
    return list(results)


def _work_total(response: Mapping[str, Any]) -> Optional[int]:
    counts = response.get("counts")
    if counts is None:
        return None
    if not isinstance(counts, Mapping):
        raise ReconcileError("work list returned an invalid counts object")
    total = counts.get("total")
    if isinstance(total, bool) or not isinstance(total, int) or total < 0:
        raise ReconcileError("work list returned an invalid counts.total value")
    return total


def _pagination_next_token(response: Mapping[str, Any]) -> str:
    """Read the public Work list continuation token without exposing it."""

    context = response.get("paginationContext")
    if context is not None:
        if not isinstance(context, Mapping):
            raise ReconcileError("work list returned an invalid pagination context")
        max_results = context.get("maxResults")
        if max_results is not None:
            if (
                isinstance(max_results, bool)
                or not isinstance(max_results, int)
                or max_results < 0
                or max_results > MAX_WORK_RESULTS_PER_PAGE
            ):
                raise ReconcileError("work list returned an invalid page size")
        token = context.get("nextToken")
    else:
        # These aliases make the read boundary tolerant of older test doubles
        # while the real CLI uses paginationContext.nextToken.
        token = next(
            (response[name] for name in ("nextToken", "next_token", "nextCursor", "next_cursor") if name in response),
            None,
        )
    if token is None or token == "":
        return ""
    if not isinstance(token, str) or not token.strip():
        raise ReconcileError("work list returned an invalid continuation token")
    return token


def _work_list(
    runner: CommandRunner,
    server: str,
    session_id: str,
    *,
    bounded: bool,
    include_superseded: bool,
) -> list[Mapping[str, Any]]:
    """Read a complete board with a bounded, fail-closed page walk.

    The installed ``you`` CLI currently follows continuation tokens itself,
    but retaining the page walk here protects this script when it is paired
    with an older CLI or a direct page-shaped test double. The aggregate CLI
    response has no token and is accepted when its optional count agrees with
    the returned rows.
    """

    common_arguments = [
        "work",
        "list",
        "--session",
        session_id,
    ]
    if not bounded:
        return _work_results(_cli_json(runner, server, *common_arguments))

    common_arguments.extend(
        ["--max-results", str(MAX_WORK_RESULTS_PER_PAGE)]
    )
    if include_superseded:
        common_arguments.append("--all")
    # Counts let us detect a silently truncated aggregate from an older CLI.
    common_arguments.append("--counts")

    rows: list[Mapping[str, Any]] = []
    seen_tokens: set[str] = set()
    next_token = ""
    for page_number in range(1, MAX_WORK_LIST_PAGES + 1):
        arguments = list(common_arguments)
        if next_token:
            arguments.extend(["--next-token", next_token])
        response = _cli_json(runner, server, *arguments)
        if not isinstance(response, Mapping):
            raise ReconcileError("work list returned a non-object JSON value")
        page_rows = _work_results(response)
        total = _work_total(response)
        continuation = _pagination_next_token(response)
        if len(page_rows) > MAX_WORK_RESULTS_PER_PAGE:
            # The current CLI aggregates its own pages before emitting JSON.
            # Permit that shape only when the complete count corroborates it;
            # a single page from an older CLI must stay within the bound.
            if continuation or total is None or len(page_rows) != total:
                raise ReconcileError("work list returned more than the bounded page size")
        rows.extend(page_rows)
        if len(rows) > MAX_WORK_RESULTS_PER_PAGE * MAX_WORK_LIST_PAGES:
            raise ReconcileError("work list exceeds the bounded page limit")
        if total is not None:
            if total > MAX_WORK_RESULTS_PER_PAGE * MAX_WORK_LIST_PAGES:
                raise ReconcileError("work list exceeds the bounded page limit")
            if len(rows) > total:
                raise ReconcileError("work list returned more rows than its count")

        if not continuation:
            if total is not None and len(rows) < total:
                raise ReconcileError("work list ended before its complete result set")
            return rows
        if continuation in seen_tokens:
            raise ReconcileError(
                f"work list pagination repeated a continuation token after page {page_number}"
            )
        seen_tokens.add(continuation)
        next_token = continuation

    raise ReconcileError(
        f"work list pagination exceeded {MAX_WORK_LIST_PAGES} pages"
    )


def _worker_session_results(response: Any) -> list[Mapping[str, Any]]:
    if not isinstance(response, Mapping):
        raise ReconcileError("Worker Session list returned a non-object JSON value")
    sessions = response.get("sessions")
    if sessions is None:
        return []
    if not isinstance(sessions, list) or any(not isinstance(item, Mapping) for item in sessions):
        raise ReconcileError("Worker Session list returned an invalid sessions array")
    return list(sessions)


def _is_superseded(work: Mapping[str, Any]) -> bool:
    value = work.get("supersededBy")
    return isinstance(value, str) and bool(value.strip())


def _session_has_active_work(
    sessions: Iterable[Mapping[str, Any]], work_id: str, session_id: str
) -> bool:
    for session in sessions:
        observed_session_id = _string(session.get("factorySessionId"))
        if observed_session_id and observed_session_id != session_id:
            continue
        if _string(session.get("state")).upper() not in ACTIVE_WORKER_SESSION_STATES:
            continue
        ids = set()
        candidate = session.get("workId")
        if isinstance(candidate, str):
            ids.add(candidate)
        candidates = session.get("workIds")
        if isinstance(candidates, list):
            ids.update(item for item in candidates if isinstance(item, str))
        # The Work-scoped endpoint may omit nullable Work identity fields. An
        # active observation with no identity is still potentially this lead;
        # fail closed rather than dispatching concurrently.
        if not ids:
            return True
        if work_id in ids:
            return True
    return False


def _belongs_to_project(work: Mapping[str, Any], project: Mapping[str, Any]) -> bool:
    name = _work_name(work)
    project_name = _work_name(project)
    if not name or not project_name or _work_type(work) not in CHILD_WORK_TYPES:
        return False

    project_payload = _payload_object(project)
    child_payload = _payload_object(work)
    project_root = _string(project_payload.get("projectRoot"))
    if _string(child_payload.get("project")) == project_name:
        return True
    if project_root and _string(child_payload.get("projectRoot")) == project_root:
        return True

    # Project Lead's documented naming contract is PROJECT-cNN-... for ideas;
    # plan/task/review/validation Work keeps that authored name through the
    # delivery graph.
    return name.startswith(project_name + "-c")


def _project_children(
    works: Iterable[Mapping[str, Any]], project: Mapping[str, Any]
) -> list[Mapping[str, Any]]:
    return [work for work in works if _belongs_to_project(work, project)]


def _same_name_cycles(
    works: Iterable[Mapping[str, Any]], project_name: str
) -> list[Mapping[str, Any]]:
    return [
        work
        for work in works
        if _work_type(work) == "project-cycle"
        and _work_name(work) == project_name
        and not _is_superseded(work)
    ]


def _observation_view(work: Mapping[str, Any]) -> dict[str, Any]:
    """Keep the request fingerprint bounded to public, state-bearing fields."""
    failure = work.get("failureDetail")
    if isinstance(failure, Mapping):
        failure = {
            "reason": _string(failure.get("reason")),
            "message": _string(failure.get("message"))[:512],
        }
    else:
        failure = None
    return {
        "workId": _work_id(work),
        "name": _work_name(work),
        "workTypeName": _work_type(work),
        "state": work.get("state"),
        "currentChainingTraceId": work.get("currentChainingTraceId"),
        "confirmationState": work.get("confirmationState"),
        "failureDetail": failure,
    }


def _observation_revision(
    project: Mapping[str, Any],
    children: Iterable[Mapping[str, Any]],
    cycles: Iterable[Mapping[str, Any]],
    reason: str,
) -> str:
    view = {
        "project": _observation_view(project),
        "children": sorted(
            (_observation_view(child) for child in children),
            key=lambda item: (item["workId"], item["name"]),
        ),
        "cycles": sorted(
            (_observation_view(cycle) for cycle in cycles),
            key=lambda item: (item["workId"], item["name"]),
        ),
        "reason": reason,
    }
    encoded = json.dumps(view, sort_keys=True, separators=(",", ":"), default=str)
    return hashlib.sha256(encoded.encode("utf-8")).hexdigest()[:20]


def _request_id(work_id: str, observation_revision: str) -> str:
    value = re.sub(r"[^A-Za-z0-9_.-]+", "-", work_id).strip("-.")
    value = value or "unknown-project"
    return ("project-reconcile-" + value + "-" + observation_revision)[:REQUEST_ID_LIMIT]


def _move_project(
    runner: CommandRunner,
    server: str,
    session_id: str,
    project: Mapping[str, Any],
    observation_revision: str,
) -> None:
    work_id = _work_id(project)
    if not work_id:
        raise ReconcileError(f"project { _work_name(project)!r } has no workId")
    _cli_json(
        runner,
        server,
        "work",
        "move",
        work_id,
        "init",
        "--session",
        session_id,
        "--request-id",
        _request_id(work_id, observation_revision),
    )


def _record_text(
    record: Mapping[str, Any], names: Iterable[str], label: str
) -> Optional[str]:
    values: list[str] = []
    for name in names:
        if name not in record:
            continue
        value = record[name]
        if not isinstance(value, str) or not value or value.strip() != value:
            raise ReconcileError(f"admission {label} is invalid")
        values.append(value)
    if not values:
        return None
    if any(value != values[0] for value in values[1:]):
        raise ReconcileError(f"admission {label} has conflicting identities")
    return values[0]


def _admission_session_identity(record: Mapping[str, Any]) -> tuple[str, str]:
    session = record.get("session")
    if not isinstance(session, Mapping):
        raise ReconcileError("admission has no bound session")
    session_id = _record_text(
        record,
        ("sessionId", "session_id"),
        "session",
    )
    nested_id = _record_text(
        session,
        ("id", "sessionId", "session_id", "sessionID"),
        "session",
    )
    if session_id and nested_id and session_id != nested_id:
        raise ReconcileError("admission session has conflicting identities")
    session_id = session_id or nested_id
    if not session_id:
        raise ReconcileError("admission has no bound session")

    endpoint = session.get("endpoint")
    endpoint_server: Optional[str] = None
    if isinstance(endpoint, Mapping):
        endpoint_server = _record_text(endpoint, ("server",), "server")
    elif isinstance(endpoint, str):
        if not endpoint or endpoint.strip() != endpoint:
            raise ReconcileError("admission server is invalid")
        endpoint_server = endpoint
    elif endpoint is not None:
        raise ReconcileError("admission server is invalid")
    nested_server = _record_text(session, ("server",), "server")
    if endpoint_server and nested_server and endpoint_server != nested_server:
        raise ReconcileError("admission server has conflicting identities")
    endpoint_server = endpoint_server or nested_server
    top_server = _record_text(record, ("server",), "server")
    if endpoint_server and top_server and endpoint_server != top_server:
        raise ReconcileError("admission server has conflicting identities")
    server = top_server or endpoint_server
    if not server:
        raise ReconcileError("admission has no bound server")
    return session_id, server


def verify_admission(
    *,
    server: str,
    session_id: str,
    root: Optional[Path] = None,
) -> str:
    """Verify the immutable project owner before any public Work mutation."""

    server = server.strip()
    session_id = session_id.strip()
    if not server:
        raise ReconcileError("server is required")
    if not session_id:
        raise ReconcileError("session id is required")
    try:
        root = root or root_path()
        contract = manifest(root)
        record = project_admission.status(root)
    except (
        ContractError,
        project_admission.AdmissionError,
        OSError,
        ValueError,
        subprocess.SubprocessError,
    ) as error:
        raise ReconcileError(f"admission verification failed: {error}") from error
    if not isinstance(contract, Mapping):
        raise ReconcileError("project manifest is invalid")
    contract_project = _record_text(contract, ("project",), "manifest project")
    contract_revision = _record_text(
        contract, ("contractRevision",), "manifest contract revision"
    )
    if not contract_project or not contract_revision:
        raise ReconcileError("project manifest is missing identity")
    if not isinstance(record, Mapping):
        raise ReconcileError("no project admission is active")
    status = record.get("status")
    if status not in {"active", "blocked"}:
        raise ReconcileError("project admission is not active")
    owner_project = _record_text(
        record,
        ("project", "project_id", "projectID"),
        "project owner",
    )
    owner_revision = _record_text(
        record,
        ("contractRevision", "contract_revision"),
        "contract revision",
    )
    if owner_project != contract_project:
        raise ReconcileError("admission owner does not match the project manifest")
    if owner_revision != contract_revision:
        raise ReconcileError("admission contract does not match the project manifest")
    recorded_session, recorded_server = _admission_session_identity(record)
    if recorded_session != session_id:
        raise ReconcileError("admission is bound to a different session")
    if recorded_server != server:
        raise ReconcileError("admission is bound to a different server")
    return owner_project


def reconcile(
    *,
    server: str,
    session_id: str,
    dry_run: bool = False,
    runner: CommandRunner = _run_command,
    project: Optional[str] = None,
    project_id: Optional[str] = None,
    project_name: Optional[str] = None,
    owner_project: Optional[str] = None,
    owner_project_id: Optional[str] = None,
    owner_project_name: Optional[str] = None,
    bounded: Optional[bool] = None,
    include_superseded: Optional[bool] = None,
) -> dict[str, Any]:
    """Inspect one session and return a deterministic reconciliation summary."""
    session_id = session_id.strip()
    server = server.strip()
    if not session_id:
        raise ReconcileError("session id is required")
    if not server:
        raise ReconcileError("server is required")
    selectors = [
        value.strip()
        for value in (
            project,
            project_id,
            project_name,
            owner_project,
            owner_project_id,
            owner_project_name,
        )
        if value is not None
    ]
    if any(not value for value in selectors):
        raise ReconcileError("project identity is required when supplied")
    if selectors and any(value != selectors[0] for value in selectors[1:]):
        raise ReconcileError("conflicting project identities were supplied")
    selected_project = selectors[0] if selectors else None
    if bounded is None:
        # The no-owner form is intentionally kept as a pure fixture seam. The
        # CLI always supplies an owner and therefore takes the bounded path.
        bounded = selected_project is not None
    if include_superseded is None:
        include_superseded = bounded

    session = _cli_json(runner, server, "session", "show", session_id)
    if not _session_is_running(session):
        return {
            "sessionId": session_id,
            "server": server,
            "status": "skipped",
            "reason": "factory-not-running",
            "moved": [],
            "skipped": [],
        }

    works = _work_list(
        runner,
        server,
        session_id,
        bounded=bool(bounded),
        include_superseded=bool(include_superseded),
    )
    projects = [work for work in works if _work_type(work) == "project"]
    if selected_project:
        matching_ids = {
            _work_id(work) for work in projects if _work_id(work) == selected_project
        }
        if matching_ids:
            projects = [work for work in projects if _work_id(work) == selected_project]
        else:
            projects = [work for work in projects if _work_name(work) == selected_project]
        if not projects:
            raise ReconcileError(
                f"admitted project {selected_project!r} is absent from the work list"
            )
    grouped: dict[str, list[Mapping[str, Any]]] = {}
    for project in projects:
        grouped.setdefault(_work_name(project), []).append(project)

    moved: list[dict[str, Any]] = []
    skipped: list[dict[str, str]] = []
    for project_name in sorted(grouped):
        candidates = grouped[project_name]
        if not project_name:
            skipped.append({"reason": "project-without-name"})
            continue
        if len(candidates) != 1:
            skipped.append({"name": project_name, "reason": "ambiguous-project-name"})
            continue
        project = candidates[0]
        state_name, _ = _state(project)
        if state_name not in {"waiting", "blocked"}:
            continue
        if state_name == "blocked":
            skipped.append({"name": project_name, "reason": "blocked-inspect-only"})
            continue

        children = _project_children(works, project)
        project_id = _work_id(project)
        if not project_id:
            skipped.append({"name": project_name, "reason": "project-without-work-id"})
            continue

        cycles = _same_name_cycles(works, project_name)
        # A cycle of any state is left to the authored graph. Moving the
        # project while that cycle remains visible can race its completion
        # transition and create overlapping same-name cycles.
        if cycles:
            reason = "cycle-in-progress" if any(
                _state(cycle)[1] in NONTERMINAL_STATE_TYPES for cycle in cycles
            ) else "cycle-transition-pending"
            skipped.append({"name": project_name, "reason": reason})
            continue

        # With no visible cycle, a waiting lead is structurally stranded. This
        # is the public-state staleness signal; no filesystem clock is used.
        reason = "missing-cycle"

        worker_sessions = _worker_session_results(
            _cli_json(
                runner,
                server,
                "worker-sessions",
                "list",
                "--work-id",
                project_id,
                "--session",
                session_id,
            )
        )
        if _session_has_active_work(worker_sessions, project_id, session_id):
            skipped.append({"name": project_name, "reason": "project-lead-active"})
            continue

        observation_revision = _observation_revision(
            project, children, cycles, reason
        )
        unfinished_children = [
            _work_id(child)
            for child in children
            if _state(child)[1] in NONTERMINAL_STATE_TYPES
        ]
        detail: dict[str, Any] = {
            "name": project_name,
            "workId": project_id,
            "reason": reason,
            "observationRevision": observation_revision,
        }
        if unfinished_children:
            detail["unfinishedChildren"] = unfinished_children
        if dry_run:
            moved.append(detail)
            continue
        _move_project(runner, server, session_id, project, observation_revision)
        moved.append(detail)

    return {
        "sessionId": session_id,
        "server": server,
        "status": "dry-run" if dry_run else "completed",
        "moved": moved,
        "skipped": skipped,
    }


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--server", default=DEFAULT_SERVER)
    parser.add_argument("--session", required=True)
    parser.add_argument("--dry-run", action="store_true")
    return parser


def main(argv: Optional[list[str]] = None) -> int:
    args = _parser().parse_args(argv)
    try:
        owner_project = verify_admission(
            server=args.server,
            session_id=args.session,
        )
        result = reconcile(
            server=args.server,
            session_id=args.session,
            dry_run=args.dry_run,
            owner_project_name=owner_project,
            bounded=True,
            include_superseded=True,
        )
    except ReconcileError as error:
        print(f"project reconciliation failed: {error}", file=sys.stderr)
        return 1
    print(json.dumps(result, sort_keys=True, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
