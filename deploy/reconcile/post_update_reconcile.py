#!/usr/bin/env python3

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import math
import os
import stat
import sys
import tempfile
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any, Callable

MAX_RESPONSE_BYTES = 2 * 1024 * 1024
MAX_TOKEN_BYTES = 4096
SAFE_KEY_FIELDS = {
    "api_key_id",
    "api_keys",
    "external_api_key_id",
    "key_hint",
    "selected_api_key_id",
}
SECRET_FIELDS = {
    "api_key",
    "authorization",
    "bearer",
    "bearer_token",
    "ciphertext",
    "database_url",
    "dsn",
    "key",
    "nonce",
    "password",
    "refresh_token",
    "secret",
    "token",
    "access_token",
}


class ReconcileError(Exception):
    def __init__(self, reason: str, message: str):
        super().__init__(message)
        self.reason = reason


class RejectRedirects(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, file_pointer, code, message, headers, new_url):  # type: ignore[no-untyped-def]
        return None


def utc_now() -> str:
    return dt.datetime.now(dt.timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def validate_entry_base(value: str) -> str:
    try:
        parsed = urllib.parse.urlsplit(value)
        port = parsed.port
    except ValueError as error:
        raise ReconcileError("invalid_entry_base", "entry base is not a valid URL") from error
    if (
        parsed.scheme != "http"
        or parsed.hostname not in {"127.0.0.1", "localhost"}
        or parsed.username is not None
        or parsed.password is not None
        or parsed.query
        or parsed.fragment
        or parsed.path not in {"", "/"}
        or port is None
        or port < 1
        or port > 65535
    ):
        raise ReconcileError("invalid_entry_base", "entry base must be a loopback HTTP URL with an explicit port")
    return value.rstrip("/")


def read_bearer(path_value: str) -> str:
    path = Path(path_value)
    if not path.is_absolute():
        raise ReconcileError("invalid_bearer_file", "bearer file path must be absolute")
    flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(path, flags)
    except OSError as error:
        raise ReconcileError("invalid_bearer_file", "bearer file cannot be read") from error
    try:
        metadata = os.fstat(descriptor)
        if not stat.S_ISREG(metadata.st_mode):
            raise ReconcileError("invalid_bearer_file", "bearer file must be a regular file and not a symlink")
        if stat.S_IMODE(metadata.st_mode) != 0o600:
            raise ReconcileError("invalid_bearer_file", "bearer file permissions must be exactly 0600")
        if metadata.st_uid != os.geteuid():
            raise ReconcileError("invalid_bearer_file", "bearer file must be owned by the current user")
        with os.fdopen(descriptor, "rb") as handle:
            descriptor = -1
            raw = handle.read(MAX_TOKEN_BYTES + 3)
    except OSError as error:
        raise ReconcileError("invalid_bearer_file", "bearer file cannot be read") from error
    finally:
        if descriptor >= 0:
            os.close(descriptor)
    if raw.endswith(b"\r\n"):
        raw = raw[:-2]
    elif raw.endswith(b"\n"):
        raw = raw[:-1]
    if not raw or len(raw) > MAX_TOKEN_BYTES or b"\r" in raw or b"\n" in raw:
        raise ReconcileError("invalid_bearer_file", "bearer file must contain exactly one non-empty token")
    try:
        token = raw.decode("ascii")
    except UnicodeDecodeError as error:
        raise ReconcileError("invalid_bearer_file", "bearer token must be ASCII") from error
    if token.strip() != token or any(character.isspace() for character in token):
        raise ReconcileError("invalid_bearer_file", "bearer token must not contain whitespace")
    return token


def field_looks_secret(name: str) -> bool:
    normalized = name.strip().lower().replace("-", "_")
    if normalized in SAFE_KEY_FIELDS:
        return False
    if normalized in SECRET_FIELDS:
        return True
    return normalized.endswith(("_token", "_secret", "_password", "_ciphertext", "_nonce"))


def reject_secret_fields(value: Any) -> None:
    if isinstance(value, dict):
        for key, child in value.items():
            if not isinstance(key, str):
                raise ReconcileError("invalid_contract", "JSON object contains a non-string field name")
            if field_looks_secret(key):
                raise ReconcileError("secret_field_detected", "API response contains a forbidden secret field")
            reject_secret_fields(child)
    elif isinstance(value, list):
        for child in value:
            reject_secret_fields(child)


def fetch_json(entry_base: str, path: str, bearer: str | None) -> dict[str, Any]:
    headers = {"Accept": "application/json"}
    if bearer is not None:
        headers["Authorization"] = f"Bearer {bearer}"
    request = urllib.request.Request(entry_base + path, headers=headers, method="GET")
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), RejectRedirects())
    try:
        with opener.open(request, timeout=10) as response:
            content_type = response.headers.get_content_type()
            if content_type != "application/json" and not content_type.endswith("+json"):
                raise ReconcileError("invalid_content_type", f"{path} did not return JSON")
            body = response.read(MAX_RESPONSE_BYTES + 1)
    except urllib.error.HTTPError as error:
        raise ReconcileError("http_error", f"{path} returned HTTP {error.code}") from error
    except urllib.error.URLError as error:
        raise ReconcileError("request_failed", f"request to {path} failed") from error
    if len(body) > MAX_RESPONSE_BYTES:
        raise ReconcileError("response_too_large", f"{path} response exceeds the size limit")
    if bearer is not None and bearer.encode("ascii") in body:
        raise ReconcileError("secret_value_detected", f"{path} response echoed the bearer token")
    try:
        payload = json.loads(body)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise ReconcileError("invalid_json", f"{path} returned invalid JSON") from error
    if not isinstance(payload, dict):
        raise ReconcileError("invalid_contract", f"{path} response must be a JSON object")
    reject_secret_fields(payload)
    return payload


def envelope_data(payload: dict[str, Any], endpoint: str) -> dict[str, Any]:
    code = payload.get("code")
    if isinstance(code, bool) or code != 0:
        raise ReconcileError("invalid_contract", f"{endpoint} returned a non-success envelope")
    data = payload.get("data")
    if not isinstance(data, dict):
        raise ReconcileError("invalid_contract", f"{endpoint} envelope has no data object")
    return data


def require_integer(value: Any, field: str, *, positive: bool = False) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or (positive and value <= 0):
        raise ReconcileError("invalid_contract", f"{field} must be an integer")
    return value


def require_string(value: Any, field: str, *, allow_empty: bool = False) -> str:
    if not isinstance(value, str) or len(value) > 256 or (not allow_empty and not value.strip()):
        raise ReconcileError("invalid_contract", f"{field} must be a bounded string")
    return value


def require_number(value: Any, field: str) -> int | float:
    if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value):
        raise ReconcileError("invalid_contract", f"{field} must be a finite number")
    return value


def normalize_candidate_items(data: dict[str, Any]) -> list[dict[str, Any]]:
    raw_items = data.get("items")
    if not isinstance(raw_items, list):
        raise ReconcileError("invalid_contract", "credentials/candidates data.items must be a list")
    result: list[dict[str, Any]] = []
    seen: set[int] = set()
    for index, raw in enumerate(raw_items):
        if not isinstance(raw, dict):
            raise ReconcileError("invalid_contract", f"credentials/candidates item {index} must be an object")
        identifier = require_integer(raw.get("id"), f"credentials/candidates item {index}.id", positive=True)
        if identifier in seen:
            raise ReconcileError("duplicate_key_id", f"duplicate official API key ID: {identifier}")
        seen.add(identifier)
        status = require_string(raw.get("status"), f"credentials/candidates item {identifier}.status")
        bound = raw.get("bound")
        if not isinstance(bound, bool):
            raise ReconcileError("invalid_contract", f"credentials/candidates item {identifier}.bound must be boolean")
        group_id = raw.get("group_id")
        if group_id is not None:
            group_id = require_integer(group_id, f"credentials/candidates item {identifier}.group_id", positive=True)
        expires_at = raw.get("expires_at")
        if expires_at is not None:
            expires_at = require_string(expires_at, f"credentials/candidates item {identifier}.expires_at")
        result.append(
            {
                "id": identifier,
                "group_id": group_id,
                "status": status,
                "quota": require_number(raw.get("quota"), f"credentials/candidates item {identifier}.quota"),
                "quota_used": require_number(raw.get("quota_used"), f"credentials/candidates item {identifier}.quota_used"),
                "expires_at": expires_at,
                "bound": bound,
            }
        )
    return sorted(result, key=lambda item: item["id"])


def normalize_missing_bindings(data: dict[str, Any], official_ids: set[int]) -> list[dict[str, Any]]:
    raw_items = data.get("missing_bindings")
    if not isinstance(raw_items, list):
        raise ReconcileError("invalid_contract", "credentials/candidates data.missing_bindings must be a list")
    result: list[dict[str, Any]] = []
    seen: set[int] = set()
    for index, raw in enumerate(raw_items):
        if not isinstance(raw, dict):
            raise ReconcileError("invalid_contract", f"missing binding {index} must be an object")
        identifier = require_integer(raw.get("external_api_key_id"), f"missing binding {index}.external_api_key_id", positive=True)
        if identifier in seen or identifier in official_ids:
            raise ReconcileError("duplicate_key_id", f"inconsistent missing binding API key ID: {identifier}")
        seen.add(identifier)
        result.append(
            {
                "external_api_key_id": identifier,
                "binding_status": require_string(raw.get("binding_status"), f"missing binding {identifier}.binding_status"),
            }
        )
    return sorted(result, key=lambda item: item["external_api_key_id"])


def normalize_config(data: dict[str, Any], candidates: list[dict[str, Any]]) -> dict[str, Any]:
    raw_keys = data.get("api_keys")
    if not isinstance(raw_keys, list):
        raise ReconcileError("invalid_contract", "config data.api_keys must be a list")
    availability: dict[int, bool] = {}
    for index, raw in enumerate(raw_keys):
        if not isinstance(raw, dict):
            raise ReconcileError("invalid_contract", f"config api_keys item {index} must be an object")
        identifier = require_integer(raw.get("id"), f"config api_keys item {index}.id", positive=True)
        if identifier in availability:
            raise ReconcileError("duplicate_key_id", f"duplicate config API key ID: {identifier}")
        available = raw.get("available")
        if not isinstance(available, bool):
            raise ReconcileError("invalid_contract", f"config API key {identifier}.available must be boolean")
        availability[identifier] = available
    candidate_ids = {item["id"] for item in candidates}
    if set(availability) != candidate_ids:
        raise ReconcileError("inconsistent_key_sets", "candidate and config API key ID sets differ")
    for item in candidates:
        expected = item["status"] == "active" and item["bound"]
        if availability[item["id"]] != expected:
            raise ReconcileError("inconsistent_availability", f"API key {item['id']} availability is inconsistent")
    selected = data.get("selected_api_key_id")
    if selected is not None:
        selected = require_integer(selected, "config selected_api_key_id", positive=True)
        if not availability.get(selected, False):
            raise ReconcileError("inconsistent_selection", "selected API key is not usable")
    enabled = data.get("enabled")
    if not isinstance(enabled, bool):
        raise ReconcileError("invalid_contract", "config enabled must be boolean")
    models = data.get("models")
    if not isinstance(models, list):
        raise ReconcileError("invalid_contract", "config models must be a list")
    return {
        "enabled": enabled,
        "policy_version": require_integer(data.get("policy_version"), "config policy_version", positive=True),
        "model_count": len(models),
        "selected_api_key_id": selected,
        "availability": [{"id": identifier, "available": availability[identifier]} for identifier in sorted(availability)],
    }


Fetcher = Callable[[str, str, str | None], dict[str, Any]]


def collect_report(slot: str, entry_base: str, bearer: str, fetcher: Fetcher = fetch_json) -> dict[str, Any]:
    prefix = "/canvas-api" if slot == "stable" else "/canvas-api-next"
    health = fetcher(entry_base, prefix + "/health/ready", None)
    if health.get("status") != "ready":
        raise ReconcileError("canvas_not_ready", "Canvas readiness response is not ready")
    release = require_string(health.get("release"), "Canvas release")

    session = envelope_data(fetcher(entry_base, prefix + "/v1/session", bearer), "session")
    user_id = require_integer(session.get("external_user_id"), "session external_user_id", positive=True)
    role = require_string(session.get("role"), "session role")
    account_status = require_string(session.get("status"), "session status")

    candidates_data = envelope_data(
        fetcher(entry_base, prefix + "/v1/credentials/candidates", bearer),
        "credentials/candidates",
    )
    candidates = normalize_candidate_items(candidates_data)
    official_ids = {item["id"] for item in candidates}
    missing = normalize_missing_bindings(candidates_data, official_ids)
    config = normalize_config(
        envelope_data(fetcher(entry_base, prefix + "/v1/config", bearer), "config"),
        candidates,
    )

    active_unbound = [item["id"] for item in candidates if item["status"] == "active" and not item["bound"]]
    inactive_bound = [item["id"] for item in candidates if item["status"] != "active" and item["bound"]]
    usable = [item["id"] for item in candidates if item["status"] == "active" and item["bound"]]
    metadata = {
        "candidates": candidates,
        "missing_bindings": missing,
        "config": config,
    }
    canonical = json.dumps(metadata, ensure_ascii=True, sort_keys=True, separators=(",", ":")).encode("utf-8")
    attention_required = bool(active_unbound or inactive_bound or missing)
    return {
        "schema_version": 1,
        "status": "attention_required" if attention_required else "ok",
        "generated_at": utc_now(),
        "slot": slot,
        "entry_base": entry_base,
        "canvas_release": release,
        "user": {
            "external_user_id": user_id,
            "role": role,
            "status": account_status,
        },
        "keys": {
            "official_visible_count": len(candidates),
            "official_active_count": sum(item["status"] == "active" for item in candidates),
            "visible_active_binding_count": sum(item["bound"] for item in candidates),
            "usable_count": len(usable),
            "active_but_unbound_ids": active_unbound,
            "inactive_but_bound_ids": inactive_bound,
            "missing_from_official": missing,
            "metadata_sha256": hashlib.sha256(canonical).hexdigest(),
        },
        "config": {
            "enabled": config["enabled"],
            "policy_version": config["policy_version"],
            "model_count": config["model_count"],
            "selected_api_key_id": config["selected_api_key_id"],
        },
        "plaintext_key_accessed": False,
        "mutation_performed": False,
    }


def write_report(path_value: str, payload: dict[str, Any]) -> Path:
    requested = Path(path_value)
    if not requested.is_absolute() or requested.name in {"", ".", ".."}:
        raise ReconcileError("invalid_report_path", "report path must be an absolute file path")
    try:
        parent = requested.parent.resolve(strict=True)
    except OSError as error:
        raise ReconcileError("invalid_report_path", "report parent directory does not exist") from error
    target = parent / requested.name
    if target.exists() or target.is_symlink():
        raise ReconcileError("report_exists", "report path already exists; refusing to overwrite evidence")
    temporary_name = ""
    try:
        descriptor, temporary_name = tempfile.mkstemp(prefix=".reconcile-", suffix=".json", dir=parent)
        with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
            os.fchmod(handle.fileno(), 0o600)
            json.dump(payload, handle, ensure_ascii=True, sort_keys=True, indent=2)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.link(temporary_name, target)
    except FileExistsError as error:
        raise ReconcileError("report_exists", "report path already exists; refusing to overwrite evidence") from error
    except OSError as error:
        raise ReconcileError("report_write_failed", "could not write reconciliation report") from error
    finally:
        if temporary_name:
            try:
                os.unlink(temporary_name)
            except FileNotFoundError:
                pass
    return target


def failed_report(slot: str, entry_base: str, reason: str) -> dict[str, Any]:
    return {
        "schema_version": 1,
        "status": "failed",
        "generated_at": utc_now(),
        "slot": slot,
        "entry_base": entry_base,
        "failure_reason": reason,
        "plaintext_key_accessed": False,
        "mutation_performed": False,
    }


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Read-only post-update Canvas/Sub2API key reconciliation")
    parser.add_argument("--slot", choices=("stable", "candidate"), required=True)
    parser.add_argument("--entry-base", required=True)
    parser.add_argument("--bearer-file", required=True)
    parser.add_argument("--report", required=True)
    return parser.parse_args()


def main() -> int:
    arguments = parse_args()
    entry_base = arguments.entry_base
    report_entry_base = "invalid"
    try:
        entry_base = validate_entry_base(entry_base)
        report_entry_base = entry_base
        bearer = read_bearer(arguments.bearer_file)
        report = collect_report(arguments.slot, entry_base, bearer)
        target = write_report(arguments.report, report)
    except ReconcileError as error:
        try:
            target = write_report(arguments.report, failed_report(arguments.slot, report_entry_base, error.reason))
            print(f"failure report: {target}", file=sys.stderr)
        except ReconcileError as report_error:
            print(f"error: reconciliation failed ({error.reason}); {report_error}", file=sys.stderr)
            return 1
        print(f"error: reconciliation failed ({error.reason}): {error}", file=sys.stderr)
        return 1

    keys = report["keys"]
    print(f"reconciliation report: {target}")
    print(
        "status={status} release={release} visible={visible} usable={usable} "
        "active_unbound={unbound} inactive_bound={inactive} missing={missing}".format(
            status=report["status"],
            release=report["canvas_release"],
            visible=keys["official_visible_count"],
            usable=keys["usable_count"],
            unbound=len(keys["active_but_unbound_ids"]),
            inactive=len(keys["inactive_but_bound_ids"]),
            missing=len(keys["missing_from_official"]),
        )
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
