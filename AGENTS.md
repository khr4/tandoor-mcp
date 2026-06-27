See [CLAUDE.md](CLAUDE.md) for build/test commands, layout, and the engineering
discipline (no stubs, no TODOs, no theatrical tests). It is the canonical guide
for any agent working in this repo.

Codex agents must also follow the security rules in `CLAUDE.md`:

- Never write real `TANDOOR_TOKEN`, `TANDOOR_API_TOKEN`, `TANDOOR_MCP_TOKEN`,
  bearer tokens, `.env` values, MCP client configs, live recipe exports, or other
  personal Tandoor data into tracked files.
- Keep examples synthetic (`xxxx`, `example.com`, placeholder paths) and avoid
  committing local agent settings.
- Before committing, run `make verify`; it includes vet, the coverage gate, lint
  and secret scanning. Install the tracked git hook with `make install-hooks`.
