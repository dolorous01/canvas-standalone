from __future__ import annotations

import sys
import tempfile
import unittest
from pathlib import Path

OPERATOR_DIR = Path(__file__).resolve().parent
PROXY_DIR = OPERATOR_DIR.parent / "proxy"
sys.path.insert(0, str(OPERATOR_DIR))
sys.path.insert(0, str(PROXY_DIR))

from deployment_operator import DeploymentError, load_catalog  # noqa: E402
from update_catalog import run  # noqa: E402


SUB2_IMAGE = "ghcr.io/example/sub2api@sha256:" + "a" * 64
CANVAS_API_IMAGE = "ghcr.io/example/canvas-api@sha256:" + "b" * 64
CANVAS_WEB_IMAGE = "ghcr.io/example/canvas-web@sha256:" + "c" * 64


class UpdateCatalogTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.catalog = Path(self.temporary.name) / "releases.json"

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def test_component_updates_are_atomic_and_preserve_the_other_target(self) -> None:
        self.assertEqual(
            run(
                [
                    "--catalog",
                    str(self.catalog),
                    "sub2api",
                    "--version",
                    "sub2-2",
                    "--image",
                    SUB2_IMAGE,
                ]
            ),
            0,
        )
        self.assertEqual(
            run(
                [
                    "--catalog",
                    str(self.catalog),
                    "canvas",
                    "--version",
                    "canvas-2",
                    "--build-id",
                    "canvas-build-2",
                    "--api-image",
                    CANVAS_API_IMAGE,
                    "--web-image",
                    CANVAS_WEB_IMAGE,
                ]
            ),
            0,
        )

        catalog = load_catalog(self.catalog)
        self.assertEqual(catalog.sub2api.image, SUB2_IMAGE)
        self.assertEqual(catalog.canvas.api_image, CANVAS_API_IMAGE)
        self.assertEqual(self.catalog.stat().st_mode & 0o777, 0o600)
        self.assertEqual(list(self.catalog.parent.glob(".releases.json.*.tmp")), [])

    def test_invalid_tag_does_not_replace_the_existing_catalog(self) -> None:
        run(
            [
                "--catalog",
                str(self.catalog),
                "sub2api",
                "--version",
                "sub2-2",
                "--image",
                SUB2_IMAGE,
            ]
        )
        before = self.catalog.read_bytes()

        with self.assertRaises(DeploymentError):
            run(
                [
                    "--catalog",
                    str(self.catalog),
                    "sub2api",
                    "--version",
                    "sub2-3",
                    "--image",
                    "ghcr.io/example/sub2api:latest",
                ]
            )

        self.assertEqual(self.catalog.read_bytes(), before)

    def test_clear_disables_only_the_selected_component(self) -> None:
        run(
            [
                "--catalog",
                str(self.catalog),
                "sub2api",
                "--version",
                "sub2-2",
                "--image",
                SUB2_IMAGE,
            ]
        )
        run(["--catalog", str(self.catalog), "clear", "--component", "canvas"])
        catalog = load_catalog(self.catalog)
        self.assertIsNotNone(catalog.sub2api)
        self.assertIsNone(catalog.canvas)


if __name__ == "__main__":
    unittest.main()
