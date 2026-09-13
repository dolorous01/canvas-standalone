#!/usr/bin/env python3
"""Bounded same-origin path proxy for official Sub2API and Canvas."""

from __future__ import annotations

import argparse
import dataclasses
import http.client
import json
import logging
import os
import shutil
import signal
import socket
import subprocess
import tempfile
import threading
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import BinaryIO

from deployment_operator import DeploymentError, DeploymentOperator

HOP_BY_HOP = {
    "connection",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailer",
    "transfer-encoding",
    "upgrade",
}
BLOCKED_LIFECYCLE_PATHS = {
    "/api/v1/admin/system/update",
    "/api/v1/admin/system/rollback",
    "/api/v1/admin/system/restart",
}
IMPORT_GUARDED_PATHS = {"/api/v1/admin/accounts/data", "/admin/accounts/data"}
AUTO_CLEAN_SERVICE = "sub2api-auto-clean.service"
IMMUTABLE_REASON = "immutable_deployment_required"
IMMUTABLE_MESSAGE = "Production lifecycle actions are available only through the operator deployment workflow."
DEFAULT_BODY_LIMIT = 512 << 20
CANVAS_BODY_LIMIT = 70 << 20
DEPLOYMENT_BODY_LIMIT = 1024
DEPLOYMENT_PREFIX = "/api/v1/admin/deployments"
ADMIN_AUTH_RESPONSE_LIMIT = 64 * 1024
CANVAS_SOURCE_WRITE_METHODS = {"POST", "PUT", "PATCH", "DELETE"}
CANVAS_SOURCE_WRITE_PREFIXES = {
    "/api/v1/image-canvas",
    "/api/v1/admin/image-canvas",
}
CANVAS_SOURCE_MAINTENANCE_REASON = "canvas_source_maintenance"
CANVAS_SOURCE_MAINTENANCE_MESSAGE = "The legacy Canvas is frozen for the standalone cutover."


class RequestBodyError(Exception):
    def __init__(self, status: int, message: str) -> None:
        super().__init__(message)
        self.status = status


@dataclasses.dataclass(frozen=True)
class Target:
    host: str
    port: int


@dataclasses.dataclass(frozen=True)
class CanvasSlot:
    web: Target
    api: Target
    build_id: str


@dataclasses.dataclass(frozen=True)
class Route:
    target: Target
    upstream_path: str
    body_limit: int = DEFAULT_BODY_LIMIT


def normalized_path(request_path: str) -> str | None:
    raw_path = urllib.parse.urlparse(request_path).path
    try:
        return urllib.parse.unquote(raw_path, errors="strict")
    except UnicodeDecodeError:
        return None


def segment_match(path: str, prefix: str) -> bool:
    return path == prefix or path.startswith(prefix + "/")


def is_blocked_lifecycle_request(method: str, request_path: str) -> bool:
    path = normalized_path(request_path)
    if path is None:
        return False
    normalized = path.rstrip("/") or "/"
    return method.upper() == "POST" and normalized in BLOCKED_LIFECYCLE_PATHS


def is_import_request(method: str, request_path: str) -> bool:
    path = normalized_path(request_path)
    return method.upper() == "POST" and path is not None and path.rstrip("/") in IMPORT_GUARDED_PATHS


def is_canvas_source_write_request(method: str, request_path: str) -> bool:
    path = normalized_path(request_path)
    if method.upper() not in CANVAS_SOURCE_WRITE_METHODS or path is None:
        return False
    normalized = path.rstrip("/") or "/"
    return any(segment_match(normalized, prefix) for prefix in CANVAS_SOURCE_WRITE_PREFIXES)


def rewrite_prefix(request_path: str, source: str, target: str) -> str:
    parsed = urllib.parse.urlsplit(request_path)
    if not segment_match(parsed.path, source):
        return request_path
    path = target + parsed.path[len(source) :]
    return urllib.parse.urlunsplit(("", "", path, parsed.query, parsed.fragment))


def parse_target(value: str, *, require_loopback: bool = False) -> Target:
    parsed = urllib.parse.urlparse(value)
    if parsed.scheme not in {"http", ""} or not parsed.hostname or parsed.username or parsed.password:
        raise ValueError(f"invalid HTTP target: {value}")
    if parsed.path not in {"", "/"} or parsed.query or parsed.fragment:
        raise ValueError(f"target must not include a path, query, or fragment: {value}")
    if require_loopback and parsed.hostname not in {"127.0.0.1", "localhost", "::1"}:
        raise ValueError(f"Canvas target must use loopback: {value}")
    return Target(parsed.hostname, parsed.port or 80)


class CanvasRouteFile:
    def __init__(self, path: str | None) -> None:
        self.path = Path(path).resolve() if path else None
        self._lock = threading.Lock()
        self._file_identity: tuple[int, int, int, int] | None = None
        self._slots: dict[str, CanvasSlot] = {}

    def snapshot(self) -> dict[str, CanvasSlot]:
        if self.path is None or not self.path.exists():
            return {}
        stat = self.path.stat()
        identity = (stat.st_ino, stat.st_size, stat.st_mtime_ns, stat.st_ctime_ns)
        with self._lock:
            if self._file_identity == identity:
                return dict(self._slots)
            payload = json.loads(self.path.read_text(encoding="utf-8"))
            if not isinstance(payload, dict) or set(payload) - {"stable", "candidate"}:
                raise ValueError("Canvas route file has unknown top-level fields")
            slots: dict[str, CanvasSlot] = {}
            for name, value in payload.items():
                if not isinstance(value, dict) or set(value) != {"web", "api", "build_id"}:
                    raise ValueError(f"Canvas route slot is invalid: {name}")
                build_id = value["build_id"]
                if not isinstance(build_id, str) or not build_id or len(build_id) > 64 or any(char not in "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-" for char in build_id):
                    raise ValueError(f"Canvas build ID is invalid: {name}")
                slots[name] = CanvasSlot(
                    web=parse_target(str(value["web"]), require_loopback=True),
                    api=parse_target(str(value["api"]), require_loopback=True),
                    build_id=build_id,
                )
            self._slots = slots
            self._file_identity = identity
            return dict(slots)


class ProxyConfig:
    def __init__(
        self,
        sub2api: str,
        auto_clean: str,
        canvas_routes_file: str | None = None,
        deployments: DeploymentOperator | None = None,
        canvas_source_maintenance_file: str | None = None,
    ) -> None:
        self.sub2api = parse_target(sub2api)
        self.auto_clean = parse_target(auto_clean)
        self.canvas_routes = CanvasRouteFile(canvas_routes_file)
        self.deployments = deployments
        self.canvas_source_maintenance_file = (
            Path(canvas_source_maintenance_file).absolute() if canvas_source_maintenance_file else None
        )

    def source_writes_frozen(self) -> bool:
        marker = self.canvas_source_maintenance_file
        if marker is None:
            return False
        try:
            return os.path.lexists(marker)
        except OSError:
            logging.exception("Legacy Canvas maintenance marker could not be inspected")
            return True

    def route(self, request_path: str) -> Route:
        parsed_path = urllib.parse.urlsplit(request_path).path
        if segment_match(parsed_path, "/auto-clean"):
            return Route(self.auto_clean, request_path)

        slots = self.canvas_routes.snapshot()
        candidate = slots.get("candidate")
        stable = slots.get("stable")
        if candidate and segment_match(parsed_path, "/canvas-api-next"):
            return Route(candidate.api, rewrite_prefix(request_path, "/canvas-api-next", "/canvas-api"), CANVAS_BODY_LIMIT)
        if candidate and segment_match(parsed_path, "/studio-next"):
            return Route(candidate.web, request_path, CANVAS_BODY_LIMIT)
        if candidate and segment_match(parsed_path, "/canvas-static-next"):
            return Route(candidate.web, rewrite_prefix(request_path, "/canvas-static-next", "/canvas-static"), CANVAS_BODY_LIMIT)
        if candidate and segment_match(parsed_path, f"/canvas-static/{candidate.build_id}"):
            return Route(candidate.web, request_path, CANVAS_BODY_LIMIT)
        if stable and segment_match(parsed_path, "/canvas-api"):
            return Route(stable.api, request_path, CANVAS_BODY_LIMIT)
        if stable and (segment_match(parsed_path, "/studio") or segment_match(parsed_path, "/canvas-static")):
            return Route(stable.web, request_path, CANVAS_BODY_LIMIT)
        return Route(self.sub2api, request_path)


def systemctl_user(action: str, service: str = AUTO_CLEAN_SERVICE) -> dict[str, object]:
    systemctl = shutil.which("systemctl") or "/usr/bin/systemctl"
    try:
        completed = subprocess.run(
            [systemctl, "--user", action, service], capture_output=True, text=True, timeout=30, check=False
        )
    except Exception as exc:  # noqa: BLE001
        return {"action": action, "ok": False, "error": str(exc)}
    return {"action": action, "ok": completed.returncode == 0, "returncode": completed.returncode}


class ProxyHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    config: ProxyConfig

    def do_GET(self) -> None:  # noqa: N802
        self.proxy_request()

    def do_HEAD(self) -> None:  # noqa: N802
        self.proxy_request()

    def do_POST(self) -> None:  # noqa: N802
        self.proxy_request()

    def do_PUT(self) -> None:  # noqa: N802
        self.proxy_request()

    def do_PATCH(self) -> None:  # noqa: N802
        self.proxy_request()

    def do_DELETE(self) -> None:  # noqa: N802
        self.proxy_request()

    def do_OPTIONS(self) -> None:  # noqa: N802
        self.proxy_request()

    def send_json_error(self, status: int, reason: str, message: str) -> None:
        self.send_api_json(status, {"code": status, "reason": reason, "message": message})

    def send_api_json(self, status: int, value: dict[str, object]) -> None:
        payload = json.dumps(value, separators=(",", ":")).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(payload)))
        self.send_header("Cache-Control", "no-store")
        self.send_header("X-Content-Type-Options", "nosniff")
        self.send_header("Connection", "close")
        self.end_headers()
        self.close_connection = True
        if self.command != "HEAD":
            self.wfile.write(payload)

    def send_api_success(self, value: dict[str, object], status: int = 200) -> None:
        self.send_api_json(status, {"code": 0, "message": "success", "data": value})

    def authenticate_deployment_admin(self) -> tuple[str, str]:
        authorization = self.headers.get("Authorization", "")
        scheme, separator, token = authorization.partition(" ")
        if (
            not separator
            or scheme.lower() != "bearer"
            or not token
            or len(token) > 4096
            or any(character.isspace() for character in token)
        ):
            raise DeploymentError(401, "invalid_admin_bearer", "Administrator authentication is required.")

        target = self.config.sub2api
        connection = http.client.HTTPConnection(target.host, target.port, timeout=10)
        try:
            connection.request(
                "GET",
                "/api/v1/admin/system/version",
                headers={
                    "Accept": "application/json",
                    "Accept-Encoding": "identity",
                    "Authorization": authorization,
                    "Host": f"{target.host}:{target.port}",
                },
            )
            upstream = connection.getresponse()
            body = upstream.read(ADMIN_AUTH_RESPONSE_LIMIT + 1)
        except (ConnectionError, http.client.HTTPException, socket.timeout) as error:
            raise DeploymentError(
                503,
                "admin_auth_unavailable",
                "The administrator authentication service is temporarily unavailable.",
            ) from error
        finally:
            connection.close()

        if upstream.status in {401, 403}:
            raise DeploymentError(upstream.status, "admin_access_required", "Administrator access is required.")
        if upstream.status != 200 or len(body) > ADMIN_AUTH_RESPONSE_LIMIT:
            raise DeploymentError(503, "admin_auth_unavailable", "Administrator authentication could not be verified.")
        try:
            payload = json.loads(body)
            data = payload.get("data") if isinstance(payload, dict) and payload.get("code") == 0 else None
            version = data.get("version") if isinstance(data, dict) else None
        except (UnicodeDecodeError, json.JSONDecodeError) as error:
            raise DeploymentError(503, "admin_auth_unavailable", "Administrator authentication returned invalid data.") from error
        if not isinstance(version, str) or not version or len(version) > 128:
            raise DeploymentError(503, "admin_auth_unavailable", "The current Sub2API version is unavailable.")
        return token, version

    def validate_deployment_origin(self) -> None:
        origin = self.headers.get("Origin")
        if not origin:
            return
        parsed = urllib.parse.urlsplit(origin)
        request_host = self.headers.get("Host", "").lower()
        if parsed.scheme not in {"http", "https"} or parsed.netloc.lower() != request_host:
            raise DeploymentError(403, "deployment_origin_rejected", "The deployment request origin is not allowed.")

    def read_deployment_json(self) -> None:
        body, _length = self.read_body(DEPLOYMENT_BODY_LIMIT)
        if body is None:
            return
        try:
            raw = body.read(DEPLOYMENT_BODY_LIMIT + 1)
        finally:
            body.close()
        if not raw:
            return
        content_type = self.headers.get("Content-Type", "").partition(";")[0].strip().lower()
        if content_type != "application/json":
            raise DeploymentError(415, "invalid_deployment_request", "Deployment requests must use JSON.")
        try:
            payload = json.loads(raw)
        except (UnicodeDecodeError, json.JSONDecodeError) as error:
            raise DeploymentError(400, "invalid_deployment_request", "Deployment request JSON is invalid.") from error
        if not isinstance(payload, dict) or payload:
            raise DeploymentError(400, "invalid_deployment_request", "Deployment request must be an empty JSON object.")

    def handle_deployment_request(self) -> None:
        manager = self.config.deployments
        if manager is None:
            self.send_json_error(503, "deployment_operator_disabled", "The deployment operator is not configured.")
            return
        path = normalized_path(self.path)
        if path is None:
            self.send_json_error(400, "invalid_deployment_path", "The deployment path is invalid.")
            return
        normalized = path.rstrip("/") or "/"
        try:
            bearer, current_version = self.authenticate_deployment_admin()
            if normalized == DEPLOYMENT_PREFIX:
                if self.command != "GET":
                    raise DeploymentError(405, "deployment_method_not_allowed", "This deployment endpoint only accepts GET.")
                self.send_api_success(manager.status(current_version))
                return

            operation_prefix = DEPLOYMENT_PREFIX + "/operations/"
            if normalized.startswith(operation_prefix):
                if self.command != "GET":
                    raise DeploymentError(405, "deployment_method_not_allowed", "This deployment endpoint only accepts GET.")
                operation_id = normalized[len(operation_prefix) :]
                if not operation_id or "/" in operation_id:
                    raise DeploymentError(404, "deployment_operation_not_found", "Deployment operation was not found.")
                self.send_api_success(manager.get_operation(operation_id))
                return

            relative = normalized.removeprefix(DEPLOYMENT_PREFIX + "/")
            parts = relative.split("/")
            if len(parts) != 2 or parts[0] not in {"sub2api", "canvas"} or parts[1] not in {"update", "rollback"}:
                raise DeploymentError(404, "deployment_action_not_found", "Deployment action was not found.")
            if self.command != "POST":
                raise DeploymentError(405, "deployment_method_not_allowed", "This deployment endpoint only accepts POST.")
            component, action = parts
            self.validate_deployment_origin()
            if self.headers.get("X-Deployment-Confirmation") != f"{component}:{action}":
                raise DeploymentError(
                    428,
                    "deployment_confirmation_required",
                    "The exact deployment confirmation header is required.",
                )
            self.read_deployment_json()
            operation = manager.start(component, action, bearer, current_version)
            self.send_api_success(operation, 202)
        except RequestBodyError as error:
            self.send_json_error(error.status, "invalid_deployment_request", str(error))
        except DeploymentError as error:
            payload: dict[str, object] = {
                "code": error.status,
                "reason": error.reason,
                "message": str(error),
            }
            if error.metadata:
                payload["metadata"] = error.metadata
            self.send_api_json(error.status, payload)

    def proxy_request(self) -> None:
        path = normalized_path(self.path)
        if path is not None and segment_match(path.rstrip("/") or "/", DEPLOYMENT_PREFIX):
            self.handle_deployment_request()
            return
        if is_blocked_lifecycle_request(self.command, self.path):
            self.send_json_error(403, IMMUTABLE_REASON, IMMUTABLE_MESSAGE)
            return
        if self.config.source_writes_frozen() and is_canvas_source_write_request(self.command, self.path):
            self.send_json_error(
                503,
                CANVAS_SOURCE_MAINTENANCE_REASON,
                CANVAS_SOURCE_MAINTENANCE_MESSAGE,
            )
            return
        try:
            route = self.config.route(self.path)
        except (OSError, ValueError, json.JSONDecodeError):
            logging.exception("Canvas route configuration is invalid")
            self.send_json_error(503, "canvas_routes_invalid", "Canvas routing is temporarily unavailable.")
            return

        conn: http.client.HTTPConnection | None = None
        body: BinaryIO | None = None
        import_guarded = is_import_request(self.command, self.path)
        if import_guarded:
            logging.info("pausing %s before import request %s", AUTO_CLEAN_SERVICE, self.path)
            logging.info("auto-clean stop result: %s", systemctl_user("stop"))
        try:
            body, body_length = self.read_body(route.body_limit)
            headers = self.forward_headers(route.target, body_length)
            conn = http.client.HTTPConnection(route.target.host, route.target.port, timeout=300)
            conn.request(self.command, route.upstream_path, body=body, headers=headers)
            response = conn.getresponse()
            self.send_response(response.status, response.reason)
            content_length = response.getheader("Content-Length")
            content_type = response.getheader("Content-Type", "")
            stream_response = content_length is None or "text/event-stream" in content_type or self.path.startswith("/v1/")
            for key, value in response.getheaders():
                lower = key.lower()
                if lower in HOP_BY_HOP or (stream_response and lower == "content-length"):
                    continue
                if lower == "content-security-policy":
                    value = allow_self_frames(value)
                self.send_header(key, value)
            if stream_response:
                self.send_header("Transfer-Encoding", "chunked")
            self.end_headers()
            if self.command == "HEAD":
                return
            while chunk := response.read(65536):
                if stream_response:
                    self.wfile.write(f"{len(chunk):X}\r\n".encode("ascii"))
                    self.wfile.write(chunk)
                    self.wfile.write(b"\r\n")
                else:
                    self.wfile.write(chunk)
                self.wfile.flush()
            if stream_response:
                self.wfile.write(b"0\r\n\r\n")
                self.wfile.flush()
        except RequestBodyError as exc:
            self.send_json_error(exc.status, "request_body_rejected", str(exc))
        except (ConnectionError, http.client.HTTPException, socket.timeout) as exc:
            logging.exception("proxy error")
            if not self.wfile.closed:
                self.send_json_error(502, "upstream_unavailable", f"Upstream proxy error: {type(exc).__name__}")
        finally:
            if body is not None:
                body.close()
            if conn is not None:
                conn.close()
            if import_guarded:
                logging.info("starting %s after import request %s", AUTO_CLEAN_SERVICE, self.path)
                logging.info("auto-clean start result: %s", systemctl_user("start"))

    def read_body(self, limit: int) -> tuple[BinaryIO | None, int | None]:
        transfer_encoding = ",".join(self.headers.get_all("Transfer-Encoding", []))
        if any(part.strip().lower() == "chunked" for part in transfer_encoding.split(",")):
            return self.read_chunked_body(limit)
        length_header = self.headers.get("Content-Length")
        if not length_header:
            return None, None
        try:
            length = int(length_header)
        except ValueError as exc:
            raise RequestBodyError(400, "Invalid Content-Length") from exc
        if length < 0:
            raise RequestBodyError(400, "Invalid Content-Length")
        if length > limit:
            raise RequestBodyError(413, "Request body is too large")
        body = tempfile.SpooledTemporaryFile(max_size=1 << 20)
        remaining = length
        while remaining:
            chunk = self.rfile.read(min(65536, remaining))
            if not chunk:
                body.close()
                raise RequestBodyError(400, "Unexpected EOF in request body")
            body.write(chunk)
            remaining -= len(chunk)
        body.seek(0)
        return body, length

    def read_chunked_body(self, limit: int) -> tuple[BinaryIO, int]:
        body = tempfile.SpooledTemporaryFile(max_size=1 << 20)
        total = 0
        try:
            while True:
                line = self.rfile.readline(65537)
                if not line or len(line) > 65536:
                    raise RequestBodyError(400, "Invalid chunk header")
                try:
                    size = int(line.split(b";", 1)[0].strip(), 16)
                except ValueError as exc:
                    raise RequestBodyError(400, "Invalid chunk size") from exc
                if size < 0 or total + size > limit:
                    raise RequestBodyError(413, "Request body is too large")
                if size == 0:
                    self.consume_chunked_trailers()
                    break
                chunk = self.rfile.read(size)
                if len(chunk) != size or self.rfile.read(2) != b"\r\n":
                    raise RequestBodyError(400, "Invalid chunk body")
                body.write(chunk)
                total += size
            body.seek(0)
            return body, total
        except Exception:
            body.close()
            raise

    def consume_chunked_trailers(self) -> None:
        while True:
            line = self.rfile.readline(65537)
            if len(line) > 65536:
                raise RequestBodyError(400, "Chunk trailer is too large")
            if line in (b"\r\n", b"\n", b""):
                return

    def forward_headers(self, target: Target, body_length: int | None) -> dict[str, str]:
        headers: dict[str, str] = {}
        connection_tokens: set[str] = set()
        for value in self.headers.get_all("Connection", []):
            connection_tokens.update(part.strip().lower() for part in value.split(","))
        for key, value in self.headers.items():
            lower = key.lower()
            if lower in HOP_BY_HOP or lower in connection_tokens or lower == "content-length":
                continue
            headers[key] = value
        if body_length is not None:
            headers["Content-Length"] = str(body_length)
        host = self.headers.get("Host", "")
        headers["Host"] = host or f"{target.host}:{target.port}"
        headers["X-Forwarded-Host"] = host
        headers["X-Forwarded-For"] = append_forwarded_for(self.headers.get("X-Forwarded-For"), self.client_address[0])
        headers["X-Forwarded-Proto"] = self.headers.get("X-Forwarded-Proto", "https" if self.headers.get("CF-Visitor") else "http")
        return headers

    def log_message(self, fmt: str, *args: object) -> None:
        logging.info("%s - %s", self.client_address[0], fmt % args)


def append_forwarded_for(existing: str | None, ip: str) -> str:
    return f"{existing}, {ip}" if existing else ip


def allow_self_frames(policy: str) -> str:
    directives = [part.strip() for part in policy.split(";") if part.strip()]
    for index, directive in enumerate(directives):
        tokens = directive.split()
        if tokens and tokens[0].lower() == "frame-src":
            if "'self'" not in tokens:
                tokens.insert(1, "'self'")
                directives[index] = " ".join(tokens)
            return "; ".join(directives)
    directives.append("frame-src 'self'")
    return "; ".join(directives)


def make_handler(config: ProxyConfig) -> type[ProxyHandler]:
    class Handler(ProxyHandler):
        pass

    Handler.config = config
    return Handler


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Run the Sub2API and Canvas path proxy.")
    parser.add_argument("--host", default="0.0.0.0")
    parser.add_argument("--port", type=int, default=8080)
    parser.add_argument("--sub2api", default="http://127.0.0.1:18080")
    parser.add_argument("--auto-clean", default="http://127.0.0.1:8093")
    parser.add_argument("--canvas-routes-file")
    parser.add_argument("--canvas-source-maintenance-file")
    parser.add_argument("--deployment-catalog")
    parser.add_argument("--deployment-state-dir")
    parser.add_argument("--deployment-dir")
    parser.add_argument("--deployment-timeout-seconds", type=int, default=3600)
    parser.add_argument("--log-file", default="/home/ubuntu/ResearchWang13/data/logs/path_proxy.log")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    Path(args.log_file).parent.mkdir(parents=True, exist_ok=True)
    logging.basicConfig(filename=args.log_file, level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
    deployment_arguments = (args.deployment_catalog, args.deployment_state_dir, args.deployment_dir)
    if any(deployment_arguments) and not all(deployment_arguments):
        raise SystemExit("--deployment-catalog, --deployment-state-dir, and --deployment-dir must be set together")
    deployments = None
    if all(deployment_arguments):
        if not args.canvas_routes_file:
            raise SystemExit("--canvas-routes-file is required when the deployment operator is enabled")
        if args.deployment_timeout_seconds < 60 or args.deployment_timeout_seconds > 7200:
            raise SystemExit("--deployment-timeout-seconds must be between 60 and 7200")
        deployments = DeploymentOperator(
            args.deployment_dir,
            args.canvas_routes_file,
            args.deployment_catalog,
            args.deployment_state_dir,
            timeout_seconds=args.deployment_timeout_seconds,
        )
    if args.canvas_source_maintenance_file:
        marker = Path(args.canvas_source_maintenance_file)
        if not marker.is_absolute() or marker == Path("/"):
            raise SystemExit("--canvas-source-maintenance-file must be an absolute non-root path")
        if not marker.parent.is_dir():
            raise SystemExit("--canvas-source-maintenance-file parent directory does not exist")
        if os.path.lexists(marker) and (marker.is_symlink() or not marker.is_file()):
            raise SystemExit("--canvas-source-maintenance-file must be a regular file when present")
    config = ProxyConfig(
        args.sub2api,
        args.auto_clean,
        args.canvas_routes_file,
        deployments,
        args.canvas_source_maintenance_file,
    )
    server = ThreadingHTTPServer((args.host, args.port), make_handler(config))

    def shutdown(_signum: int, _frame: object) -> None:
        threading.Thread(target=server.shutdown, daemon=True).start()

    signal.signal(signal.SIGTERM, shutdown)
    signal.signal(signal.SIGINT, shutdown)
    logging.info("path proxy listening on http://%s:%s", args.host, args.port)
    server.serve_forever()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
