#!/usr/bin/env python3
"""Validate and publish the one reviewed audio-runtime scope amendment.

The amendment is deliberately narrower than a project waiver.  It records the
operator's already-reviewed exclusion of two physical subproofs while keeping
the original project manifest, rubrics, reports, and Realtime budget as the
authority.  The module has no dependency on the live factory server and is
usable by the controller, preparation, and isolated fixture tests.
"""
from __future__ import annotations

import argparse
import copy
import hashlib
import json
import os
import stat
import sys
import tempfile
from pathlib import Path, PurePosixPath
from typing import Any, Mapping

import project_contract


SCHEMA = "factory.project-scope-amendment.v1"
PROJECT = "audio-runtime"
CONTRACT_REVISION = "audio-runtime-v1"
AMENDMENT_ID = "user-windows-hardware-scope-20260910"

MANIFEST_RELATIVE = "factory/projects/audio-runtime/manifest.json"
AMENDMENTS_RELATIVE = "factory/projects/audio-runtime/amendments"
AUTHORIZATION_RELATIVE = (
    "docs/temp/projects/audio-runtime/"
    "user-scope-service-decomposition-20260910/scope-amendment-authority.json"
)
HISTORICAL_REPORT_RELATIVE = (
    "docs/temp/projects/audio-runtime/"
    "audio-runtime-c32-capture-energy-vertical-probe.json"
)

# These values are the reviewed authorization anchors.  They are intentionally
# independent of the record supplied to ``append``: a forged document cannot
# authorize itself by carrying a matching, self-selected digest.
TRUSTED_MANIFEST_SHA256 = (
    "ec1439b3b1edf5ab935a59cfe67756b67f4a27e51c20ffcaad87ffab35acdf3d"
)
TRUSTED_AUTHORIZATION_SHA256 = (
    "c3bfa91543b349b2c9b89f48c3a95203db72bf59a4fd93b5e4b1851aee772413"
)
TRUSTED_HISTORICAL_REPORT_SHA256 = (
    "0345a628a6038e18bf7c7a59016e7a0002ae89734c6ff59aa541642f34b3d960"
)

EXPECTED_EXCLUDED_SUBPROOF = [
    {
        "criterionId": "DEVICE",
        "subproof": "Native Windows hardware/endpoints testing",
        "verdict": "OUT_OF_SCOPE",
    },
    {
        "criterionId": "PARITY",
        "subproof": "Physical acoustic testing",
        "verdict": "OUT_OF_SCOPE",
    },
]
EXPECTED_RETAINED_PROOF = [
    "Windows software execution and compilation CI",
    "Hermetic capture-energy/codec tests",
    "All nine architectural and software criteria",
    "Original Realtime budget",
    "Fresh independent scoped and final artifact evidence",
]
RETAINED_EVIDENCE_KEYS = {
    "windowsSoftwareExecutionCompilation",
    "hermeticCaptureEnergyCodec",
    "deviceIsolationLifecycle",
    "consumptionVsQueueAdmission",
}

EXPECTED_AUTHORIZATION = {
    "project": PROJECT,
    "contractRevision": CONTRACT_REVISION,
    "amendmentId": AMENDMENT_ID,
    "authority": (
        "Explicit user direction in wake user-scope-service-decomposition-20260910 "
        "and factory/docs/operating-policy.md#user-scope-amendment--2026-09-10"
    ),
    "excluded": [
        "Native Windows hardware/endpoints testing",
        "Physical acoustic testing",
    ],
    "retained": EXPECTED_RETAINED_PROOF,
    "historicalReport": {
        "path": HISTORICAL_REPORT_RELATIVE,
        "sha256": TRUSTED_HISTORICAL_REPORT_SHA256,
        "canonicalState": "failed",
        "decision": "FAILED",
        "deviceVerdict": "BLOCKED",
    },
    "status": (
        "User scope effective; controller amendment support pending C39. "
        "Original report and manifest untouched. Hardware OUT OF SCOPE is never "
        "PASS. No C32 acceptance inferred."
    ),
}

RECORD_KEYS = {
    "schema",
    "project",
    "contractRevision",
    "amendmentId",
    "manifest",
    "authority",
    "authorization",
    "historicalReport",
    "excludedSubproof",
    "retainedProof",
    "criteria",
    "realtimeBudget",
}
REFERENCE_KEYS = {
    "id",
    "path",
    "contentSha256",
    "manifestSha256",
    "authority",
    "authorization",
    "historicalReport",
    "excludedSubproof",
}
MAX_JSON_BYTES = 256 * 1024
MAX_STRING_BYTES = 16 * 1024
MAX_JSON_DEPTH = 16
MAX_OBJECT_ITEMS = 128
MAX_ARRAY_ITEMS = 128


class ScopeAmendmentError(ValueError):
    """A malformed, unauthorized, stale, or unsafe amendment input."""


def _reject_duplicate_keys(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def _reject_constant(value: str) -> Any:
    raise ValueError(f"non-finite JSON number: {value}")


def _check_bounded(value: Any, depth: int = 0) -> None:
    if depth > MAX_JSON_DEPTH:
        raise ScopeAmendmentError("JSON nesting exceeds the amendment limit")
    if isinstance(value, str):
        if len(value.encode("utf-8")) > MAX_STRING_BYTES:
            raise ScopeAmendmentError("JSON string exceeds the amendment limit")
    elif isinstance(value, dict):
        if len(value) > MAX_OBJECT_ITEMS:
            raise ScopeAmendmentError("JSON object exceeds the amendment limit")
        for key, item in value.items():
            if not isinstance(key, str):
                raise ScopeAmendmentError("JSON object key must be a string")
            _check_bounded(item, depth + 1)
    elif isinstance(value, list):
        if len(value) > MAX_ARRAY_ITEMS:
            raise ScopeAmendmentError("JSON array exceeds the amendment limit")
        for item in value:
            _check_bounded(item, depth + 1)


def _load_json_bytes(data: bytes, label: str) -> dict[str, Any]:
    if len(data) > MAX_JSON_BYTES:
        raise ScopeAmendmentError(f"{label} exceeds {MAX_JSON_BYTES} bytes")
    try:
        value = json.loads(
            data.decode("utf-8"),
            object_pairs_hook=_reject_duplicate_keys,
            parse_constant=_reject_constant,
        )
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as error:
        raise ScopeAmendmentError(f"malformed {label}") from error
    _check_bounded(value)
    if not isinstance(value, dict):
        raise ScopeAmendmentError(f"{label} must be a JSON object")
    return value


def canonical_bytes(value: Mapping[str, Any]) -> bytes:
    """Return the byte representation published for an amendment record."""

    _check_bounded(value)
    return (
        json.dumps(
            value,
            ensure_ascii=False,
            sort_keys=True,
            separators=(",", ":"),
            allow_nan=False,
        ).encode("utf-8")
        + b"\n"
    )


def _digest(path: Path) -> str:
    try:
        with path.open("rb") as stream:
            return hashlib.file_digest(stream, "sha256").hexdigest()
    except OSError as error:
        raise ScopeAmendmentError(f"cannot hash {path}") from error


def _relative_path(value: Any, label: str) -> Path:
    if not isinstance(value, str) or not value or "\x00" in value:
        raise ScopeAmendmentError(f"{label} must be a relative path")
    if len(value.encode("utf-8")) > MAX_STRING_BYTES:
        raise ScopeAmendmentError(f"{label} is too long")
    if "\\" in value:
        raise ScopeAmendmentError(f"{label} must use contained slash paths")
    parsed = PurePosixPath(value)
    if parsed.is_absolute() or any(part in {"", ".", ".."} for part in parsed.parts):
        raise ScopeAmendmentError(f"{label} escapes the repository")
    normalized = "/".join(parsed.parts)
    if normalized != value:
        raise ScopeAmendmentError(f"{label} is not normalized")
    return Path(*parsed.parts)


def _safe_file(root: Path, value: Any, label: str) -> Path:
    relative = _relative_path(value, label)
    root = root.resolve()
    if not root.is_dir():
        raise ScopeAmendmentError("repository root is not a directory")
    current = root
    for index, part in enumerate(relative.parts):
        current /= part
        try:
            mode = os.lstat(current).st_mode
        except OSError as error:
            raise ScopeAmendmentError(f"{label} does not exist") from error
        if stat.S_ISLNK(mode):
            raise ScopeAmendmentError(f"{label} may not use symlinks")
        if index < len(relative.parts) - 1 and not stat.S_ISDIR(mode):
            raise ScopeAmendmentError(f"{label} contains a non-directory component")
        if index == len(relative.parts) - 1 and not stat.S_ISREG(mode):
            raise ScopeAmendmentError(f"{label} must be a regular file")
    return current


def _safe_input_file(root: Path, value: Any, label: str) -> Path:
    """Accept a CLI path in either contained absolute or normalized relative form."""

    if isinstance(value, str) and os.path.isabs(value):
        try:
            relative = Path(value).relative_to(root.resolve()).as_posix()
        except ValueError as error:
            raise ScopeAmendmentError(f"{label} is outside the repository") from error
        return _safe_file(root, relative, label)
    return _safe_file(root, value, label)


def _safe_directory(root: Path, relative: str, create: bool = False) -> Path:
    parts = _relative_path(relative, "amendment directory").parts
    current = root.resolve()
    if not current.is_dir():
        raise ScopeAmendmentError("repository root is not a directory")
    for part in parts:
        current /= part
        try:
            mode = os.lstat(current).st_mode
        except FileNotFoundError:
            if not create:
                raise ScopeAmendmentError("amendment directory is missing")
            try:
                current.mkdir(mode=0o700)
                mode = os.lstat(current).st_mode
            except OSError as error:
                raise ScopeAmendmentError("cannot create amendment directory") from error
        except OSError as error:
            raise ScopeAmendmentError("cannot inspect amendment directory") from error
        if stat.S_ISLNK(mode) or not stat.S_ISDIR(mode):
            raise ScopeAmendmentError("amendment directory must contain no symlink")
    return current


def _read_json_file(root: Path, relative: str, label: str) -> tuple[Path, dict[str, Any]]:
    path = _safe_file(root, relative, label)
    try:
        data = path.read_bytes()
    except OSError as error:
        raise ScopeAmendmentError(f"cannot read {label}") from error
    return path, _load_json_bytes(data, label)


def _require_exact_keys(value: Any, keys: set[str], label: str) -> None:
    if not isinstance(value, dict):
        raise ScopeAmendmentError(f"{label} must be an object")
    actual = set(value)
    if actual != keys:
        missing = sorted(keys - actual)
        unknown = sorted(actual - keys)
        detail = []
        if missing:
            detail.append("missing " + ",".join(missing))
        if unknown:
            detail.append("unknown " + ",".join(unknown))
        raise ScopeAmendmentError(f"{label} has invalid keys ({'; '.join(detail)})")


def _require_text(value: Any, label: str) -> str:
    if not isinstance(value, str) or not value or value != value.strip():
        raise ScopeAmendmentError(f"{label} must be a non-empty string")
    if "\x00" in value or len(value.encode("utf-8")) > MAX_STRING_BYTES:
        raise ScopeAmendmentError(f"{label} is invalid or too long")
    return value


def _contract_and_inputs(root: Path) -> tuple[dict[str, Any], dict[str, Any]]:
    root = root.resolve()
    # The amendment is rooted in the admitted checkout.  Do not let the
    # process-wide override select a second manifest when an isolated fixture
    # or an explicit controller root is being validated.
    configured_manifest = os.environ.pop("FACTORY_PROJECT_MANIFEST", None)
    try:
        try:
            contract = project_contract.manifest(root)
        except (ValueError, OSError, RuntimeError) as error:
            raise ScopeAmendmentError(str(error)) from error
    finally:
        if configured_manifest is not None:
            os.environ["FACTORY_PROJECT_MANIFEST"] = configured_manifest
    if contract.get("project") != PROJECT or contract.get("contractRevision") != CONTRACT_REVISION:
        raise ScopeAmendmentError("admitted contract is not audio-runtime-v1")

    manifest_path = _safe_file(root, MANIFEST_RELATIVE, "manifest")
    if _digest(manifest_path) != TRUSTED_MANIFEST_SHA256:
        raise ScopeAmendmentError("original manifest digest is not the reviewed anchor")
    expected_authority: dict[str, dict[str, str]] = {}
    for name, entry in contract.get("authority", {}).items():
        if not isinstance(entry, dict):
            raise ScopeAmendmentError("manifest authority entry is malformed")
        expected_authority[name] = dict(entry)
    if set(expected_authority) != {"sourcePlan", "request", "acceptance"}:
        raise ScopeAmendmentError("manifest authority set changed")
    for name, expected in expected_authority.items():
        path = expected.get("path")
        sha256 = expected.get("sha256")
        if not isinstance(path, str) or not isinstance(sha256, str):
            raise ScopeAmendmentError(f"manifest authority {name} is malformed")
        actual_path = _safe_file(root, path, f"authority {name}")
        if _digest(actual_path) != sha256:
            raise ScopeAmendmentError(f"authority digest mismatch: {name}")
    return contract, expected_authority


def _trusted_authorization(root: Path) -> dict[str, Any]:
    path, anchor = _read_json_file(root, AUTHORIZATION_RELATIVE, "authorization anchor")
    if _digest(path) != TRUSTED_AUTHORIZATION_SHA256:
        raise ScopeAmendmentError("authorization digest is not the reviewed anchor")
    if anchor != EXPECTED_AUTHORIZATION:
        raise ScopeAmendmentError("authorization content is not the reviewed anchor")
    return anchor


def _trusted_historical_report(root: Path) -> dict[str, Any]:
    path, report = _read_json_file(root, HISTORICAL_REPORT_RELATIVE, "historical C32 report")
    if _digest(path) != TRUSTED_HISTORICAL_REPORT_SHA256:
        raise ScopeAmendmentError("historical C32 report digest mismatch")
    if (
        report.get("project") != PROJECT
        or report.get("contractRevision") != CONTRACT_REVISION
        or report.get("scope") != "vertical"
        or report.get("role") != "engineering"
        or report.get("decision") != "FAILED"
    ):
        raise ScopeAmendmentError("historical C32 report is not the preserved FAILED report")
    criteria = report.get("criteria")
    if not isinstance(criteria, dict) or not isinstance(criteria.get("DEVICE"), dict):
        raise ScopeAmendmentError("historical C32 report has no DEVICE result")
    if criteria["DEVICE"].get("verdict") != "BLOCKED":
        raise ScopeAmendmentError("historical C32 DEVICE result is not BLOCKED")
    return report


def _validate_exclusions(value: Any, label: str = "excludedSubproof") -> list[dict[str, str]]:
    if value != EXPECTED_EXCLUDED_SUBPROOF:
        raise ScopeAmendmentError(
            f"{label} may contain only the two reviewed physical subproofs"
        )
    return copy.deepcopy(EXPECTED_EXCLUDED_SUBPROOF)


def _validate_record_object(
    root: Path,
    record: Mapping[str, Any],
    *,
    require_inputs: bool = True,
) -> dict[str, Any]:
    _require_exact_keys(record, RECORD_KEYS, "amendment record")
    if record["schema"] != SCHEMA:
        raise ScopeAmendmentError("unknown amendment schema")
    if record["project"] != PROJECT or record["contractRevision"] != CONTRACT_REVISION:
        raise ScopeAmendmentError("amendment has conflicting project or contract revision")
    if record["amendmentId"] != AMENDMENT_ID:
        raise ScopeAmendmentError("amendment ID is not authorized")

    contract, authority = _contract_and_inputs(root)
    manifest = record["manifest"]
    _require_exact_keys(manifest, {"path", "sha256"}, "amendment manifest")
    if manifest != {"path": MANIFEST_RELATIVE, "sha256": TRUSTED_MANIFEST_SHA256}:
        raise ScopeAmendmentError("amendment does not bind the original manifest")

    if record["authority"] != authority:
        raise ScopeAmendmentError("amendment authority digests do not match the manifest")
    expected_authority = {
        name: {"path": entry["path"], "sha256": entry["sha256"]}
        for name, entry in authority.items()
    }
    if record["authority"] != expected_authority:
        raise ScopeAmendmentError("amendment authority entries are malformed")

    authorization = record["authorization"]
    _require_exact_keys(
        authorization,
        {"path", "sha256", "provenance"},
        "amendment authorization",
    )
    anchor = _trusted_authorization(root) if require_inputs else EXPECTED_AUTHORIZATION
    expected_authorization = {
        "path": AUTHORIZATION_RELATIVE,
        "sha256": TRUSTED_AUTHORIZATION_SHA256,
        "provenance": anchor["authority"],
    }
    if authorization != expected_authorization:
        raise ScopeAmendmentError("amendment authorization provenance is not trusted")

    historical = record["historicalReport"]
    _require_exact_keys(
        historical,
        {"path", "sha256", "canonicalState", "decision", "deviceVerdict"},
        "historical report reference",
    )
    expected_historical = {
        "path": HISTORICAL_REPORT_RELATIVE,
        "sha256": TRUSTED_HISTORICAL_REPORT_SHA256,
        "canonicalState": "failed",
        "decision": "FAILED",
        "deviceVerdict": "BLOCKED",
    }
    if historical != expected_historical:
        raise ScopeAmendmentError("amendment does not preserve the FAILED C32 report")
    if require_inputs:
        _trusted_historical_report(root)

    _validate_exclusions(record["excludedSubproof"])
    if record["retainedProof"] != EXPECTED_RETAINED_PROOF:
        raise ScopeAmendmentError("amendment removed retained software evidence")
    criteria = record["criteria"]
    if not isinstance(criteria, list) or criteria != contract["criteria"]:
        raise ScopeAmendmentError("amendment changed immutable criterion IDs or rubrics")
    budget = record["realtimeBudget"]
    if budget != {"sessionsPerMission": 3, "totalSecondsPerMission": 120}:
        raise ScopeAmendmentError("amendment changes the Realtime budget")
    return copy.deepcopy(dict(record))


def _record_path(root: Path, amendment_id: str = AMENDMENT_ID) -> Path:
    if amendment_id != AMENDMENT_ID:
        raise ScopeAmendmentError("unknown amendment ID")
    directory = _safe_directory(root, AMENDMENTS_RELATIVE, create=False)
    return directory / f"{amendment_id}.json"


def _relative_to_root(root: Path, path: Path) -> str:
    try:
        return path.resolve().relative_to(root.resolve()).as_posix()
    except ValueError as error:
        raise ScopeAmendmentError("path is outside the repository") from error


def _same_artifact(left: Any, right: Any) -> bool:
    """Compare immutable artifact identity while allowing per-mission staging paths."""

    return (
        isinstance(left, Mapping)
        and isinstance(right, Mapping)
        and left.get("identity") == right.get("identity")
        and left.get("sha256") == right.get("sha256")
    )


def _validated_record_path(root: Path, record_path: str) -> tuple[Path, dict[str, Any], bytes]:
    path = _safe_input_file(root, record_path, "amendment record input")
    try:
        data = path.read_bytes()
    except OSError as error:
        raise ScopeAmendmentError("cannot read amendment record input") from error
    record = _load_json_bytes(data, "amendment record")
    validated = _validate_record_object(root, record)
    return path, validated, canonical_bytes(validated)


def create_record(root: Path) -> dict[str, Any]:
    """Construct the only authorized record from reviewed files on disk."""

    contract, authority = _contract_and_inputs(root)
    anchor = _trusted_authorization(root)
    _trusted_historical_report(root)
    record = {
        "schema": SCHEMA,
        "project": PROJECT,
        "contractRevision": CONTRACT_REVISION,
        "amendmentId": AMENDMENT_ID,
        "manifest": {"path": MANIFEST_RELATIVE, "sha256": TRUSTED_MANIFEST_SHA256},
        "authority": authority,
        "authorization": {
            "path": AUTHORIZATION_RELATIVE,
            "sha256": TRUSTED_AUTHORIZATION_SHA256,
            "provenance": anchor["authority"],
        },
        "historicalReport": {
            "path": HISTORICAL_REPORT_RELATIVE,
            "sha256": TRUSTED_HISTORICAL_REPORT_SHA256,
            "canonicalState": "failed",
            "decision": "FAILED",
            "deviceVerdict": "BLOCKED",
        },
        "excludedSubproof": copy.deepcopy(EXPECTED_EXCLUDED_SUBPROOF),
        "retainedProof": copy.deepcopy(EXPECTED_RETAINED_PROOF),
        "criteria": copy.deepcopy(contract["criteria"]),
        "realtimeBudget": {"sessionsPerMission": 3, "totalSecondsPerMission": 120},
    }
    return _validate_record_object(root, record)


def validate_record(root: Path, record_path: str) -> dict[str, Any]:
    """Validate a candidate record and return its exact content hash."""

    path, record, published = _validated_record_path(root, record_path)
    return {
        "record": record,
        "path": _relative_to_root(root, path),
        "contentSha256": hashlib.sha256(published).hexdigest(),
        "publishedBytes": published,
    }


def append_record(root: Path, record_path: str) -> dict[str, Any]:
    """Publish a validated record without ever replacing an existing inode."""

    _source, record, published = _validated_record_path(root, record_path)
    directory = _safe_directory(root, AMENDMENTS_RELATIVE, create=True)
    destination = directory / f"{AMENDMENT_ID}.json"
    content_sha = hashlib.sha256(published).hexdigest()

    try:
        mode = os.lstat(destination).st_mode
    except FileNotFoundError:
        mode = None
    except OSError as error:
        raise ScopeAmendmentError("cannot inspect amendment destination") from error
    if mode is not None:
        if stat.S_ISLNK(mode) or not stat.S_ISREG(mode):
            raise ScopeAmendmentError("amendment destination is not a regular file")
        try:
            existing = destination.read_bytes()
        except OSError as error:
            raise ScopeAmendmentError("cannot read existing amendment") from error
        if existing == published:
            return {
                "status": "already-present",
                "amendmentId": AMENDMENT_ID,
                "path": _relative_to_root(root, destination),
                "contentSha256": content_sha,
            }
        raise ScopeAmendmentError("amendment ID collision would replace history")

    temporary_name: str | None = None
    try:
        fd, temporary_name = tempfile.mkstemp(
            dir=str(directory), prefix=f".{AMENDMENT_ID}.", suffix=".tmp"
        )
        with os.fdopen(fd, "wb") as stream:
            stream.write(published)
            stream.flush()
            os.fsync(stream.fileno())
            os.fchmod(stream.fileno(), 0o400)
        try:
            # A hard link publishes the fully fsynced inode and fails on a
            # collision; os.replace would silently destroy an existing record.
            os.link(temporary_name, destination)
        except FileExistsError as error:
            raise ScopeAmendmentError("amendment ID collision would replace history") from error
        directory_fd = os.open(directory, os.O_RDONLY)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
    except ScopeAmendmentError:
        raise
    except OSError as error:
        raise ScopeAmendmentError("atomic amendment publication failed") from error
    finally:
        if temporary_name is not None:
            try:
                os.unlink(temporary_name)
            except FileNotFoundError:
                pass
            except OSError as error:
                raise ScopeAmendmentError("cannot remove amendment staging file") from error
    return {
        "status": "appended",
        "amendmentId": AMENDMENT_ID,
        "path": _relative_to_root(root, destination),
        "contentSha256": content_sha,
        "record": record,
    }


def _reference_for_record(
    root: Path,
    record_path: Path,
    record: Mapping[str, Any],
    content_sha: str,
) -> dict[str, Any]:
    return {
        "id": record["amendmentId"],
        "path": _relative_to_root(root, record_path),
        "contentSha256": content_sha,
        "manifestSha256": record["manifest"]["sha256"],
        "authority": copy.deepcopy(record["authority"]),
        "authorization": copy.deepcopy(record["authorization"]),
        "historicalReport": copy.deepcopy(record["historicalReport"]),
        "excludedSubproof": copy.deepcopy(record["excludedSubproof"]),
    }


def amendment_reference(root: Path, value: Mapping[str, Any] | None = None) -> dict[str, Any]:
    """Load and verify a stored amendment reference or the canonical record."""

    if value is None:
        path = _record_path(root)
        relative = _relative_to_root(root, path)
    else:
        _require_exact_keys(value, REFERENCE_KEYS, "amendment reference")
        if value["id"] != AMENDMENT_ID or value["path"] != f"{AMENDMENTS_RELATIVE}/{AMENDMENT_ID}.json":
            raise ScopeAmendmentError("amendment reference identifies an unknown path")
        relative = value["path"]
        path = _safe_file(root, relative, "amendment reference")
    validated = validate_record(root, relative)
    record_path = _safe_file(root, relative, "amendment reference")
    expected = _reference_for_record(
        root,
        record_path,
        validated["record"],
        validated["contentSha256"],
    )
    if value is not None and dict(value) != expected:
        raise ScopeAmendmentError("amendment reference content or provenance mismatch")
    return expected


def list_status(root: Path, amendment_id: str | None = None) -> dict[str, Any]:
    """Return validated amendment status without changing the project state."""

    try:
        directory = _safe_directory(root, AMENDMENTS_RELATIVE, create=False)
    except ScopeAmendmentError as error:
        if "missing" in str(error):
            return {"status": "none", "amendments": []}
        raise
    if amendment_id is not None:
        if amendment_id != AMENDMENT_ID:
            raise ScopeAmendmentError("unknown amendment ID")
        relative = f"{AMENDMENTS_RELATIVE}/{amendment_id}.json"
        path = _safe_file(root, relative, "amendment status")
        validated = validate_record(root, relative)
        reference = _reference_for_record(
            root,
            path,
            validated["record"],
            validated["contentSha256"],
        )
        return {"status": "present", "amendment": reference}

    records = []
    for path in sorted(directory.iterdir(), key=lambda candidate: candidate.name):
        if path.suffix != ".json":
            raise ScopeAmendmentError("unexpected file in amendment directory")
        relative = _relative_to_root(root, path)
        validated = validate_record(root, relative)
        records.append(
            _reference_for_record(
                root,
                path,
                validated["record"],
                validated["contentSha256"],
            )
        )
    return {"status": "present" if records else "none", "amendments": records}


def normalize_packet_amendment(root: Path, packet: dict[str, Any]) -> dict[str, Any] | None:
    """Validate an explicit packet amendment and normalize its reference."""

    if "amendment" not in packet:
        return None
    if packet.get("scope") != "project":
        raise ScopeAmendmentError(
            "scope amendments require an explicit project-scope mission"
        )
    source_revision = packet.get("sourceRevision", packet.get("mergedRevision"))
    if not isinstance(source_revision, str) or not source_revision.strip():
        raise ScopeAmendmentError(
            "amended missions require an explicit sourceRevision"
        )
    packet["sourceRevision"] = source_revision
    reference = amendment_reference(root, packet["amendment"])
    packet["amendment"] = reference
    return reference


def _absolute_contained_file(root: Path, value: Any, label: str) -> tuple[Path, str]:
    if not isinstance(value, str) or not os.path.isabs(value):
        raise ScopeAmendmentError(f"{label} must be an absolute repository path")
    try:
        relative = Path(value).relative_to(root.resolve()).as_posix()
    except ValueError as error:
        raise ScopeAmendmentError(f"{label} is outside the repository") from error
    path = _safe_file(root, relative, label)
    return path, relative


def validate_amended_report(
    root: Path,
    report: Mapping[str, Any],
    *,
    role: str,
    build: Mapping[str, Any],
    expected_criteria: set[str],
    amendment: Mapping[str, Any],
    report_path: Path,
) -> None:
    """Enforce fresh mission, artifact, evidence, and amendment provenance."""

    contract, _ = _contract_and_inputs(root)
    rubric_by_id = {
        criterion["id"]: criterion["rubric"] for criterion in contract["criteria"]
    }
    if report.get("scope") != "project":
        raise ScopeAmendmentError("amended completion requires explicit project-scope reports")
    if report.get("role") != role or report.get("amendment") != dict(amendment):
        raise ScopeAmendmentError("validation report amendment or role mismatch")
    if not _same_artifact(report.get("build"), build):
        raise ScopeAmendmentError("validation report does not match the completion artifact")
    validation_work_id = report.get("validationWorkId")
    if not isinstance(validation_work_id, str) or not validation_work_id.strip():
        raise ScopeAmendmentError("amended report requires a canonical validation Work ID")

    source_revision = report.get("sourceRevision")
    if not isinstance(source_revision, str) or not source_revision.strip():
        raise ScopeAmendmentError("amended report requires source/build provenance")
    mission_path, _ = _absolute_contained_file(root, report.get("missionPath"), "missionPath")
    mission_sha = report.get("missionSha256")
    if mission_sha != _digest(mission_path):
        raise ScopeAmendmentError("staged mission digest mismatch")
    try:
        mission = _load_json_bytes(mission_path.read_bytes(), "staged mission")
    except OSError as error:
        raise ScopeAmendmentError("cannot read staged mission") from error
    if (
        mission.get("project") != PROJECT
        or mission.get("contractRevision") != CONTRACT_REVISION
        or mission.get("scope") != "project"
        or mission.get("role") != role
        or mission.get("amendment") != dict(amendment)
        or not _same_artifact(mission.get("build"), report.get("build"))
        or mission.get("sourceRevision") != source_revision
        or mission.get("authority") != amendment["authority"]
        or mission.get("manifestSha256") != amendment["manifestSha256"]
        or mission.get("reportPath") != str(report_path)
    ):
        raise ScopeAmendmentError("report does not match the staged amended mission")
    validation_work_name = report.get("validationWorkName")
    if (
        not isinstance(validation_work_name, str)
        or validation_work_name != mission.get("validationWorkName")
    ):
        raise ScopeAmendmentError("report does not identify its staged validation mission")
    budget = mission.get("budget")
    time_seconds = budget.get("timeSeconds") if isinstance(budget, dict) else None
    if (
        not isinstance(budget, dict)
        or isinstance(time_seconds, bool)
        or not isinstance(time_seconds, int)
        or not 1 <= time_seconds <= 1800
        or budget.get("realtimeSessions") != 0
        or budget.get("realtimeSeconds") != 0
    ):
        raise ScopeAmendmentError("amended mission has an invalid Realtime budget")

    criteria = report.get("criteria")
    if not isinstance(criteria, dict) or set(criteria) != expected_criteria:
        raise ScopeAmendmentError("amended report must cover all immutable criteria")
    for criterion_id, result in criteria.items():
        if (
            not isinstance(result, dict)
            or result.get("verdict") != "PASS"
            or not isinstance(result.get("rubric"), str)
            or result.get("rubric") != rubric_by_id.get(criterion_id)
            or not isinstance(result.get("evidence"), str)
            or not result.get("evidence").strip()
        ):
            raise ScopeAmendmentError("amended report has incomplete criterion evidence")
        if criterion_id in {"DEVICE", "PARITY"} and "out of scope" not in result["evidence"].lower():
            raise ScopeAmendmentError(
                f"{criterion_id} evidence must identify the excluded subproof as OUT OF SCOPE"
            )

    scope_evidence = report.get("scopeEvidence")
    _require_exact_keys(
        scope_evidence,
        {"excludedSubproof", "historicalReport", "retained"},
        "amended scope evidence",
    )
    if scope_evidence["excludedSubproof"] != amendment["excludedSubproof"]:
        raise ScopeAmendmentError("scope evidence changed the authorized exclusions")
    if scope_evidence["historicalReport"] != amendment["historicalReport"]:
        raise ScopeAmendmentError("scope evidence did not preserve the historical report")
    retained = scope_evidence["retained"]
    if not isinstance(retained, dict) or set(retained) != RETAINED_EVIDENCE_KEYS:
        raise ScopeAmendmentError("scope evidence is missing retained software/device proof")
    for evidence in retained.values():
        if (
            not isinstance(evidence, dict)
            or set(evidence) != {"verdict", "evidence"}
            or evidence["verdict"] != "PASS"
            or not isinstance(evidence["evidence"], str)
            or not evidence["evidence"].strip()
        ):
            raise ScopeAmendmentError("retained scope evidence must be explicit PASS evidence")
    if validation_work_id == "batch-audio-runtime-c32-capture-energy-vertical-probe-20260910-audio-runtime-c32-capture-energy-vertical-probe":
        raise ScopeAmendmentError("historical C32 validation Work cannot satisfy amended completion")


def _write_json(path: Path, value: Mapping[str, Any]) -> None:
    path.write_bytes(canonical_bytes(value))


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", help="repository root (defaults to FACTORY_ROOT/Git root)")
    parser.add_argument(
        "operation",
        choices=["create", "validate", "append", "status"],
    )
    parser.add_argument("--record", help="contained candidate record path")
    parser.add_argument("--output", help="contained output path for create")
    parser.add_argument("--amendment-id", default=AMENDMENT_ID)
    args = parser.parse_args()
    root = Path(args.root).resolve() if args.root else project_contract.root_path()
    if args.operation == "create":
        if not args.output:
            raise ScopeAmendmentError("create requires --output")
        record = create_record(root)
        relative = _relative_path(_relative_to_root(root, Path(args.output).resolve()), "output")
        output = root / relative
        if output.exists() or output.is_symlink():
            raise ScopeAmendmentError("output already exists")
        output.parent.mkdir(parents=True, exist_ok=True)
        _write_json(output, record)
        print(json.dumps({"status": "created", "path": _relative_to_root(root, output)}))
    elif args.operation == "validate":
        if not args.record:
            raise ScopeAmendmentError("validate requires --record")
        result = validate_record(root, args.record)
        print(
            json.dumps(
                {
                    "status": "valid",
                    "path": result["path"],
                    "amendmentId": result["record"]["amendmentId"],
                    "contentSha256": result["contentSha256"],
                }
            )
        )
    elif args.operation == "append":
        if not args.record:
            raise ScopeAmendmentError("append requires --record")
        print(json.dumps(append_record(root, args.record)))
    else:
        print(json.dumps(list_status(root, args.amendment_id)))


if __name__ == "__main__":
    try:
        main()
    except (ScopeAmendmentError, ValueError, OSError, RuntimeError) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
