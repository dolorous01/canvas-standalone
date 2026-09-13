# Canvas Standalone

Canvas Standalone separates the Sub2API Studio from the official Sub2API
application lifecycle. Official Sub2API remains authoritative for identity,
API keys, gateway scheduling, quota, and billing. This service owns Studio UI,
projects, assets, editor documents, and durable Canvas jobs.

## Status

The service was extracted from the verified Canvas.18 source revision
`6b390391c7d418567f2342c0b99fa0d558eaaece`. Candidate and migration rehearsals
are complete. Production changes still require every gate in the Chinese
dual-track upgrade runbook; a tagged image is not by itself approval to cut
traffic or enable writes.

## Release Boundary

- Official Sub2API is deployed independently by immutable digest through its
  existing blue-green workflow.
- Canvas Web, API, and Worker are deployed independently by immutable digest.
- Runtime integration uses versioned HTTP contracts only. Canvas does not read
  official tables, Redis keys, JWT signing secrets, or provider credentials.
- Production lifecycle endpoints remain blocked at the public path proxy.

See [docs/architecture.md](docs/architecture.md),
[docs/development.md](docs/development.md), and the Chinese
[dual-track upgrade runbook](docs/UPGRADE_CN.md). The Chinese
[key reconciliation runbook](docs/KEY_RECONCILIATION_CN.md) documents the
one-time manual binding and read-only post-update checks.

## Local Verification

```bash
pnpm install --frozen-lockfile
make fmt-check
make lint
make test
make test-deploy
make build
make check-canvas-license
make secret-scan
```

The production frontend build requires `VITE_CANVAS_SOURCE_URL` to identify
the corresponding source. The Makefile currently points at the exact source
revision used for extraction.

## License

Canvas Standalone is licensed under AGPL-3.0-only. The pinned upstream Canvas
snapshot retains its MIT license; see `frontend/LICENSE.upstream`,
`frontend/UPSTREAM.md`, and `NOTICE`.
