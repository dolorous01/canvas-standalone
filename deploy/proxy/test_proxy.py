from __future__ import annotations

import http.client
import json
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from sub2api_path_proxy import (
    CANVAS_BODY_LIMIT,
    CanvasRouteFile,
    ProxyConfig,
    is_blocked_lifecycle_request,
    is_canvas_source_write_request,
    make_handler,
    parse_target,
    rewrite_prefix,
    segment_match,
)


class ProxyRouteTests(unittest.TestCase):
    def setUp(self) -> None:
        self.directory = tempfile.TemporaryDirectory()
        self.routes = Path(self.directory.name, "routes.json")
        self.routes.write_text(
            json.dumps(
                {
                    "stable": {"web": "http://127.0.0.1:18100", "api": "http://127.0.0.1:18101", "build_id": "stable-1"},
                    "candidate": {"web": "http://127.0.0.1:18110", "api": "http://127.0.0.1:18111", "build_id": "next-2"},
                }
            ),
            encoding="utf-8",
        )
        self.config = ProxyConfig("http://127.0.0.1:18080", "http://127.0.0.1:8093", str(self.routes))

    def tearDown(self) -> None:
        self.directory.cleanup()

    def assert_route(self, path: str, port: int, upstream: str | None = None) -> None:
        route = self.config.route(path)
        self.assertEqual(route.target.port, port)
        self.assertEqual(route.upstream_path, upstream or path)

    def test_stable_candidate_and_static_build_routes(self) -> None:
        self.assert_route("/studio", 18100)
        self.assert_route("/studio/canvas/p-1?tab=x", 18100)
        self.assert_route("/canvas-api/v1/projects", 18101)
        self.assert_route("/studio-next/canvas/p-1", 18110)
        self.assert_route("/canvas-api-next/v1/projects?q=1", 18111, "/canvas-api/v1/projects?q=1")
        self.assert_route("/canvas-static-next/next-2/a.js", 18110, "/canvas-static/next-2/a.js")
        self.assert_route("/canvas-static/next-2/a.js", 18110)
        self.assert_route("/canvas-static/stable-1/a.js", 18100)
        self.assertEqual(self.config.route("/canvas-api/v1/assets").body_limit, CANVAS_BODY_LIMIT)

    def test_segment_boundaries_stay_on_official(self) -> None:
        for path in ("/studioevil", "/studio_next", "/canvas-api-evil", "/canvas-staticity/a.js"):
            self.assert_route(path, 18080)

    def test_lifecycle_guard_is_exact_and_method_specific(self) -> None:
        for path in (
            "/api/v1/admin/system/update",
            "/api/v1/admin/system/update/",
            "/api/v1/admin/system/update?now=1",
        ):
            self.assertTrue(is_blocked_lifecycle_request("POST", path))
        self.assertFalse(is_blocked_lifecycle_request("GET", "/api/v1/admin/system/update"))
        self.assertFalse(is_blocked_lifecycle_request("POST", "/api/v1/admin/system/update-now"))

    def test_legacy_canvas_maintenance_guard_is_segment_and_method_specific(self) -> None:
        for path in (
            "/api/v1/image-canvas/projects",
            "/api/v1/image-canvas/projects/p-1?tab=x",
            "/api/v1/admin/image-canvas/model-policy",
        ):
            self.assertTrue(is_canvas_source_write_request("POST", path))
        self.assertTrue(is_canvas_source_write_request("DELETE", "/api/v1/image-canvas/assets/a-1"))
        self.assertFalse(is_canvas_source_write_request("GET", "/api/v1/image-canvas/projects"))
        self.assertFalse(is_canvas_source_write_request("POST", "/api/v1/image-canvas-evil/projects"))

    def test_legacy_canvas_maintenance_marker_reloads_without_restart(self) -> None:
        marker = Path(self.directory.name, "legacy-canvas-frozen")
        config = ProxyConfig(
            "http://127.0.0.1:18080",
            "http://127.0.0.1:8093",
            str(self.routes),
            canvas_source_maintenance_file=str(marker),
        )
        self.assertFalse(config.source_writes_frozen())
        marker.touch(mode=0o600)
        self.assertTrue(config.source_writes_frozen())
        marker.unlink()
        self.assertFalse(config.source_writes_frozen())

    def test_route_file_reloads_atomically(self) -> None:
        route_file = CanvasRouteFile(str(self.routes))
        self.assertEqual(route_file.snapshot()["candidate"].build_id, "next-2")
        payload = json.loads(self.routes.read_text(encoding="utf-8"))
        payload["candidate"]["build_id"] = "next-3"
        self.routes.write_text(json.dumps(payload) + "\n", encoding="utf-8")
        self.assertEqual(route_file.snapshot()["candidate"].build_id, "next-3")

    def test_rejects_non_loopback_canvas_target(self) -> None:
        with self.assertRaises(ValueError):
            parse_target("http://example.com:18100", require_loopback=True)

    def test_helpers_preserve_query_and_segment_boundaries(self) -> None:
        self.assertTrue(segment_match("/studio/a", "/studio"))
        self.assertFalse(segment_match("/studioevil", "/studio"))
        self.assertEqual(rewrite_prefix("/canvas-api-next/v1/jobs?q=1", "/canvas-api-next", "/canvas-api"), "/canvas-api/v1/jobs?q=1")


class DeploymentProxyTests(unittest.TestCase):
    def setUp(self) -> None:
        class AdminAuthHandler(BaseHTTPRequestHandler):
            def do_GET(self) -> None:
                if self.path == "/api/v1/admin/system/version" and self.headers.get("Authorization") == "Bearer admin-token":
                    payload = {"code": 0, "message": "success", "data": {"version": "sub2-1"}}
                    body = json.dumps(payload).encode("ascii")
                    self.send_response(200)
                else:
                    body = b'{"code":403,"message":"forbidden"}'
                    self.send_response(403)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, _format: str, *_arguments: object) -> None:
                pass

        class FakeDeployments:
            def __init__(self) -> None:
                self.starts: list[tuple[str, str, str, str]] = []

            def status(self, current_version: str) -> dict[str, object]:
                return {"current_version": current_version, "components": {}}

            def get_operation(self, operation_id: str) -> dict[str, object]:
                return {"id": operation_id, "state": "succeeded"}

            def start(self, component: str, action: str, bearer: str, current_version: str) -> dict[str, object]:
                self.starts.append((component, action, bearer, current_version))
                return {"id": "deploy-" + "a" * 32, "state": "queued"}

        self.auth_server = ThreadingHTTPServer(("127.0.0.1", 0), AdminAuthHandler)
        self.auth_thread = threading.Thread(target=self.auth_server.serve_forever, daemon=True)
        self.auth_thread.start()
        self.deployments = FakeDeployments()
        config = ProxyConfig(
            f"http://127.0.0.1:{self.auth_server.server_address[1]}",
            "http://127.0.0.1:9",
            deployments=self.deployments,  # type: ignore[arg-type]
        )
        self.proxy_server = ThreadingHTTPServer(("127.0.0.1", 0), make_handler(config))
        self.proxy_thread = threading.Thread(target=self.proxy_server.serve_forever, daemon=True)
        self.proxy_thread.start()

    def tearDown(self) -> None:
        self.proxy_server.shutdown()
        self.proxy_server.server_close()
        self.auth_server.shutdown()
        self.auth_server.server_close()

    def request(
        self,
        method: str,
        path: str,
        *,
        token: str | None = None,
        body: bytes | None = None,
        headers: dict[str, str] | None = None,
    ) -> tuple[int, dict[str, object]]:
        request_headers = dict(headers or {})
        if token:
            request_headers["Authorization"] = f"Bearer {token}"
        connection = http.client.HTTPConnection("127.0.0.1", self.proxy_server.server_address[1], timeout=3)
        try:
            connection.request(method, path, body=body, headers=request_headers)
            response = connection.getresponse()
            return response.status, json.loads(response.read())
        finally:
            connection.close()

    def test_status_requires_a_live_admin_session(self) -> None:
        status, payload = self.request("GET", "/api/v1/admin/deployments")
        self.assertEqual(status, 401)
        self.assertEqual(payload["reason"], "invalid_admin_bearer")

        status, payload = self.request("GET", "/api/v1/admin/deployments", token="regular-user")
        self.assertEqual(status, 403)
        self.assertEqual(payload["reason"], "admin_access_required")

        status, payload = self.request("GET", "/api/v1/admin/deployments?timezone=Asia%2FShanghai", token="admin-token")
        self.assertEqual(status, 200)
        self.assertEqual(payload["data"]["current_version"], "sub2-1")

    def test_update_requires_exact_confirmation_and_empty_body(self) -> None:
        path = "/api/v1/admin/deployments/sub2api/update"
        headers = {"Content-Type": "application/json", "X-Deployment-Confirmation": "sub2api:update"}
        status, payload = self.request("POST", path, token="admin-token", body=b"{}", headers=headers)
        self.assertEqual(status, 202)
        self.assertEqual(payload["data"]["state"], "queued")
        self.assertEqual(self.deployments.starts, [("sub2api", "update", "admin-token", "sub2-1")])

        status, payload = self.request(
            "POST",
            path,
            token="admin-token",
            body=json.dumps({"image": "attacker/image:latest"}).encode("ascii"),
            headers=headers,
        )
        self.assertEqual(status, 400)
        self.assertEqual(payload["reason"], "invalid_deployment_request")

        status, payload = self.request(
            "POST",
            path,
            token="admin-token",
            body=b"{}",
            headers={"Content-Type": "application/json"},
        )
        self.assertEqual(status, 428)
        self.assertEqual(payload["reason"], "deployment_confirmation_required")

    def test_old_in_place_update_remains_blocked(self) -> None:
        status, payload = self.request("POST", "/api/v1/admin/system/update", token="admin-token", body=b"{}")
        self.assertEqual(status, 403)
        self.assertEqual(payload["reason"], "immutable_deployment_required")
        self.assertEqual(self.deployments.starts, [])


if __name__ == "__main__":
    unittest.main()
