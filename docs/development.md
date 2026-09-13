# Development

## Toolchain

- Go 1.26.6
- Node.js 20.19.5
- pnpm 10.28.2

Install JavaScript dependencies from the repository root and keep the lockfile
committed. Go commands run from `backend` or through the root Makefile.

## Rules

- Add tests before wiring a new official or Canvas contract.
- Never log bearer tokens, API keys, database DSNs, object contents, or signed
  URLs.
- Secrets are read from mounted files, not command arguments or Compose values.
- Do not import `github.com/Wei-Shaw/sub2api/internal/...`.
- Do not add runtime SQL queries against the official database.
- Keep production images immutable and address them by full digest.

