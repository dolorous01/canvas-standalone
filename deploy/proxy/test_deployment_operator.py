from __future__ import annotations

import json
import tempfile
import threading
import time
import unittest
from pathlib import Path

from deployment_operator import (
    CATALOG_FORMAT,
    DeploymentError,
    DeploymentOperator,
    load_catalog,
)


SUB2_IMAGE = "ghcr.io/example/sub2api@sha256:" + "a" * 64
CANVAS_API_IMAGE = "ghcr.io/example/canvas-api@sha256:" + "b" * 64
CANVAS_WEB_IMAGE = "ghcr.io/example/canvas-web@sha256:" + "c" * 64


class DeploymentOperatorTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.deploy_dir = self.root / "deploy"
        self.deploy_dir.mkdir()
        for name in (
            "sub2api-release.sh",
            "sub2api-rollback.sh",
            "canvas-release.sh",
            "canvas-rollback.sh",
        ):
            script = self.deploy_dir / name
            script.write_text("#!/bin/sh\nexit 0\n", encoding="ascii")
            script.chmod(0o755)

        self.route_file = self.root / "routes.json"
        self.route_file.write_text(
            json.dumps(
                {
                    "stable": {
                        "web": "http://127.0.0.1:18100",
                        "api": "http://127.0.0.1:18101",
                        "build_id": "canvas-1",
                    }
                }
            ),
            encoding="ascii",
        )
        self.route_file.chmod(0o644)
        stable_releases = self.root / "state" / "stable" / "releases"
        stable_releases.mkdir(parents=True)
        (stable_releases / "current.env").write_text("CANVAS_RELEASE=canvas-1\n", encoding="ascii")
        (stable_releases / "previous.env").write_text("CANVAS_RELEASE=canvas-0\n", encoding="ascii")

        self.catalog_file = self.root / "catalog.json"
        self.write_catalog()
        self.operator_state = self.root / "state" / "operator"
        self.commands: list[list[str]] = []
        self.bearers_seen: list[str] = []

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def write_catalog(self, **overrides: object) -> None:
        value: dict[str, object] = {
            "format": CATALOG_FORMAT,
            "sub2api": {
                "version": "sub2-2",
                "image": SUB2_IMAGE,
                "release_url": "https://github.com/example/sub2api/releases/tag/sub2-2",
            },
            "canvas": {
                "version": "canvas-2",
                "build_id": "canvas-2",
                "api_image": CANVAS_API_IMAGE,
                "web_image": CANVAS_WEB_IMAGE,
                "release_url": "https://github.com/example/canvas/releases/tag/canvas-2",
            },
        }
        value.update(overrides)
        self.catalog_file.write_text(json.dumps(value), encoding="ascii")
        self.catalog_file.chmod(0o644)

    def successful_runner(self, command: list[str], _cwd: Path, log_path: Path, _timeout: int) -> int:
        self.commands.append(command)
        bearer_path = Path(command[command.index("--reconcile-bearer-file") + 1])
        report_path = Path(command[command.index("--reconcile-report") + 1])
        self.bearers_seen.append(bearer_path.read_text(encoding="ascii").strip())
        log_path.write_text("controlled deployment completed\n", encoding="ascii")
        log_path.chmod(0o600)
        report_path.write_text(
            json.dumps(
                {
                    "status": "attention_required",
                    "keys": {
                        "metadata_sha256": "d" * 64,
                        "active_but_unbound_ids": [10],
                        "inactive_but_bound_ids": [],
                        "missing_from_official": [{"external_api_key_id": 20}],
                    },
                    "plaintext_key_accessed": False,
                    "mutation_performed": False,
                }
            ),
            encoding="ascii",
        )
        report_path.chmod(0o600)
        return 0

    def make_operator(self, runner=None) -> DeploymentOperator:
        return DeploymentOperator(
            str(self.deploy_dir),
            str(self.route_file),
            str(self.catalog_file),
            str(self.operator_state),
            runner=runner or self.successful_runner,
        )

    def wait_for_terminal(self, operator: DeploymentOperator, operation_id: str) -> dict[str, object]:
        for _attempt in range(100):
            operation = operator.get_operation(operation_id)
            if operation["state"] in {"succeeded", "failed", "interrupted"}:
                return operation
            time.sleep(0.01)
        self.fail("deployment operation did not finish")

    def test_catalog_accepts_only_fixed_digest_targets(self) -> None:
        catalog = load_catalog(self.catalog_file)
        self.assertEqual(catalog.sub2api.image, SUB2_IMAGE)
        self.write_catalog(
            sub2api={
                "version": "sub2-2",
                "image": "ghcr.io/example/sub2api:latest",
                "release_url": None,
            }
        )
        with self.assertRaisesRegex(DeploymentError, "immutable"):
            load_catalog(self.catalog_file)

    def test_catalog_rejects_unknown_fields(self) -> None:
        payload = json.loads(self.catalog_file.read_text(encoding="ascii"))
        payload["command"] = "rm -rf /"
        self.catalog_file.write_text(json.dumps(payload), encoding="ascii")
        with self.assertRaisesRegex(DeploymentError, "invalid fields"):
            load_catalog(self.catalog_file)

    def test_sub2api_update_is_allowlisted_and_always_reconciles(self) -> None:
        operator = self.make_operator()
        status = operator.status("sub2-1")
        self.assertTrue(status["components"]["sub2api"]["update_enabled"])
        self.assertTrue(status["components"]["canvas"]["update_enabled"])

        started = operator.start("sub2api", "update", "header.payload.signature", "sub2-1")
        operation = self.wait_for_terminal(operator, started["id"])

        self.assertEqual(operation["state"], "succeeded")
        self.assertTrue(operation["deployment_succeeded"])
        self.assertEqual(operation["reconciliation"]["status"], "attention_required")
        self.assertEqual(operation["reconciliation"]["active_but_unbound_count"], 1)
        self.assertEqual(operation["reconciliation"]["missing_from_official_count"], 1)
        self.assertEqual(self.bearers_seen, ["header.payload.signature"])
        command = self.commands[0]
        self.assertEqual(command[0], str(self.deploy_dir / "sub2api-release.sh"))
        self.assertEqual(command[command.index("--image") + 1], SUB2_IMAGE)
        self.assertIn("--reconcile-bearer-file", command)
        self.assertIn("--reconcile-report", command)
        self.assertFalse(any((self.operator_state / "tokens").iterdir()))
        self.assertNotIn("header.payload.signature", json.dumps(operation))

    def test_canvas_update_uses_only_the_stable_slot(self) -> None:
        operator = self.make_operator()
        started = operator.start("canvas", "update", "token", "sub2-1")
        operation = self.wait_for_terminal(operator, started["id"])
        self.assertEqual(operation["state"], "succeeded")
        command = self.commands[0]
        self.assertEqual(command[0], str(self.deploy_dir / "canvas-release.sh"))
        self.assertEqual(command[command.index("--slot") + 1], "stable")
        self.assertEqual(command[command.index("--api-image") + 1], CANVAS_API_IMAGE)
        self.assertEqual(command[command.index("--web-image") + 1], CANVAS_WEB_IMAGE)

    def test_only_one_operation_can_run_at_a_time(self) -> None:
        entered = threading.Event()
        release = threading.Event()

        def blocking_runner(command: list[str], cwd: Path, log_path: Path, timeout: int) -> int:
            entered.set()
            release.wait(2)
            return self.successful_runner(command, cwd, log_path, timeout)

        operator = self.make_operator(blocking_runner)
        first = operator.start("sub2api", "update", "token-one", "sub2-1")
        self.assertTrue(entered.wait(1))
        with self.assertRaises(DeploymentError) as raised:
            operator.start("canvas", "update", "token-two", "sub2-1")
        self.assertEqual(raised.exception.status, 409)
        self.assertEqual(raised.exception.metadata["operation_id"], first["id"])
        release.set()
        self.wait_for_terminal(operator, first["id"])

    def test_running_operation_keeps_the_target_approved_at_start(self) -> None:
        entered = threading.Event()
        release = threading.Event()
        command_at_start: list[list[str]] = []

        def blocking_runner(command: list[str], cwd: Path, log_path: Path, timeout: int) -> int:
            command_at_start.append(list(command))
            entered.set()
            release.wait(2)
            return self.successful_runner(command, cwd, log_path, timeout)

        operator = self.make_operator(blocking_runner)
        started = operator.start("sub2api", "update", "token", "sub2-1")
        self.assertTrue(entered.wait(1))
        replacement_image = "ghcr.io/example/sub2api@sha256:" + "e" * 64
        self.write_catalog(
            sub2api={
                "version": "sub2-3",
                "image": replacement_image,
                "release_url": None,
            }
        )
        release.set()
        operation = self.wait_for_terminal(operator, started["id"])

        self.assertEqual(operation["target_version"], "sub2-2")
        command = command_at_start[0]
        self.assertEqual(command[command.index("--image") + 1], SUB2_IMAGE)
        self.assertNotIn(replacement_image, command)

    def test_sub2api_update_is_blocked_until_canvas_stable_exists(self) -> None:
        self.route_file.write_text("{}\n", encoding="ascii")
        self.route_file.chmod(0o644)
        operator = self.make_operator()
        status = operator.status("sub2-1")
        self.assertEqual(status["components"]["sub2api"]["update_block_reason"], "canvas_stable_required")
        with self.assertRaises(DeploymentError) as raised:
            operator.start("sub2api", "update", "token", "sub2-1")
        self.assertEqual(raised.exception.reason, "canvas_stable_required")

    def test_missing_reconciliation_report_fails_a_completed_update(self) -> None:
        def runner_without_report(_command: list[str], _cwd: Path, log_path: Path, _timeout: int) -> int:
            log_path.write_text("release returned zero without reconciliation\n", encoding="ascii")
            log_path.chmod(0o600)
            return 0

        operator = self.make_operator(runner_without_report)
        started = operator.start("sub2api", "update", "token", "sub2-1")
        operation = self.wait_for_terminal(operator, started["id"])
        self.assertEqual(operation["state"], "failed")
        self.assertEqual(operation["error"]["reason"], "reconciliation_report_missing")
        self.assertEqual(operation["reconciliation"]["state"], "not_run")


if __name__ == "__main__":
    unittest.main()
