#!/usr/bin/env python3
"""Atomically update one non-secret Canvas proxy route slot."""

from __future__ import annotations

import argparse
import json
import os
import re
import tempfile
from pathlib import Path

from sub2api_path_proxy import parse_target

BUILD_ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--file", required=True)
    parser.add_argument("--slot", choices=("stable", "candidate"), required=True)
    parser.add_argument("--web", required=True)
    parser.add_argument("--api", required=True)
    parser.add_argument("--build-id", required=True)
    args = parser.parse_args()

    parse_target(args.web, require_loopback=True)
    parse_target(args.api, require_loopback=True)
    if not BUILD_ID.fullmatch(args.build_id):
        parser.error("invalid build ID")

    destination = Path(args.file).resolve()
    destination.parent.mkdir(parents=True, exist_ok=True)
    payload: dict[str, object] = {}
    if destination.exists():
        loaded = json.loads(destination.read_text(encoding="utf-8"))
        if not isinstance(loaded, dict):
            parser.error("existing route file is not an object")
        payload = loaded
    payload[args.slot] = {"web": args.web, "api": args.api, "build_id": args.build_id}

    descriptor, temporary_name = tempfile.mkstemp(prefix=destination.name + ".", dir=destination.parent)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
            json.dump(payload, handle, sort_keys=True, separators=(",", ":"))
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(temporary_name, 0o644)
        os.replace(temporary_name, destination)
    finally:
        if os.path.exists(temporary_name):
            os.unlink(temporary_name)
    print(json.dumps(payload, sort_keys=True, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
