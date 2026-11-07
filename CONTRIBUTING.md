# Contributing

Issues and pull requests are welcome.

- Open an issue first for anything big, so we can agree on the shape before
  you write it.
- Keep a pull request to one change. Say what it fixes and how you checked it.
- `go vet ./...` and `go test -race ./...` must pass. Store and end-to-end
  tests need Postgres: set `GREENGRAM_TEST_DATABASE_URL` (see README).
  CI runs the same on every pull request.
- Changes to the admin panel (`admin/`): `bun test ./tests` in `admin/` must
  pass and `scripts/build-admin.sh out` must build. Pin new packages exactly
  (`bun add --exact`) and commit `admin/bun.lock`.
- New database changes go in a new file in `internal/store/migrations`, with
  the next free number. Never edit one that has shipped.
- Don't add Telegram's markup, pictures or captured files to the repository.
  They go in the mini apps bundle (see `cmd/ggminiapps`) or another bundle
  that is fetched at install time.
- Code you copy from elsewhere keeps its copyright line and licence; add it to
  `third_party/`.

By sending a pull request you agree that your change is released under the
AGPL-3.0, like the rest of the project.

Security problems: see `SECURITY.md`, not the issue tracker.
