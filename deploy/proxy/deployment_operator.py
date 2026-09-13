#!/usr/bin/env python3
"""Allowlisted deployment orchestration for the Sub2API path proxy."""

from __future__ import annotations

import datetime as dt
import json
import os
import re
import stat
import subprocess
import threading
import uuid
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable
from urllib.parse import urlsplit

CATALOG_FORMAT = "sub2api-deployment-catalog/v1"
OPERATION_FORMAT = "sub2api-deployment-operation/v1"
COMPONENTS = {"sub2api", "canvas"}
ACTIONS = {"update", "rollback"}
TERMINAL_STATES = {"succeeded", "failed", "interrupted"}
IDENTIFIER_PATTERN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
IMAGE_PATTERN = re.compile(
    r"^[a-z0-9]+(?:[._-][a-z0-9]+)*(?::[0-9]+)?"
    r"(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)+@sha256:[a-f0-9]{64}$"
)
OPERATION_ID_PATTERN = re.compile(r"^deploy-[a-f0-9]{32}$")
MAX_CATALOG_BYTES = 64 * 1024
MAX_OPERATION_BYTES = 256 * 1024


class DeploymentError(Exception):
    def __init__(self, status: int, reason: str, message: str, metadata: dict[str, Any] | None = None) -> None:
        super().__init__(message)
        self.status = status
        self.reason = reason
        self.metadata = metadata or {}


@dataclass(frozen=True)
class Sub2APITarget:
    version: str
    image: str
    release_url: str | None


@dataclass(frozen=True)
class CanvasTarget:
    version: str
    build_id: str
    api_image: str
    web_image: str
    release_url: str | None


@dataclass(frozen=True)
class DeploymentCatalog:
    sub2api: Sub2APITarget | None
    canvas: CanvasTarget | None


Runner = Callable[[list[str], Path, Path, int], int]


def utc_now() -> str:
    return dt.datetime.now(dt.timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def _require_exact_keys(value: dict[str, Any], expected: set[str], field: str) -> None:
    if set(value) != expected:
        raise DeploymentError(503, "invalid_deployment_catalog", f"{field} has invalid fields")


def _require_identifier(value: Any, field: str) -> str:
    if not isinstance(value, str) or not IDENTIFIER_PATTERN.fullmatch(value):
        raise DeploymentError(503, "invalid_deployment_catalog", f"{field} is invalid")
    return value


def _require_image(value: Any, field: str) -> str:
    if not isinstance(value, str) or len(value) > 300 or not IMAGE_PATTERN.fullmatch(value):
        raise DeploymentError(
            503,
            "invalid_deployment_catalog",
            f"{field} must be an immutable repository@sha256 image",
        )
    return value


def _optional_release_url(value: Any, field: str) -> str | None:
    if value is None:
        return None
    if not isinstance(value, str) or len(value) > 2048:
        raise DeploymentError(503, "invalid_deployment_catalog", f"{field} is invalid")
    parsed = urlsplit(value)
    if parsed.scheme != "https" or not parsed.hostname or parsed.username or parsed.password:
        raise DeploymentError(503, "invalid_deployment_catalog", f"{field} must be an HTTPS URL")
    return value


def _read_secure_json(path: Path, maximum: int, reason: str) -> dict[str, Any]:
    flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(path, flags)
    except OSError as error:
        raise DeploymentError(503, reason, f"cannot read {path.name}") from error
    try:
        metadata = os.fstat(descriptor)
        if not stat.S_ISREG(metadata.st_mode) or metadata.st_size > maximum:
            raise DeploymentError(503, reason, f"{path.name} is not a bounded regular file")
        if metadata.st_mode & 0o022:
            raise DeploymentError(503, reason, f"{path.name} must not be group- or world-writable")
        with os.fdopen(descriptor, "rb") as handle:
            descriptor = -1
            raw = handle.read(maximum + 1)
    finally:
        if descriptor >= 0:
            os.close(descriptor)
    if len(raw) > maximum:
        raise DeploymentError(503, reason, f"{path.name} is too large")
    try:
        value = json.loads(raw)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise DeploymentError(503, reason, f"{path.name} is not valid JSON") from error
    if not isinstance(value, dict):
        raise DeploymentError(503, reason, f"{path.name} must contain a JSON object")
    return value


def load_catalog(path: Path) -> DeploymentCatalog:
    payload = _read_secure_json(path, MAX_CATALOG_BYTES, "invalid_deployment_catalog")
    _require_exact_keys(payload, {"format", "sub2api", "canvas"}, "catalog")
    if payload["format"] != CATALOG_FORMAT:
        raise DeploymentError(503, "invalid_deployment_catalog", "catalog format is unsupported")

    raw_sub2api = payload["sub2api"]
    sub2api: Sub2APITarget | None = None
    if raw_sub2api is not None:
        if not isinstance(raw_sub2api, dict):
            raise DeploymentError(503, "invalid_deployment_catalog", "sub2api target must be an object or null")
        _require_exact_keys(raw_sub2api, {"version", "image", "release_url"}, "sub2api target")
        sub2api = Sub2APITarget(
            version=_require_identifier(raw_sub2api["version"], "sub2api.version"),
            image=_require_image(raw_sub2api["image"], "sub2api.image"),
            release_url=_optional_release_url(raw_sub2api["release_url"], "sub2api.release_url"),
        )

    raw_canvas = payload["canvas"]
    canvas: CanvasTarget | None = None
    if raw_canvas is not None:
        if not isinstance(raw_canvas, dict):
            raise DeploymentError(503, "invalid_deployment_catalog", "canvas target must be an object or null")
        _require_exact_keys(
            raw_canvas,
            {"version", "build_id", "api_image", "web_image", "release_url"},
            "canvas target",
        )
        canvas = CanvasTarget(
            version=_require_identifier(raw_canvas["version"], "canvas.version"),
            build_id=_require_identifier(raw_canvas["build_id"], "canvas.build_id"),
            api_image=_require_image(raw_canvas["api_image"], "canvas.api_image"),
            web_image=_require_image(raw_canvas["web_image"], "canvas.web_image"),
            release_url=_optional_release_url(raw_canvas["release_url"], "canvas.release_url"),
        )
    return DeploymentCatalog(sub2api=sub2api, canvas=canvas)


def default_runner(command: list[str], cwd: Path, log_path: Path, timeout_seconds: int) -> int:
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_CLOEXEC", 0)
    descriptor = os.open(log_path, flags, 0o600)
    with os.fdopen(descriptor, "wb") as log:
        try:
            completed = subprocess.run(
                command,
                cwd=cwd,
                stdin=subprocess.DEVNULL,
                stdout=log,
                stderr=subprocess.STDOUT,
                timeout=timeout_seconds,
                check=False,
                close_fds=True,
            )
        except subprocess.TimeoutExpired:
            return 124
    return completed.returncode


class DeploymentOperator:
    def __init__(
        self,
        deploy_dir: str,
        route_file: str,
        catalog_file: str,
        state_dir: str,
        *,
        timeout_seconds: int = 3600,
        runner: Runner = default_runner,
    ) -> None:
        self.deploy_dir = Path(deploy_dir).resolve(strict=True)
        self.repository_root = self.deploy_dir.parent
        self.route_file = Path(route_file).resolve(strict=True)
        self.catalog_file = Path(catalog_file).resolve(strict=True)
        self.state_dir = Path(state_dir).resolve() if Path(state_dir).exists() else Path(state_dir).absolute()
        self.timeout_seconds = timeout_seconds
        self.runner = runner
        self._lock = threading.RLock()
        self._active_operation_id: str | None = None
        self._validate_configuration()
        self._recover_interrupted_operations()

    def _validate_configuration(self) -> None:
        if not self.deploy_dir.is_dir() or self.deploy_dir.is_symlink():
            raise DeploymentError(503, "invalid_deployment_config", "deployment directory is invalid")
        if not self.route_file.is_file() or self.route_file.is_symlink():
            raise DeploymentError(503, "invalid_deployment_config", "route file is invalid")
        for name in (
            "sub2api-release.sh",
            "sub2api-rollback.sh",
            "canvas-release.sh",
            "canvas-rollback.sh",
        ):
            script = self.deploy_dir / name
            if not script.is_file() or script.is_symlink() or not os.access(script, os.X_OK):
                raise DeploymentError(503, "invalid_deployment_config", f"required script is invalid: {name}")
        load_catalog(self.catalog_file)
        if self.state_dir.exists() and (not self.state_dir.is_dir() or self.state_dir.is_symlink()):
            raise DeploymentError(503, "invalid_deployment_config", "operator state directory is invalid")
        self.state_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
        os.chmod(self.state_dir, 0o700)
        for child in ("operations", "logs", "reports", "tokens"):
            directory = self.state_dir / child
            if directory.exists() and (not directory.is_dir() or directory.is_symlink()):
                raise DeploymentError(503, "invalid_deployment_config", f"operator {child} directory is invalid")
            directory.mkdir(mode=0o700, exist_ok=True)
            os.chmod(directory, 0o700)

    def _operation_path(self, operation_id: str) -> Path:
        if not OPERATION_ID_PATTERN.fullmatch(operation_id):
            raise DeploymentError(404, "deployment_operation_not_found", "deployment operation was not found")
        return self.state_dir / "operations" / f"{operation_id}.json"

    def _write_operation(self, operation: dict[str, Any]) -> None:
        path = self._operation_path(str(operation["id"]))
        temporary = path.with_name(f".{path.name}.{uuid.uuid4().hex}.tmp")
        raw = (json.dumps(operation, ensure_ascii=True, separators=(",", ":")) + "\n").encode("ascii")
        descriptor = os.open(
            temporary,
            os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_CLOEXEC", 0),
            0o600,
        )
        try:
            with os.fdopen(descriptor, "wb") as handle:
                descriptor = -1
                handle.write(raw)
                handle.flush()
                os.fsync(handle.fileno())
            os.replace(temporary, path)
        finally:
            if descriptor >= 0:
                os.close(descriptor)
            try:
                temporary.unlink()
            except FileNotFoundError:
                pass

    def _read_operation(self, path: Path) -> dict[str, Any]:
        payload = _read_secure_json(path, MAX_OPERATION_BYTES, "invalid_deployment_state")
        if payload.get("format") != OPERATION_FORMAT or not OPERATION_ID_PATTERN.fullmatch(str(payload.get("id", ""))):
            raise DeploymentError(503, "invalid_deployment_state", "deployment operation state is invalid")
        return payload

    def _recover_interrupted_operations(self) -> None:
        for path in sorted((self.state_dir / "operations").glob("deploy-*.json")):
            operation = self._read_operation(path)
            if operation.get("state") in {"queued", "running"}:
                operation["state"] = "interrupted"
                operation["stage"] = "operator_restarted"
                operation["finished_at"] = utc_now()
                operation["error"] = {
                    "reason": "operator_restarted",
                    "message": "The deployment operator restarted before it recorded a terminal result.",
                }
                self._write_operation(operation)

    def _latest_operation(self) -> dict[str, Any] | None:
        paths = sorted((self.state_dir / "operations").glob("deploy-*.json"), key=lambda item: item.stat().st_mtime_ns)
        return self._public_operation(self._read_operation(paths[-1])) if paths else None

    def _latest_component_operation(self, component: str) -> dict[str, Any] | None:
        paths = sorted(
            (self.state_dir / "operations").glob("deploy-*.json"),
            key=lambda item: item.stat().st_mtime_ns,
            reverse=True,
        )
        for path in paths:
            operation = self._read_operation(path)
            if operation.get("component") == component:
                return self._public_operation(operation)
        return None

    def _load_routes(self) -> dict[str, Any]:
        return _read_secure_json(self.route_file, MAX_CATALOG_BYTES, "invalid_canvas_routes")

    def _canvas_release(self) -> tuple[str | None, bool]:
        routes = self._load_routes()
        stable = routes.get("stable")
        if not isinstance(stable, dict):
            return None, False
        build_id = stable.get("build_id")
        if not isinstance(build_id, str) or not IDENTIFIER_PATTERN.fullmatch(build_id):
            raise DeploymentError(503, "invalid_canvas_routes", "stable Canvas build ID is invalid")
        current_file = self.repository_root / "state" / "stable" / "releases" / "current.env"
        if current_file.is_file() and not current_file.is_symlink():
            for line in current_file.read_text(encoding="utf-8").splitlines():
                if line.startswith("CANVAS_RELEASE="):
                    release = line.partition("=")[2]
                    if IDENTIFIER_PATTERN.fullmatch(release):
                        return release, True
        return build_id, True

    def _canvas_rollback_available(self) -> bool:
        previous = self.repository_root / "state" / "stable" / "releases" / "previous.env"
        return previous.is_file() and not previous.is_symlink()

    def _sub2api_rollback_available(self) -> bool:
        latest = self._latest_component_operation("sub2api")
        return bool(
            latest
            and latest.get("component") == "sub2api"
            and latest.get("action") == "update"
            and latest.get("deployment_succeeded") is True
        )

    def status(self, current_sub2api_version: str) -> dict[str, Any]:
        catalog = load_catalog(self.catalog_file)
        canvas_version, stable_ready = self._canvas_release()
        active = self._active_operation()

        sub2_target = catalog.sub2api
        sub2_reason: str | None = None
        if not stable_ready:
            sub2_reason = "canvas_stable_required"
        elif sub2_target is None:
            sub2_reason = "release_not_approved"
        elif sub2_target.version == current_sub2api_version:
            sub2_reason = "already_up_to_date"
        elif active is not None:
            sub2_reason = "deployment_in_progress"

        canvas_target = catalog.canvas
        canvas_reason: str | None = None
        if not stable_ready:
            canvas_reason = "canvas_stable_required"
        elif canvas_target is None:
            canvas_reason = "release_not_approved"
        elif canvas_target.version == canvas_version:
            canvas_reason = "already_up_to_date"
        elif active is not None:
            canvas_reason = "deployment_in_progress"

        return {
            "components": {
                "sub2api": {
                    "component": "sub2api",
                    "current_version": current_sub2api_version,
                    "target_version": sub2_target.version if sub2_target else None,
                    "release_url": sub2_target.release_url if sub2_target else None,
                    "update_available": sub2_reason is None,
                    "update_enabled": sub2_reason is None,
                    "update_block_reason": sub2_reason,
                    "rollback_enabled": stable_ready and active is None and self._sub2api_rollback_available(),
                    "deployment_mode": "blue_green",
                    "last_operation": self._latest_component_operation("sub2api"),
                },
                "canvas": {
                    "component": "canvas",
                    "current_version": canvas_version,
                    "target_version": canvas_target.version if canvas_target else None,
                    "release_url": canvas_target.release_url if canvas_target else None,
                    "update_available": canvas_reason is None,
                    "update_enabled": canvas_reason is None,
                    "update_block_reason": canvas_reason,
                    "rollback_enabled": stable_ready and active is None and self._canvas_rollback_available(),
                    "deployment_mode": "stable",
                    "last_operation": self._latest_component_operation("canvas"),
                },
            },
            "active_operation": active,
            "latest_operation": self._latest_operation(),
            "reconciliation": {
                "automatic_after_update": True,
                "plaintext_key_accessed": False,
                "mutation_performed": False,
            },
        }

    def _active_operation(self) -> dict[str, Any] | None:
        with self._lock:
            operation_id = self._active_operation_id
        if operation_id is None:
            return None
        try:
            operation = self._read_operation(self._operation_path(operation_id))
        except DeploymentError:
            return None
        return self._public_operation(operation) if operation.get("state") not in TERMINAL_STATES else None

    def get_operation(self, operation_id: str) -> dict[str, Any]:
        path = self._operation_path(operation_id)
        if not path.is_file() or path.is_symlink():
            raise DeploymentError(404, "deployment_operation_not_found", "deployment operation was not found")
        return self._public_operation(self._read_operation(path))

    def start(self, component: str, action: str, bearer: str, current_sub2api_version: str) -> dict[str, Any]:
        if component not in COMPONENTS or action not in ACTIONS:
            raise DeploymentError(404, "deployment_action_not_found", "deployment action was not found")
        if not bearer or len(bearer) > 4096 or any(character.isspace() for character in bearer):
            raise DeploymentError(401, "invalid_admin_bearer", "administrator authentication is required")

        with self._lock:
            if self._active_operation_id is not None:
                active_id = self._active_operation_id
                raise DeploymentError(
                    409,
                    "deployment_in_progress",
                    "another deployment operation is already running",
                    {"operation_id": active_id},
                )
            status = self.status(current_sub2api_version)
            component_status = status["components"][component]
            if action == "update" and not component_status["update_enabled"]:
                reason = component_status["update_block_reason"] or "deployment_not_available"
                raise DeploymentError(409, reason, "this component cannot be updated now")
            if action == "rollback" and not component_status["rollback_enabled"]:
                raise DeploymentError(409, "rollback_not_available", "this component has no recorded rollback target")

            catalog = load_catalog(self.catalog_file)
            release: dict[str, Any] | None = None
            if action == "update" and component == "sub2api":
                if catalog.sub2api is None:
                    raise DeploymentError(409, "release_not_approved", "Sub2API release is not approved")
                release = {
                    "version": catalog.sub2api.version,
                    "image": catalog.sub2api.image,
                }
            elif action == "update":
                if catalog.canvas is None:
                    raise DeploymentError(409, "release_not_approved", "Canvas release is not approved")
                release = {
                    "version": catalog.canvas.version,
                    "build_id": catalog.canvas.build_id,
                    "api_image": catalog.canvas.api_image,
                    "web_image": catalog.canvas.web_image,
                }

            operation_id = "deploy-" + uuid.uuid4().hex
            target_version = release["version"] if release else None
            operation = {
                "format": OPERATION_FORMAT,
                "id": operation_id,
                "component": component,
                "action": action,
                "state": "queued",
                "stage": "queued",
                "requested_at": utc_now(),
                "started_at": None,
                "finished_at": None,
                "from_version": component_status["current_version"],
                "target_version": target_version,
                "release": release,
                "deployment_succeeded": False,
                "reconciliation": {
                    "required": action == "update",
                    "state": "pending" if action == "update" else "not_required",
                },
                "error": None,
            }
            self._write_operation(operation)
            self._active_operation_id = operation_id
            thread = threading.Thread(
                target=self._run_operation,
                args=(operation, bearer),
                name=operation_id,
                daemon=True,
            )
            thread.start()
        return self._public_operation(operation)

    def _build_command(self, operation: dict[str, Any], bearer_file: Path, report_file: Path) -> list[str]:
        component = str(operation["component"])
        action = str(operation["action"])
        release = operation.get("release")
        route = str(self.route_file)
        if component == "sub2api" and action == "update":
            if not isinstance(release, dict):
                raise DeploymentError(503, "invalid_deployment_state", "Sub2API release target is missing")
            return [
                str(self.deploy_dir / "sub2api-release.sh"),
                "--image",
                str(release["image"]),
                "--route-file",
                route,
                "--reconcile-bearer-file",
                str(bearer_file),
                "--reconcile-report",
                str(report_file),
            ]
        if component == "canvas" and action == "update":
            if not isinstance(release, dict):
                raise DeploymentError(503, "invalid_deployment_state", "Canvas release target is missing")
            return [
                str(self.deploy_dir / "canvas-release.sh"),
                "--slot",
                "stable",
                "--web-image",
                str(release["web_image"]),
                "--api-image",
                str(release["api_image"]),
                "--release",
                str(release["version"]),
                "--build-id",
                str(release["build_id"]),
                "--route-file",
                route,
                "--reconcile-bearer-file",
                str(bearer_file),
                "--reconcile-report",
                str(report_file),
            ]
        if component == "sub2api" and action == "rollback":
            return [str(self.deploy_dir / "sub2api-rollback.sh"), "--route-file", route]
        return [
            str(self.deploy_dir / "canvas-rollback.sh"),
            "--slot",
            "stable",
            "--route-file",
            route,
        ]

    def _write_bearer(self, path: Path, bearer: str) -> None:
        descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_CLOEXEC", 0), 0o600)
        with os.fdopen(descriptor, "w", encoding="ascii") as handle:
            handle.write(bearer)
            handle.write("\n")

    def _load_reconciliation(self, path: Path) -> dict[str, Any]:
        payload = _read_secure_json(path, MAX_OPERATION_BYTES, "invalid_reconciliation_report")
        status_value = payload.get("status")
        if status_value not in {"ok", "attention_required", "failed"}:
            status_value = "failed"
        keys = payload.get("keys") if isinstance(payload.get("keys"), dict) else {}
        secret_was_accessed = payload.get("plaintext_key_accessed") is not False
        mutation_was_performed = payload.get("mutation_performed") is not False
        return {
            "required": True,
            "state": "succeeded"
            if status_value in {"ok", "attention_required"} and not secret_was_accessed and not mutation_was_performed
            else "failed",
            "status": status_value,
            "metadata_sha256": keys.get("metadata_sha256") if isinstance(keys.get("metadata_sha256"), str) else None,
            "active_but_unbound_count": len(keys.get("active_but_unbound_ids", []))
            if isinstance(keys.get("active_but_unbound_ids"), list)
            else None,
            "inactive_but_bound_count": len(keys.get("inactive_but_bound_ids", []))
            if isinstance(keys.get("inactive_but_bound_ids"), list)
            else None,
            "missing_from_official_count": len(keys.get("missing_from_official", []))
            if isinstance(keys.get("missing_from_official"), list)
            else None,
            "plaintext_key_accessed": secret_was_accessed,
            "mutation_performed": mutation_was_performed,
        }

    def _run_operation(self, operation: dict[str, Any], bearer: str) -> None:
        operation_id = str(operation["id"])
        bearer_file = self.state_dir / "tokens" / f"{operation_id}.token"
        report_file = self.state_dir / "reports" / f"{operation_id}.json"
        log_file = self.state_dir / "logs" / f"{operation_id}.log"
        try:
            operation["state"] = "running"
            operation["stage"] = "deploying"
            operation["started_at"] = utc_now()
            self._write_operation(operation)
            self._write_bearer(bearer_file, bearer)
            bearer = ""
            command = self._build_command(operation, bearer_file, report_file)
            return_code = self.runner(command, self.repository_root, log_file, self.timeout_seconds)

            if operation["action"] == "update" and report_file.is_file() and not report_file.is_symlink():
                operation["deployment_succeeded"] = True
                operation["stage"] = "reconciling"
                operation["reconciliation"] = self._load_reconciliation(report_file)
            elif operation["action"] == "update":
                operation["reconciliation"] = {"required": True, "state": "not_run"}

            if return_code == 0 and operation["action"] == "update" and not report_file.is_file():
                return_code = 125
                operation["error"] = {
                    "reason": "reconciliation_report_missing",
                    "message": "The release command did not create the required reconciliation report.",
                }

            if (
                return_code == 0
                and operation["action"] == "update"
                and operation["reconciliation"].get("state") != "succeeded"
            ):
                return_code = 125
                operation["error"] = {
                    "reason": "reconciliation_failed",
                    "message": "The release completed, but its required read-only reconciliation failed.",
                }

            if return_code == 0:
                operation["state"] = "succeeded"
                operation["stage"] = "completed"
                operation["deployment_succeeded"] = True
            else:
                operation["state"] = "failed"
                operation["stage"] = "failed"
                if operation["error"] is None:
                    operation["error"] = {
                        "reason": "deployment_command_failed",
                        "message": f"The controlled deployment command exited with status {return_code}.",
                    }
        except Exception as error:  # noqa: BLE001
            operation["state"] = "failed"
            operation["stage"] = "failed"
            operation["error"] = {
                "reason": error.reason if isinstance(error, DeploymentError) else "deployment_operator_failed",
                "message": str(error)[:512],
            }
        finally:
            bearer = ""
            try:
                bearer_file.unlink()
            except FileNotFoundError:
                pass
            operation["finished_at"] = utc_now()
            self._write_operation(operation)
            with self._lock:
                if self._active_operation_id == operation_id:
                    self._active_operation_id = None

    @staticmethod
    def _public_operation(operation: dict[str, Any]) -> dict[str, Any]:
        allowed = {
            "id",
            "component",
            "action",
            "state",
            "stage",
            "requested_at",
            "started_at",
            "finished_at",
            "from_version",
            "target_version",
            "deployment_succeeded",
            "reconciliation",
            "error",
        }
        return {key: operation.get(key) for key in allowed}
