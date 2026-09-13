from __future__ import annotations

import importlib.util
import io
import json
import os
import stat
import tempfile
import threading
import unittest
from contextlib import redirect_stderr
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any
from unittest import mock


MODULE_PATH = Path(__file__).with_name("post_update_reconcile.py")
SPEC = importlib.util.spec_from_file_location("post_update_reconcile", MODULE_PATH)
assert SPEC is not None and SPEC.loader is not None
reconcile = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(reconcile)


class PostUpdateReconcileTests(unittest.TestCase):
    def setUp(self) -> None:
        self.calls: list[tuple[str, str | None]] = []
        self.token = "header.payload.signature"
        self.responses: dict[str, dict[str, Any]] = {
            "/canvas-api/health/ready": {"status": "ready", "release": "0.2.0"},
            "/canvas-api/v1/session": {
                "code": 0,
                "message": "success",
                "data": {"external_user_id": 42, "role": "admin", "status": "active"},
            },
            "/canvas-api/v1/credentials/candidates": {
                "code": 0,
                "message": "success",
                "data": {
                    "items": [
                        {
                            "id": 20,
                            "name": "disabled key name is omitted from report",
                            "status": "disabled",
                            "quota": 20,
                            "quota_used": 2,
                            "bound": True,
                            "credential_id": "cred_20",
                            "key_hint": "...0020",
                        },
                        {
                            "id": 10,
                            "name": "active key name is omitted from report",
                            "group_id": 3,
                            "status": "active",
                            "quota": 10,
                            "quota_used": 1,
                            "expires_at": "2030-01-01T00:00:00Z",
                            "bound": False,
                        },
                        {
                            "id": 30,
                            "name": "usable key name is omitted from report",
                            "status": "active",
                            "quota": 30,
                            "quota_used": 3,
                            "bound": True,
                            "credential_id": "cred_30",
                            "key_hint": "...0030",
                        },
                    ],
                    "missing_bindings": [
                        {"external_api_key_id": 40, "binding_status": "active"},
                    ],
                },
            },
            "/canvas-api/v1/config": {
                "code": 0,
                "message": "success",
                "data": {
                    "enabled": True,
                    "api_keys": [
                        {"id": 10, "name": "one", "group_id": 3, "available": False, "unavailable_reason": "credential_not_bound"},
                        {"id": 20, "name": "two", "group_id": 0, "available": False, "unavailable_reason": "key_disabled"},
                        {"id": 30, "name": "three", "group_id": 0, "available": True},
                    ],
                    "selected_api_key_id": 30,
                    "policy_version": 2,
                    "models": [{"id": "model-one"}],
                },
            },
        }

    def fetch(self, _entry_base: str, path: str, bearer: str | None) -> dict[str, Any]:
        self.calls.append((path, bearer))
        return self.responses[path]

    def test_collects_a_read_only_redacted_report(self) -> None:
        report = reconcile.collect_report("stable", "http://127.0.0.1:8080", self.token, self.fetch)

        self.assertEqual(report["status"], "attention_required")
        self.assertEqual(report["canvas_release"], "0.2.0")
        self.assertEqual(report["user"]["external_user_id"], 42)
        self.assertEqual(report["keys"]["official_visible_count"], 3)
        self.assertEqual(report["keys"]["official_active_count"], 2)
        self.assertEqual(report["keys"]["visible_active_binding_count"], 2)
        self.assertEqual(report["keys"]["usable_count"], 1)
        self.assertEqual(report["keys"]["active_but_unbound_ids"], [10])
        self.assertEqual(report["keys"]["inactive_but_bound_ids"], [20])
        self.assertEqual(
            report["keys"]["missing_from_official"],
            [{"external_api_key_id": 40, "binding_status": "active"}],
        )
        self.assertEqual(len(report["keys"]["metadata_sha256"]), 64)
        self.assertFalse(report["plaintext_key_accessed"])
        self.assertFalse(report["mutation_performed"])
        serialized = json.dumps(report)
        for forbidden in (self.token, "...0020", "cred_20", "disabled key name"):
            self.assertNotIn(forbidden, serialized)
        self.assertEqual(self.calls[0], ("/canvas-api/health/ready", None))
        self.assertTrue(all(bearer == self.token for _, bearer in self.calls[1:]))

    def test_candidate_uses_candidate_prefix(self) -> None:
        self.responses = {path.replace("/canvas-api/", "/canvas-api-next/"): value for path, value in self.responses.items()}
        reconcile.collect_report("candidate", "http://localhost:8080", self.token, self.fetch)
        self.assertTrue(all(path.startswith("/canvas-api-next/") for path, _ in self.calls))

    def test_rejects_duplicate_and_inconsistent_key_ids(self) -> None:
        duplicate = self.responses["/canvas-api/v1/credentials/candidates"]["data"]["items"][0].copy()
        duplicate["id"] = 10
        self.responses["/canvas-api/v1/credentials/candidates"]["data"]["items"].append(duplicate)
        with self.assertRaisesRegex(reconcile.ReconcileError, "duplicate official API key ID"):
            reconcile.collect_report("stable", "http://127.0.0.1:8080", self.token, self.fetch)

    def test_rejects_secret_fields(self) -> None:
        payload = self.responses["/canvas-api/v1/credentials/candidates"]
        payload["data"]["items"][0]["key"] = "plaintext-must-not-pass"
        with self.assertRaisesRegex(reconcile.ReconcileError, "forbidden secret field"):
            reconcile.reject_secret_fields(payload)

    def test_reads_only_an_owned_mode_0600_token_file(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory, "bearer")
            path.write_text(self.token + "\n", encoding="ascii")
            path.chmod(0o600)
            self.assertEqual(reconcile.read_bearer(str(path)), self.token)
            path.chmod(0o640)
            with self.assertRaisesRegex(reconcile.ReconcileError, "0600"):
                reconcile.read_bearer(str(path))
            path.chmod(0o600)
            link = Path(directory, "bearer-link")
            link.symlink_to(path)
            with self.assertRaises(reconcile.ReconcileError):
                reconcile.read_bearer(str(link))
            path.write_bytes(b"x" * (reconcile.MAX_TOKEN_BYTES + 1))
            with self.assertRaisesRegex(reconcile.ReconcileError, "exactly one"):
                reconcile.read_bearer(str(path))

    def test_writes_a_new_mode_0600_report_without_overwriting(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory, "report.json")
            result = reconcile.write_report(str(path), {"status": "ok"})
            self.assertEqual(result, path)
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
            self.assertEqual(json.loads(path.read_text(encoding="utf-8")), {"status": "ok"})
            with self.assertRaisesRegex(reconcile.ReconcileError, "refusing to overwrite"):
                reconcile.write_report(str(path), {"status": "changed"})

    def test_entry_base_is_loopback_only(self) -> None:
        self.assertEqual(reconcile.validate_entry_base("http://127.0.0.1:8080/"), "http://127.0.0.1:8080")
        for value in ("https://127.0.0.1:8080", "http://example.com:8080", "http://127.0.0.1"):
            with self.subTest(value=value), self.assertRaises(reconcile.ReconcileError):
                reconcile.validate_entry_base(value)

    def test_failure_report_does_not_copy_an_invalid_entry_url(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            report_path = Path(directory, "failed.json")
            malicious = "http://operator:must-not-be-reported@127.0.0.1:8080"
            arguments = [
                str(MODULE_PATH),
                "--slot",
                "stable",
                "--entry-base",
                malicious,
                "--bearer-file",
                str(Path(directory, "unused")),
                "--report",
                str(report_path),
            ]
            with mock.patch.object(reconcile.sys, "argv", arguments), redirect_stderr(io.StringIO()):
                self.assertEqual(reconcile.main(), 1)
            report = json.loads(report_path.read_text(encoding="utf-8"))
            self.assertEqual(report["entry_base"], "invalid")
            self.assertNotIn("must-not-be-reported", report_path.read_text(encoding="utf-8"))

    def test_http_fetch_sends_bearer_but_rejects_redirects(self) -> None:
        received: list[tuple[str, str | None]] = []

        class SinkHandler(BaseHTTPRequestHandler):
            def do_GET(self) -> None:
                received.append((self.path, self.headers.get("Authorization")))
                body = b'{"code":0,"data":{"value":"ok"}}'
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, _format: str, *_arguments: object) -> None:
                pass

        sink = ThreadingHTTPServer(("127.0.0.1", 0), SinkHandler)
        sink_thread = threading.Thread(target=sink.serve_forever, daemon=True)
        sink_thread.start()
        sink_base = f"http://127.0.0.1:{sink.server_address[1]}"
        self.addCleanup(sink.server_close)
        self.addCleanup(sink.shutdown)

        payload = reconcile.fetch_json(sink_base, "/direct", self.token)
        self.assertEqual(payload["data"]["value"], "ok")
        self.assertEqual(received, [("/direct", f"Bearer {self.token}")])

        class RedirectHandler(BaseHTTPRequestHandler):
            def do_GET(self) -> None:
                self.send_response(302)
                self.send_header("Location", sink_base + "/redirect-target")
                self.end_headers()

            def log_message(self, _format: str, *_arguments: object) -> None:
                pass

        redirect = ThreadingHTTPServer(("127.0.0.1", 0), RedirectHandler)
        redirect_thread = threading.Thread(target=redirect.serve_forever, daemon=True)
        redirect_thread.start()
        self.addCleanup(redirect.server_close)
        self.addCleanup(redirect.shutdown)
        redirect_base = f"http://127.0.0.1:{redirect.server_address[1]}"

        with self.assertRaisesRegex(reconcile.ReconcileError, "HTTP 302"):
            reconcile.fetch_json(redirect_base, "/redirect", self.token)
        self.assertEqual(received, [("/direct", f"Bearer {self.token}")])


if __name__ == "__main__":
    unittest.main()
