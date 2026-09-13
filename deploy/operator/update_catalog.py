#!/usr/bin/env python3
"""Atomically manage the allowlisted deployment release catalog."""

from __future__ import annotations

import argparse
import json
import os
import sys
import tempfile
from pathlib import Path
from typing import Any, Sequence

PROXY_DIR = Path(__file__).resolve().parents[1] / "proxy"
sys.path.insert(0, str(PROXY_DIR))

from deployment_operator import CATALOG_FORMAT, DeploymentCatalog, DeploymentError, load_catalog  # noqa: E402


def catalog_payload(catalog: DeploymentCatalog | None) -> dict[str, Any]:
    if catalog is None:
        return {"format": CATALOG_FORMAT, "sub2api": None, "canvas": None}
    return {
        "format": CATALOG_FORMAT,
        "sub2api": (
            {
                "version": catalog.sub2api.version,
                "image": catalog.sub2api.image,
                "release_url": catalog.sub2api.release_url,
            }
            if catalog.sub2api
            else None
        ),
        "canvas": (
            {
                "version": catalog.canvas.version,
                "build_id": catalog.canvas.build_id,
                "api_image": catalog.canvas.api_image,
                "web_image": catalog.canvas.web_image,
                "release_url": catalog.canvas.release_url,
            }
            if catalog.canvas
            else None
        ),
    }


def write_target(catalog_path: Path, component: str, target: dict[str, Any] | None) -> None:
    if not catalog_path.is_absolute():
        raise DeploymentError(2, "invalid_catalog_path", "catalog path must be absolute")
    if catalog_path.is_symlink():
        raise DeploymentError(2, "invalid_catalog_path", "catalog path must not be a symlink")

    parent = catalog_path.parent.resolve(strict=True)
    if not parent.is_dir() or parent.is_symlink():
        raise DeploymentError(2, "invalid_catalog_path", "catalog parent directory is invalid")

    current = load_catalog(catalog_path) if catalog_path.exists() else None
    payload = catalog_payload(current)
    payload[component] = target
    raw = (json.dumps(payload, ensure_ascii=True, indent=2) + "\n").encode("ascii")

    descriptor, temporary_name = tempfile.mkstemp(
        prefix=f".{catalog_path.name}.",
        suffix=".tmp",
        dir=parent,
    )
    temporary = Path(temporary_name)
    try:
        os.fchmod(descriptor, 0o600)
        with os.fdopen(descriptor, "wb") as handle:
            descriptor = -1
            handle.write(raw)
            handle.flush()
            os.fsync(handle.fileno())
        load_catalog(temporary)
        os.replace(temporary, catalog_path)
        directory_descriptor = os.open(parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
        try:
            os.fsync(directory_descriptor)
        finally:
            os.close(directory_descriptor)
    finally:
        if descriptor >= 0:
            os.close(descriptor)
        try:
            temporary.unlink()
        except FileNotFoundError:
            pass


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Update the immutable deployment release catalog.")
    parser.add_argument("--catalog", required=True, type=Path, help="Absolute catalog JSON path")
    commands = parser.add_subparsers(dest="command", required=True)

    sub2api = commands.add_parser("sub2api", help="Approve a Sub2API image")
    sub2api.add_argument("--version", required=True)
    sub2api.add_argument("--image", required=True)
    sub2api.add_argument("--release-url")

    canvas = commands.add_parser("canvas", help="Approve a Canvas Web/API image pair")
    canvas.add_argument("--version", required=True)
    canvas.add_argument("--build-id", required=True)
    canvas.add_argument("--api-image", required=True)
    canvas.add_argument("--web-image", required=True)
    canvas.add_argument("--release-url")

    clear = commands.add_parser("clear", help="Disable updates for one component")
    clear.add_argument("--component", required=True, choices=("sub2api", "canvas"))

    commands.add_parser("validate", help="Validate the catalog without changing it")
    return parser


def run(arguments: Sequence[str]) -> int:
    args = build_parser().parse_args(arguments)
    catalog_path = args.catalog
    if args.command == "validate":
        load_catalog(catalog_path)
        print(f"catalog is valid: {catalog_path}")
        return 0

    if args.command == "sub2api":
        target = {
            "version": args.version,
            "image": args.image,
            "release_url": args.release_url,
        }
        component = "sub2api"
    elif args.command == "canvas":
        target = {
            "version": args.version,
            "build_id": args.build_id,
            "api_image": args.api_image,
            "web_image": args.web_image,
            "release_url": args.release_url,
        }
        component = "canvas"
    else:
        component = args.component
        target = None

    write_target(catalog_path, component, target)
    print(f"updated {component} target: {catalog_path}")
    return 0


def main() -> int:
    try:
        return run(sys.argv[1:])
    except (DeploymentError, OSError) as error:
        print(f"error: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
