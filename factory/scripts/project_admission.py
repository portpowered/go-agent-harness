#!/usr/bin/env python3
"""Durable ownership for the single-project factory.

The factory scheduler owns work and task state.  This module owns only the
small admission record that prevents two roots (or two Git worktrees) from
starting different project sessions at the same time.  The record lives in
the repository's Git common directory, which is shared by linked worktrees.

The Python API is intentionally usable without the CLI::

    admission = ProjectAdmission(repo_path)
    admission.Admit("project-1", "contract-v1")
    admission.BindEndpoint("project-1", "session-1", "factory://local/1")
    admission.Release("project-1", {"outcome": "complete"})

``Admit`` is idempotent for the current project and contract revision.  An
old record is never released because its age elapsed; only an explicit
``Release`` with terminal evidence makes the record available to a new
project.  A crashed process leaves both the record and any lock file in
place.  The operating system releases an interprocess lock held by a dead
process, while the durable owner remains unchanged.
"""

from __future__ import annotations

import argparse
import contextlib
import copy
import json
import os
import stat
import subprocess
import sys
import tempfile
import threading
import time
from pathlib import Path
from typing import Any, Dict, Iterator, Mapping, Optional, Sequence, Tuple, Union


try:
    import fcntl
except ImportError:  # pragma: no cover - exercised on Windows hosts.
    fcntl = None  # type: ignore[assignment]


STATE_SCHEMA = "factory.project-admission.v1"
STATE_VERSION = 1
STATE_FILENAME = "factory-project-admission.json"
LOCK_FILENAME = "factory-project-admission.lock"
# Descriptive aliases for callers that prefer the longer constant names.
ADMISSION_STATE_FILENAME = STATE_FILENAME
ADMISSION_LOCK_FILENAME = LOCK_FILENAME

_JsonObject = Dict[str, Any]
_Endpoint = Union[str, Mapping[str, Any]]


class AdmissionError(RuntimeError):
    """Base class for errors that should fail closed at a launcher boundary."""

    code = "admission_error"
    exit_code = 1


class AdmissionConflictError(AdmissionError):
    """A different project or identity already owns the admission."""

    code = "admission_conflict"
    exit_code = 3


class AdmissionIdentityMismatchError(AdmissionConflictError):
    """A session or endpoint does not match the recorded identity."""

    code = "identity_mismatch"


class AdmissionStateError(AdmissionError):
    """The durable record or its storage boundary is unsafe or malformed."""

    code = "invalid_admission_state"
    exit_code = 4


class AdmissionNotFoundError(AdmissionError):
    """An operation requires an active admission but none exists."""

    code = "admission_not_found"
    exit_code = 5


class AdmissionValidationError(AdmissionError):
    """An API argument is not a valid admission value."""

    code = "invalid_admission_argument"
    exit_code = 2


_THREAD_LOCKS: Dict[str, threading.Lock] = {}
_THREAD_LOCKS_GUARD = threading.Lock()


def _absolute_path(path: Union[str, os.PathLike[str]]) -> Path:
    """Normalize a path without resolving a possibly unsafe final symlink."""

    return Path(os.path.abspath(os.fspath(Path(path).expanduser())))


def _path_key(path: Path) -> str:
    """Return a stable key for the process-local lock map."""

    return os.path.normcase(str(path.resolve()))


def _thread_lock(path: Path) -> threading.Lock:
    key = _path_key(path)
    with _THREAD_LOCKS_GUARD:
        return _THREAD_LOCKS.setdefault(key, threading.Lock())


def _run_git_common_dir(repo_path: Path) -> Path:
    try:
        result = subprocess.run(
            ["git", "rev-parse", "--git-common-dir"],
            cwd=str(repo_path),
            capture_output=True,
            text=True,
            check=False,
        )
    except OSError as exc:
        raise AdmissionStateError(f"could not run git to resolve common directory: {exc}") from exc

    if result.returncode != 0:
        detail = (result.stderr or result.stdout).strip() or "git returned no details"
        raise AdmissionStateError(f"could not resolve Git common directory: {detail}")

    value = result.stdout.strip()
    if not value:
        raise AdmissionStateError("git returned an empty common directory")

    common_dir = Path(value)
    if not common_dir.is_absolute():
        common_dir = repo_path / common_dir
    common_dir = common_dir.resolve()
    if not common_dir.is_dir():
        raise AdmissionStateError(f"Git common directory is not a directory: {common_dir}")
    return common_dir


def git_common_dir(repo_path: Optional[Union[str, os.PathLike[str]]] = None) -> Path:
    """Resolve the shared Git common directory for a checkout or worktree."""

    path = Path(repo_path or os.getcwd()).expanduser().resolve()
    if not path.exists() or not path.is_dir():
        raise AdmissionStateError(f"repository path is not a directory: {path}")
    return _run_git_common_dir(path)


def admission_state_path(
    repo_path: Optional[Union[str, os.PathLike[str]]] = None,
) -> Path:
    """Return the durable admission JSON path in the Git common directory."""

    return git_common_dir(repo_path) / STATE_FILENAME


def admission_lock_path(
    repo_path: Optional[Union[str, os.PathLike[str]]] = None,
) -> Path:
    """Return the interprocess lock path in the Git common directory."""

    return git_common_dir(repo_path) / LOCK_FILENAME


state_path = admission_state_path
lock_path = admission_lock_path


def _require_text(value: Any, label: str) -> str:
    if not isinstance(value, str) or not value or value.strip() != value or "\x00" in value:
        raise AdmissionValidationError(
            f"{label} must be a non-empty string without surrounding whitespace or NUL"
        )
    return value


def _json_copy(value: Any, label: str) -> Any:
    """Validate and detach a JSON value, rejecting non-standard NaN values."""

    try:
        encoded = json.dumps(
            value,
            ensure_ascii=False,
            sort_keys=True,
            separators=(",", ":"),
            allow_nan=False,
        )
        return json.loads(encoded)
    except (TypeError, ValueError, json.JSONDecodeError) as exc:
        raise AdmissionValidationError(f"{label} must be JSON-serializable") from exc


def _endpoint_copy(endpoint: Any) -> Any:
    if isinstance(endpoint, str):
        return _require_text(endpoint, "endpoint identity")
    if not isinstance(endpoint, Mapping):
        raise AdmissionValidationError("endpoint identity must be a non-empty string or JSON object")
    if not endpoint:
        raise AdmissionValidationError("endpoint identity must not be empty")
    return _json_copy(dict(endpoint), "endpoint identity")


def _evidence_copy(evidence: Any) -> Any:
    copied = _json_copy(evidence, "terminal evidence")
    if (
        copied is None
        or copied is False
        or copied == 0
        or copied == ""
        or copied == {}
        or copied == []
    ):
        raise AdmissionValidationError("terminal evidence is required and must not be empty")
    return copied


def _reject_duplicate_json_keys(pairs: Sequence[Tuple[str, Any]]) -> _JsonObject:
    result: _JsonObject = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def _endpoint_owner_value(endpoint: Any, *names: str) -> Any:
    if not isinstance(endpoint, Mapping):
        return None
    values = []
    for name in names:
        if name in endpoint:
            values.append(endpoint[name])
    identity = endpoint.get("identity")
    if isinstance(identity, Mapping):
        for name in names:
            if name in identity:
                values.append(identity[name])
    if values and any(value != values[0] for value in values[1:]):
        raise AdmissionIdentityMismatchError(
            "endpoint or recording identity contains conflicting owner fields"
        )
    return values[0] if values else None


def _validate_endpoint_ownership(endpoint: Any, project_id: str, session_id: str) -> None:
    """Reject endpoint metadata that explicitly names another owner."""

    endpoint_project = _endpoint_owner_value(
        endpoint,
        "project_id",
        "projectID",
        "project",
    )
    if endpoint_project is not None and endpoint_project != project_id:
        raise AdmissionIdentityMismatchError(
            f"endpoint belongs to project {endpoint_project!r}, not {project_id!r}"
        )
    endpoint_session = _endpoint_owner_value(
        endpoint,
        "session_id",
        "sessionID",
        "sessionId",
    )
    if endpoint_session is not None and endpoint_session != session_id:
        raise AdmissionIdentityMismatchError(
            f"endpoint belongs to session {endpoint_session!r}, not {session_id!r}"
        )


def _validate_recording_ownership(
    evidence: Any,
    project_id: str,
    source_session_id: str,
) -> None:
    """Reject recording metadata that explicitly names another owner."""

    if not isinstance(evidence, Mapping):
        return
    evidence_project = _endpoint_owner_value(
        evidence,
        "project_id",
        "projectID",
        "project",
    )
    if evidence_project is not None and evidence_project != project_id:
        raise AdmissionIdentityMismatchError(
            f"recording evidence belongs to project {evidence_project!r}, not {project_id!r}"
        )
    evidence_session = _endpoint_owner_value(
        evidence,
        "source_session_id",
        "sourceSessionID",
        "session_id",
        "sessionID",
        "sessionId",
    )
    if evidence_session is not None and evidence_session != source_session_id:
        raise AdmissionIdentityMismatchError(
            f"recording evidence belongs to session {evidence_session!r}, not {source_session_id!r}"
        )


def _server_from_endpoint(endpoint: Any) -> Optional[str]:
    if isinstance(endpoint, str):
        return endpoint
    if isinstance(endpoint, Mapping):
        server = endpoint.get("server")
        if isinstance(server, str):
            return server
    return None


def _identity_from_state(state: Mapping[str, Any]) -> Tuple[str, str]:
    project_id = state.get("project_id")
    contract_revision = state.get("contract_revision")
    if not isinstance(project_id, str) or not isinstance(contract_revision, str):
        raise AdmissionStateError("admission state has no complete project identity")
    return project_id, contract_revision


def _canonical_state(raw: Any) -> _JsonObject:
    """Validate a state file and return its canonical in-memory representation."""

    if not isinstance(raw, dict):
        raise AdmissionStateError("admission state must be a JSON object")

    # The aliases make an interrupted migration readable without writing a
    # second representation.  New writes always use the snake_case fields.
    schema = raw.get("schema", STATE_SCHEMA)
    version = raw.get("version", STATE_VERSION)
    if schema != STATE_SCHEMA or version != STATE_VERSION:
        raise AdmissionStateError(
            f"unsupported admission state schema/version: {schema!r}/{version!r}"
        )
    allowed_keys = {
        "schema",
        "version",
        "status",
        "session",
        "project_id",
        "projectID",
        "contract_revision",
        "contractRevision",
        "release_evidence",
        "releaseEvidence",
        "terminal_evidence",
        "terminalEvidence",
    }
    unknown_keys = set(raw) - allowed_keys
    if unknown_keys:
        raise AdmissionStateError(
            "admission state has unsupported fields: "
            + ", ".join(sorted(str(key) for key in unknown_keys))
        )

    aliases = {
        "project_id": ("project_id", "projectID"),
        "contract_revision": ("contract_revision", "contractRevision"),
        "release_evidence": (
            "release_evidence",
            "releaseEvidence",
            "terminal_evidence",
            "terminalEvidence",
        ),
    }
    values: _JsonObject = {}
    for canonical_name, names in aliases.items():
        found = [name for name in names if name in raw]
        if len(found) > 1:
            first = raw[found[0]]
            if any(raw[name] != first for name in found[1:]):
                raise AdmissionStateError(f"admission state contains conflicting {canonical_name} fields")
        if found:
            values[canonical_name] = raw[found[0]]

    project_id = values.get("project_id")
    contract_revision = values.get("contract_revision")
    if not isinstance(project_id, str) or not project_id or "\x00" in project_id:
        raise AdmissionStateError("admission state has an invalid project_id")
    if not isinstance(contract_revision, str) or not contract_revision or "\x00" in contract_revision:
        raise AdmissionStateError("admission state has an invalid contract_revision")

    status = raw.get("status", "active")
    if status not in {"active", "blocked", "released"}:
        raise AdmissionStateError(f"unknown admission state status: {status!r}")

    session = raw.get("session")
    if session is not None:
        if not isinstance(session, dict):
            raise AdmissionStateError("admission state session must be an object or null")
        session_unknown = set(session) - {
            "id",
            "session_id",
            "sessionID",
            "sessionId",
            "endpoint",
            "resume",
        }
        if session_unknown:
            raise AdmissionStateError(
                "admission state session has unsupported fields: "
                + ", ".join(sorted(str(key) for key in session_unknown))
            )
        session_id_values = [
            session[name]
            for name in ("id", "session_id", "sessionID", "sessionId")
            if name in session
        ]
        if session_id_values and any(
            value != session_id_values[0] for value in session_id_values[1:]
        ):
            raise AdmissionStateError("admission state session has conflicting ids")
        session_id = session_id_values[0] if session_id_values else None
        if not isinstance(session_id, str) or not session_id or "\x00" in session_id:
            raise AdmissionStateError("admission state session has an invalid id")
        if "endpoint" not in session:
            raise AdmissionStateError("admission state session has no endpoint identity")
        try:
            endpoint = _endpoint_copy(session["endpoint"])
        except AdmissionError as exc:
            raise AdmissionStateError(str(exc)) from exc
        session = {"id": session_id, "endpoint": endpoint}
        if "resume" in raw.get("session", {}):
            resume = raw["session"]["resume"]
            if not isinstance(resume, dict):
                raise AdmissionStateError("admission state session.resume must be an object")
            previous_session_id = resume.get(
                "previous_session_id",
                resume.get("previousSessionID"),
            )
            if (
                not isinstance(previous_session_id, str)
                or not previous_session_id
                or "\x00" in previous_session_id
            ):
                raise AdmissionStateError(
                    "admission state session.resume has an invalid previous session id"
                )
            if "recording_evidence" not in resume and "recordingEvidence" not in resume:
                raise AdmissionStateError(
                    "admission state session.resume has no recording evidence"
                )
            recording = resume.get("recording_evidence", resume.get("recordingEvidence"))
            try:
                recording = _evidence_copy(recording)
            except AdmissionError as exc:
                raise AdmissionStateError(str(exc)) from exc
            session["resume"] = {
                "previous_session_id": previous_session_id,
                "recording_evidence": recording,
            }
            try:
                _validate_recording_ownership(
                    recording,
                    project_id,
                    previous_session_id,
                )
            except AdmissionError as exc:
                raise AdmissionStateError(str(exc)) from exc
        try:
            _validate_endpoint_ownership(endpoint, project_id, session_id)
        except AdmissionError as exc:
            raise AdmissionStateError(str(exc)) from exc

    result: _JsonObject = {
        "schema": STATE_SCHEMA,
        "version": STATE_VERSION,
        "status": status,
        "project_id": project_id,
        "contract_revision": contract_revision,
        "session": session,
    }

    if status == "released":
        if "release_evidence" not in values:
            raise AdmissionStateError("released admission state has no terminal evidence")
        try:
            result["release_evidence"] = _evidence_copy(values["release_evidence"])
        except AdmissionError as exc:
            raise AdmissionStateError(str(exc)) from exc
    elif "release_evidence" in values:
        raise AdmissionStateError("active admission state cannot contain release evidence")

    return result


class ProjectAdmission:
    """Durable, lock-serialized single-project admission owner."""

    def __init__(
        self,
        repo_path: Optional[Union[str, os.PathLike[str]]] = None,
        *,
        state_path: Optional[Union[str, os.PathLike[str]]] = None,
        lock_path: Optional[Union[str, os.PathLike[str]]] = None,
    ) -> None:
        if state_path is None:
            state = admission_state_path(repo_path)
        else:
            state = _absolute_path(state_path)
        if lock_path is None:
            if state_path is None:
                lock = admission_lock_path(repo_path)
            else:
                lock = state.parent / LOCK_FILENAME
        else:
            lock = _absolute_path(lock_path)
        self.state_path = state
        self.lock_path = lock

    @property
    def repo_state_path(self) -> Path:
        """Compatibility alias used by launchers that call this a state path."""

        return self.state_path

    def _check_existing_regular_file(self, path: Path, description: str) -> None:
        try:
            info = path.lstat()
        except FileNotFoundError:
            return
        except OSError as exc:
            raise AdmissionStateError(f"could not inspect {description}: {exc}") from exc
        if stat.S_ISLNK(info.st_mode):
            raise AdmissionStateError(f"refusing symlink {description}: {path}")
        if not stat.S_ISREG(info.st_mode):
            raise AdmissionStateError(f"{description} is not a regular file: {path}")

    @contextlib.contextmanager
    def _lock(self, *, create: bool = True) -> Iterator[None]:
        """Hold both a process-local and OS-level lock for this repository."""

        thread_lock = _thread_lock(self.lock_path)
        with thread_lock:
            if create:
                self.lock_path.parent.mkdir(parents=True, exist_ok=True)
            self._check_existing_regular_file(self.lock_path, "admission lock")
            if not create and not self.lock_path.exists():
                yield
                return

            flags = os.O_RDWR
            if create:
                flags |= os.O_CREAT
            no_follow = getattr(os, "O_NOFOLLOW", 0)
            flags |= no_follow
            try:
                fd = os.open(self.lock_path, flags, 0o600)
            except OSError as exc:
                raise AdmissionStateError(f"could not open admission lock: {exc}") from exc
            try:
                try:
                    os.fchmod(fd, 0o600)
                except OSError as exc:
                    raise AdmissionStateError(f"could not secure admission lock: {exc}") from exc
                try:
                    if os.fstat(fd).st_size == 0:
                        os.write(fd, b"\0")
                        os.fsync(fd)
                except OSError as exc:
                    raise AdmissionStateError(f"could not initialize admission lock: {exc}") from exc
                try:
                    if fcntl is not None:
                        fcntl.flock(fd, fcntl.LOCK_EX)
                    else:
                        import msvcrt

                        while True:
                            try:
                                os.lseek(fd, 0, os.SEEK_SET)
                                msvcrt.locking(fd, msvcrt.LK_NBLCK, 1)
                                break
                            except OSError:
                                time.sleep(0.05)
                except OSError as exc:
                    raise AdmissionStateError(f"could not acquire admission lock: {exc}") from exc
                try:
                    yield
                finally:
                    try:
                        if fcntl is not None:
                            fcntl.flock(fd, fcntl.LOCK_UN)
                        else:
                            import msvcrt

                            os.lseek(fd, 0, os.SEEK_SET)
                            msvcrt.locking(fd, msvcrt.LK_UNLCK, 1)
                    except OSError:
                        pass
            finally:
                os.close(fd)

    def _read_state_unlocked(self) -> Optional[_JsonObject]:
        self._check_existing_regular_file(self.state_path, "admission state")
        try:
            with self.state_path.open("r", encoding="utf-8") as handle:
                raw = json.load(handle, object_pairs_hook=_reject_duplicate_json_keys)
        except FileNotFoundError:
            return None
        except (OSError, UnicodeError, ValueError) as exc:
            raise AdmissionStateError(f"could not read admission state {self.state_path}: {exc}") from exc
        return _canonical_state(raw)

    def _write_state_unlocked(self, state: Mapping[str, Any]) -> None:
        canonical = _canonical_state(dict(state))
        self.state_path.parent.mkdir(parents=True, exist_ok=True)
        self._check_existing_regular_file(self.state_path, "admission state")
        temporary_path: Optional[Path] = None
        fd: Optional[int] = None
        try:
            fd, name = tempfile.mkstemp(
                prefix=f".{self.state_path.name}.",
                suffix=".tmp",
                dir=str(self.state_path.parent),
            )
            temporary_path = Path(name)
            os.fchmod(fd, 0o600)
            payload = json.dumps(
                canonical,
                ensure_ascii=False,
                sort_keys=True,
                separators=(",", ":"),
                allow_nan=False,
            ).encode("utf-8")
            with os.fdopen(fd, "wb") as handle:
                fd = None
                handle.write(payload)
                handle.write(b"\n")
                handle.flush()
                os.fsync(handle.fileno())
            os.replace(temporary_path, self.state_path)
            temporary_path = None
            # The temporary file was created with 0600.  Keep this explicit in
            # case a platform's replace operation applies target metadata.
            os.chmod(self.state_path, 0o600)
            try:
                directory_fd = os.open(self.state_path.parent, os.O_RDONLY)
            except OSError:
                directory_fd = None
            if directory_fd is not None:
                try:
                    os.fsync(directory_fd)
                except OSError:
                    pass
                finally:
                    os.close(directory_fd)
        except OSError as exc:
            raise AdmissionStateError(f"could not atomically write admission state: {exc}") from exc
        finally:
            if fd is not None:
                os.close(fd)
            if temporary_path is not None:
                try:
                    temporary_path.unlink()
                except FileNotFoundError:
                    pass
                except OSError:
                    pass

    def Status(self) -> _JsonObject:
        """Return a consistent snapshot without changing admission state."""

        # Atomic replace makes an unlocked read safe with respect to partial
        # JSON.  If a lock already exists, taking it also gives a stable
        # snapshot relative to an in-flight mutation; a status query on a
        # fresh repository does not create a lock file as a side effect.
        with self._lock(create=False):
            state = self._read_state_unlocked()
        if state is None:
            return {
                "schema": STATE_SCHEMA,
                "version": STATE_VERSION,
                "status": "free",
                "admitted": False,
            }
        result = copy.deepcopy(state)
        result["admitted"] = result["status"] in {"active", "blocked"}
        return result

    status = Status

    def Admit(
        self,
        project_id: str,
        contract_revision: str,
        session_id: Optional[str] = None,
        endpoint: Optional[_Endpoint] = None,
    ) -> _JsonObject:
        """Atomically admit one project, or replay its identical admission."""

        project_id = _require_text(project_id, "project_id")
        contract_revision = _require_text(contract_revision, "contract_revision")
        if (session_id is None) != (endpoint is None):
            raise AdmissionValidationError(
                "session_id and endpoint must be supplied together"
            )
        if session_id is not None:
            session_id = _require_text(session_id, "session_id")
        normalized_endpoint = _endpoint_copy(endpoint) if endpoint is not None else None
        if normalized_endpoint is not None:
            _validate_endpoint_ownership(normalized_endpoint, project_id, session_id or "")

        with self._lock():
            current = self._read_state_unlocked()
            if current is not None and current["status"] in {"active", "blocked"}:
                current_project, current_revision = _identity_from_state(current)
                if (current_project, current_revision) != (project_id, contract_revision):
                    raise AdmissionConflictError(
                        "project admission is already owned by "
                        f"{current_project!r} at contract {current_revision!r}"
                    )
                if session_id is not None or normalized_endpoint is not None:
                    self._check_session_match(
                        current,
                        session_id,
                        normalized_endpoint,
                        require_endpoint=normalized_endpoint is not None,
                    )
                    if current.get("session") is None:
                        current["session"] = {"id": session_id, "endpoint": normalized_endpoint}
                        self._write_state_unlocked(current)
                return copy.deepcopy(current)

            state: _JsonObject = {
                "schema": STATE_SCHEMA,
                "version": STATE_VERSION,
                "status": "active",
                "project_id": project_id,
                "contract_revision": contract_revision,
                "session": None,
            }
            if session_id is not None:
                state["session"] = {"id": session_id, "endpoint": normalized_endpoint}
            self._write_state_unlocked(state)
            return copy.deepcopy(state)

    admit = Admit

    def _check_session_match(
        self,
        current: Mapping[str, Any],
        session_id: Optional[str],
        endpoint: Any,
        *,
        require_endpoint: bool,
    ) -> None:
        session = current.get("session")
        if session is None:
            if session_id is None and endpoint is None and not require_endpoint:
                return
            if session_id is None or endpoint is None:
                raise AdmissionIdentityMismatchError("admission has no bound session endpoint")
            _validate_endpoint_ownership(endpoint, current["project_id"], session_id)
            return
        current_id = session["id"]
        if session_id is not None and current_id != session_id:
            raise AdmissionIdentityMismatchError(
                f"admission is bound to session {current_id!r}, not {session_id!r}"
            )
        if endpoint is not None and session["endpoint"] != endpoint:
            # The CLI accepts a bare endpoint string while the launcher-facing
            # bind/resume helpers persist ``{"server": value}``.  Treat those
            # two spellings as the same identity only when the server value is
            # exact; structured endpoint metadata still compares strictly.
            if not (
                isinstance(endpoint, str)
                and _server_from_endpoint(session["endpoint"]) == endpoint
            ):
                raise AdmissionIdentityMismatchError(
                    "session endpoint identity does not match admission"
                )
        if require_endpoint and endpoint is None:
            raise AdmissionValidationError("endpoint identity is required")

    def BindEndpoint(
        self,
        project_id: str,
        session_id: str,
        endpoint: _Endpoint,
        contract_revision: Optional[str] = None,
    ) -> _JsonObject:
        """Bind an endpoint once, requiring exact project/session identity."""

        project_id = _require_text(project_id, "project_id")
        session_id = _require_text(session_id, "session_id")
        if contract_revision is not None:
            contract_revision = _require_text(contract_revision, "contract_revision")
        normalized_endpoint = _endpoint_copy(endpoint)
        _validate_endpoint_ownership(normalized_endpoint, project_id, session_id)

        with self._lock():
            current = self._read_state_unlocked()
            if current is None or current["status"] not in {"active", "blocked"}:
                raise AdmissionNotFoundError("cannot bind an endpoint without an active admission")
            if current["project_id"] != project_id:
                raise AdmissionConflictError(
                    f"project admission is owned by {current['project_id']!r}, not {project_id!r}"
                )
            if contract_revision is not None and current["contract_revision"] != contract_revision:
                raise AdmissionIdentityMismatchError("contract revision does not match admission")
            self._check_session_match(
                current,
                session_id,
                normalized_endpoint,
                require_endpoint=True,
            )
            if current.get("session") is None:
                current["session"] = {"id": session_id, "endpoint": normalized_endpoint}
                self._write_state_unlocked(current)
            return copy.deepcopy(current)

    bind_endpoint = BindEndpoint

    def BindSession(
        self,
        project_id: str,
        contract_revision: str,
        session_id: str,
        endpoint: _Endpoint,
    ) -> _JsonObject:
        """Bind an endpoint while requiring the complete admission identity."""

        return self.BindEndpoint(project_id, session_id, endpoint, contract_revision)

    bind_session = BindSession

    def ResumeSession(
        self,
        project_id: str,
        contract_revision: str,
        server: str,
        previous_session_id: str,
        new_session_id: str,
        recording_evidence: Any,
    ) -> _JsonObject:
        """Compare-and-swap a crashed session to a verified replacement.

        The caller remains responsible for stopping the old process and
        validating the recording hash against canonical runtime evidence.
        This owner verifies the identity portion of that proof and performs
        the session replacement under the same admission lock as all other
        mutations.  A retry with the old session id fails after the swap,
        which keeps a second live session from being silently attached.
        """

        project_id = _require_text(project_id, "project_id")
        contract_revision = _require_text(contract_revision, "contract_revision")
        server = _require_text(server, "server")
        previous_session_id = _require_text(previous_session_id, "previous_session_id")
        new_session_id = _require_text(new_session_id, "new_session_id")
        if previous_session_id == new_session_id:
            raise AdmissionIdentityMismatchError(
                "new session id must differ from previous session id"
            )
        recording = _evidence_copy(recording_evidence)
        _validate_recording_ownership(recording, project_id, previous_session_id)

        with self._lock():
            current = self._read_state_unlocked()
            if current is None or current["status"] not in {"active", "blocked"}:
                raise AdmissionNotFoundError(
                    "cannot resume a session without an active admission"
                )
            if current["project_id"] != project_id:
                raise AdmissionIdentityMismatchError(
                    f"admission belongs to project {current['project_id']!r}, not {project_id!r}"
                )
            if current["contract_revision"] != contract_revision:
                raise AdmissionIdentityMismatchError(
                    "contract revision does not match admission"
                )
            session = current.get("session")
            if not isinstance(session, Mapping):
                raise AdmissionIdentityMismatchError(
                    "admission has no session binding to resume"
                )
            if session.get("id") != previous_session_id:
                raise AdmissionIdentityMismatchError(
                    f"admission is bound to session {session.get('id')!r}, not {previous_session_id!r}"
                )
            recorded_server = _server_from_endpoint(session.get("endpoint"))
            if recorded_server != server:
                raise AdmissionIdentityMismatchError(
                    f"admission is bound to server {recorded_server!r}, not {server!r}"
                )
            current["session"] = {
                "id": new_session_id,
                "endpoint": {"server": server},
                "resume": {
                    "previous_session_id": previous_session_id,
                    "recording_evidence": recording,
                },
            }
            self._write_state_unlocked(current)
            return copy.deepcopy(current)

    resume_session = ResumeSession

    def Release(
        self,
        project_id: str,
        terminal_evidence: Any = None,
        contract_revision: Optional[str] = None,
        session_id: Optional[str] = None,
        endpoint: Optional[_Endpoint] = None,
    ) -> _JsonObject:
        """Release matching ownership with durable terminal evidence.

        The documented short form is ``Release(project_id, evidence)``.  A
        caller that has the immutable contract revision may pass it as the
        third argument (or use the CLI option); session and endpoint are
        optional additional checks when the launcher has bound them.
        """

        # Also accept the complete positional form Release(project, revision,
        # evidence), while keeping the documented short form
        # Release(project, evidence).
        if terminal_evidence is not None and not isinstance(contract_revision, (str, type(None))):
            terminal_evidence, contract_revision = contract_revision, terminal_evidence
        project_id = _require_text(project_id, "project_id")
        if contract_revision is not None:
            contract_revision = _require_text(contract_revision, "contract_revision")
        if session_id is not None:
            session_id = _require_text(session_id, "session_id")
        normalized_endpoint = _endpoint_copy(endpoint) if endpoint is not None else None
        if normalized_endpoint is not None and session_id is None:
            raise AdmissionValidationError("session_id is required when an endpoint is supplied")
        evidence = _evidence_copy(terminal_evidence)

        with self._lock():
            current = self._read_state_unlocked()
            if current is None:
                raise AdmissionNotFoundError("cannot release without an active admission")
            current_project, current_revision = _identity_from_state(current)
            if current_project != project_id:
                raise AdmissionIdentityMismatchError(
                    f"cannot release project {project_id!r}; admission belongs to {current_project!r}"
                )
            if contract_revision is not None and current_revision != contract_revision:
                raise AdmissionIdentityMismatchError("contract revision does not match admission")
            current_session = current.get("session")
            evidence_session_id = session_id
            if evidence_session_id is None and isinstance(current_session, Mapping):
                evidence_session_id = current_session.get("id")
            if evidence_session_id is not None:
                _validate_recording_ownership(
                    evidence,
                    current_project,
                    evidence_session_id,
                )
            if current["status"] == "released":
                if current.get("release_evidence") == evidence:
                    return copy.deepcopy(current)
                raise AdmissionConflictError("admission has already been released with different evidence")
            self._check_session_match(
                current,
                session_id,
                normalized_endpoint,
                require_endpoint=normalized_endpoint is not None,
            )
            current["status"] = "released"
            current["release_evidence"] = evidence
            self._write_state_unlocked(current)
            return copy.deepcopy(current)

    release = Release


AdmissionStore = ProjectAdmission


# Module-level operations make the intended root-launcher interface easy to
# discover and keep tests independent of the class name.
def Admit(
    project_id: str,
    contract_revision: str,
    repo_path: Optional[Union[str, os.PathLike[str]]] = None,
    session_id: Optional[str] = None,
    endpoint: Optional[_Endpoint] = None,
) -> _JsonObject:
    return ProjectAdmission(repo_path).Admit(project_id, contract_revision, session_id, endpoint)


def BindEndpoint(
    project_id: str,
    session_id: str,
    endpoint: _Endpoint,
    repo_path: Optional[Union[str, os.PathLike[str]]] = None,
    contract_revision: Optional[str] = None,
) -> _JsonObject:
    return ProjectAdmission(repo_path).BindEndpoint(
        project_id, session_id, endpoint, contract_revision
    )


def BindSession(
    project_id: str,
    contract_revision: str,
    session_id: str,
    endpoint: _Endpoint,
    repo_path: Optional[Union[str, os.PathLike[str]]] = None,
) -> _JsonObject:
    return ProjectAdmission(repo_path).BindSession(
        project_id, contract_revision, session_id, endpoint
    )


def Release(
    project_id: str,
    terminal_evidence: Any,
    repo_path: Optional[Union[str, os.PathLike[str]]] = None,
    contract_revision: Optional[str] = None,
    session_id: Optional[str] = None,
    endpoint: Optional[_Endpoint] = None,
) -> _JsonObject:
    return ProjectAdmission(repo_path).Release(
        project_id,
        terminal_evidence,
        contract_revision,
        session_id,
        endpoint,
    )


def Status(
    repo_path: Optional[Union[str, os.PathLike[str]]] = None,
) -> _JsonObject:
    return ProjectAdmission(repo_path).Status()


def _launcher_view(state: Optional[Mapping[str, Any]]) -> Optional[_JsonObject]:
    """Expose the manifest vocabulary expected by the root launcher.

    The JSON file intentionally keeps one canonical spelling for each field.
    These aliases are returned only by the small launcher-facing functions so
    scheduler packets and manifest checks can use their existing
    ``project``/``contractRevision`` names without making the persisted record
    redundant.
    """

    if state is None:
        return None
    result = copy.deepcopy(dict(state))
    if "project_id" in result:
        result["project"] = result["project_id"]
        result["projectID"] = result["project_id"]
    if "contract_revision" in result:
        result["contractRevision"] = result["contract_revision"]
    session = result.get("session")
    if isinstance(session, Mapping):
        session_view = dict(session)
        session_view["session_id"] = session.get("id")
        session_view["sessionId"] = session.get("id")
        endpoint = session.get("endpoint")
        if isinstance(endpoint, Mapping) and "server" in endpoint:
            session_view["server"] = endpoint["server"]
            result["server"] = endpoint["server"]
        result["session"] = session_view
        result["sessionId"] = session.get("id")
    return result


def common_dir(repo_path: Union[str, os.PathLike[str]]) -> Path:
    """Launcher-facing alias for the shared Git common directory."""

    return git_common_dir(repo_path)


def status(
    repo_path: Union[str, os.PathLike[str]],
) -> Optional[_JsonObject]:
    """Return the current owner, or ``None`` when the admission is free."""

    snapshot = ProjectAdmission(repo_path).Status()
    if snapshot.get("status") in {"free", "released"}:
        return None
    return _launcher_view(snapshot)


def admit(
    repo_path: Union[str, os.PathLike[str]],
    project_id: str,
    contract_revision: str,
) -> _JsonObject:
    """Launcher-facing atomic admission operation."""

    return _launcher_view(
        ProjectAdmission(repo_path).Admit(project_id, contract_revision)
    ) or {}


def bind_session(
    repo_path: Union[str, os.PathLike[str]],
    project_id: str,
    contract_revision: str,
    server: str,
    session_id: str,
) -> _JsonObject:
    """Bind one server/session pair to the admitted project identity."""

    server = _require_text(server, "server")
    # The server is the endpoint identity.  Keeping it in a structured value
    # leaves room for a launcher to add an endpoint generation later while
    # preserving exact equality for retries and mismatch rejection.
    endpoint = {"server": server}
    return _launcher_view(
        ProjectAdmission(repo_path).BindEndpoint(
            project_id,
            session_id,
            endpoint,
            contract_revision,
        )
    ) or {}


def release(
    repo_path: Union[str, os.PathLike[str]],
    project_id: str,
    contract_revision: str,
    terminal_evidence: Any,
    server: Optional[str] = None,
    session_id: Optional[str] = None,
) -> _JsonObject:
    """Launcher-facing explicit release with optional session checks."""

    endpoint = None
    if server is not None:
        endpoint = {"server": _require_text(server, "server")}
    return _launcher_view(
        ProjectAdmission(repo_path).Release(
            project_id,
            terminal_evidence,
            contract_revision,
            session_id,
            endpoint,
    )
    ) or {}


def resume_session(
    repo_path: Union[str, os.PathLike[str]],
    project_id: str,
    contract_revision: str,
    server: str,
    previous_session_id: str,
    new_session_id: str,
    recording_evidence: Any,
) -> _JsonObject:
    """Launcher-facing compare-and-swap session recovery operation."""

    return _launcher_view(
        ProjectAdmission(repo_path).ResumeSession(
            project_id,
            contract_revision,
            server,
            previous_session_id,
            new_session_id,
            recording_evidence,
        )
    ) or {}


ResumeSession = resume_session


def _parse_json_argument(value: str, label: str) -> Any:
    try:
        return json.loads(value)
    except json.JSONDecodeError as exc:
        # A plain endpoint string is a useful and unambiguous CLI identity;
        # terminal evidence must be JSON so callers cannot accidentally pass
        # a path or a human status message as proof.
        if label == "endpoint":
            return _require_text(value, label)
        raise AdmissionValidationError(f"{label} must be valid JSON: {exc.msg}") from exc


def _argument(parser: argparse.ArgumentParser, positional: Optional[str], option: Optional[str], label: str) -> str:
    if positional and option and positional != option:
        parser.error(f"conflicting {label} values")
    value = positional or option
    if value is None:
        parser.error(f"{label} is required")
    return value


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="durable single-project factory admission",
    )
    parser.add_argument("--repo", default=os.getcwd(), help="checkout or worktree used to find Git common dir")
    parser.add_argument("--state-path", help=argparse.SUPPRESS)
    parser.add_argument("--lock-path", help=argparse.SUPPRESS)
    commands = parser.add_subparsers(dest="operation", required=True)

    admit = commands.add_parser("admit", help="admit or replay one project identity")
    admit.add_argument("project_id_pos", nargs="?")
    admit.add_argument("contract_revision_pos", nargs="?")
    admit.add_argument("--project-id")
    admit.add_argument("--contract-revision")
    admit.add_argument("--session-id")
    admit.add_argument("--endpoint")

    bind = commands.add_parser("bind-endpoint", aliases=["bind", "bind-session"], help="bind a session endpoint")
    bind.add_argument("project_id_pos", nargs="?")
    bind.add_argument("contract_or_session_pos", nargs="?")
    bind.add_argument("session_or_endpoint_pos", nargs="?")
    bind.add_argument("endpoint_pos", nargs="?")
    bind.add_argument("--project-id")
    bind.add_argument("--contract-revision")
    bind.add_argument("--session-id")
    bind.add_argument("--endpoint")

    release = commands.add_parser("release", help="release matching ownership with terminal evidence")
    release.add_argument("project_id_pos", nargs="?")
    release.add_argument("terminal_evidence_pos", nargs="?")
    release.add_argument("contract_revision_pos", nargs="?")
    release.add_argument("--project-id")
    release.add_argument("--contract-revision")
    release.add_argument("--terminal-evidence", "--evidence", dest="terminal_evidence")
    release.add_argument("--session-id")
    release.add_argument("--endpoint")

    resume = commands.add_parser(
        "resume-session",
        aliases=["resume"],
        help="compare-and-swap a stopped session to a replacement",
    )
    resume.add_argument("project_id_pos", nargs="?")
    resume.add_argument("contract_revision_pos", nargs="?")
    resume.add_argument("server_pos", nargs="?")
    resume.add_argument("previous_session_id_pos", nargs="?")
    resume.add_argument("new_session_id_pos", nargs="?")
    resume.add_argument("recording_evidence_pos", nargs="?")
    resume.add_argument("--project-id")
    resume.add_argument("--contract-revision")
    resume.add_argument("--server")
    resume.add_argument("--previous-session-id")
    resume.add_argument("--new-session-id")
    resume.add_argument("--recording-evidence", "--evidence", dest="recording_evidence")

    commands.add_parser("status", help="read the current admission snapshot")
    # Accept repository/storage options after the subcommand as well as before
    # it; this is convenient for direct root-launcher invocations.
    for command in (admit, bind, release, resume):
        command.add_argument("--repo", dest="repo", default=argparse.SUPPRESS)
        command.add_argument("--state-path", dest="state_path", help=argparse.SUPPRESS, default=argparse.SUPPRESS)
        command.add_argument("--lock-path", dest="lock_path", help=argparse.SUPPRESS, default=argparse.SUPPRESS)
    status_command = commands.choices["status"]
    status_command.add_argument("--repo", dest="repo", default=argparse.SUPPRESS)
    status_command.add_argument("--state-path", dest="state_path", help=argparse.SUPPRESS, default=argparse.SUPPRESS)
    status_command.add_argument("--lock-path", dest="lock_path", help=argparse.SUPPRESS, default=argparse.SUPPRESS)
    return parser


def _store_from_args(args: argparse.Namespace) -> ProjectAdmission:
    return ProjectAdmission(
        args.repo,
        state_path=args.state_path,
        lock_path=args.lock_path,
    )


def main(argv: Optional[Sequence[str]] = None) -> int:
    parser = _build_parser()
    args = parser.parse_args(argv)
    try:
        store = _store_from_args(args)
        if args.operation == "status":
            result = store.Status()
        elif args.operation == "admit":
            project_id = _argument(parser, args.project_id_pos, args.project_id, "project_id")
            contract_revision = _argument(
                parser,
                args.contract_revision_pos,
                args.contract_revision,
                "contract_revision",
            )
            endpoint = _parse_json_argument(args.endpoint, "endpoint") if args.endpoint is not None else None
            result = store.Admit(project_id, contract_revision, args.session_id, endpoint)
        elif args.operation in {"bind-endpoint", "bind", "bind-session"}:
            project_id = _argument(parser, args.project_id_pos, args.project_id, "project_id")
            contract_revision = args.contract_revision
            session_id = args.session_id
            endpoint_text = args.endpoint
            # The complete positional form is: project revision session endpoint.
            # The short form accepted by bind-endpoint is: project session
            # endpoint; options are preferred for scripts.
            if args.endpoint_pos is not None:
                positional_contract = args.contract_or_session_pos
                positional_session = args.session_or_endpoint_pos
                if contract_revision is None:
                    contract_revision = positional_contract
                if session_id is None:
                    session_id = positional_session
                if endpoint_text is None:
                    endpoint_text = args.endpoint_pos
            else:
                if session_id is None:
                    session_id = args.contract_or_session_pos
                if endpoint_text is None:
                    endpoint_text = args.session_or_endpoint_pos
            if session_id is None:
                parser.error("session_id is required")
            if endpoint_text is None:
                parser.error("endpoint is required")
            endpoint = _parse_json_argument(endpoint_text, "endpoint")
            result = store.BindEndpoint(project_id, session_id, endpoint, contract_revision)
        elif args.operation in {"resume-session", "resume"}:
            project_id = _argument(parser, args.project_id_pos, args.project_id, "project_id")
            contract_revision = _argument(
                parser,
                args.contract_revision_pos,
                args.contract_revision,
                "contract_revision",
            )
            server = _argument(parser, args.server_pos, args.server, "server")
            previous_session_id = _argument(
                parser,
                args.previous_session_id_pos,
                args.previous_session_id,
                "previous_session_id",
            )
            new_session_id = _argument(
                parser,
                args.new_session_id_pos,
                args.new_session_id,
                "new_session_id",
            )
            recording_evidence_text = _argument(
                parser,
                args.recording_evidence_pos,
                args.recording_evidence,
                "recording_evidence",
            )
            result = store.ResumeSession(
                project_id,
                contract_revision,
                server,
                previous_session_id,
                new_session_id,
                _parse_json_argument(recording_evidence_text, "recording evidence"),
            )
        else:
            project_id = _argument(parser, args.project_id_pos, args.project_id, "project_id")
            # Support both release PROJECT EVIDENCE and the complete
            # positional form release PROJECT REVISION EVIDENCE.
            if args.contract_revision_pos is not None:
                if args.contract_revision and args.contract_revision != args.terminal_evidence_pos:
                    parser.error("conflicting contract_revision values")
                contract_revision = args.contract_revision or args.terminal_evidence_pos
                evidence_pos = args.contract_revision_pos
            else:
                contract_revision = args.contract_revision
                evidence_pos = args.terminal_evidence_pos
            evidence_text = _argument(
                parser,
                evidence_pos,
                args.terminal_evidence,
                "terminal_evidence",
            )
            evidence = _parse_json_argument(evidence_text, "terminal evidence")
            result = store.Release(
                project_id,
                evidence,
                contract_revision,
                args.session_id,
                _parse_json_argument(args.endpoint, "endpoint") if args.endpoint is not None else None,
            )
        json.dump(result, sys.stdout, ensure_ascii=False, sort_keys=True)
        sys.stdout.write("\n")
        return 0
    except AdmissionError as exc:
        json.dump(
            {"error": {"code": exc.code, "message": str(exc)}},
            sys.stderr,
            ensure_ascii=False,
            sort_keys=True,
        )
        sys.stderr.write("\n")
        return exc.exit_code
    except (OSError, ValueError, TypeError) as exc:
        json.dump(
            {"error": {"code": "admission_error", "message": str(exc)}},
            sys.stderr,
            ensure_ascii=False,
            sort_keys=True,
        )
        sys.stderr.write("\n")
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
