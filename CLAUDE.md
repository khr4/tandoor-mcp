# tandoor-mcp — agent guide

MCP server exposing the [Tandoor Recipes](https://tandoor.dev) REST API over stdio.
Go, single binary, configured via environment variables.

## Commands

```sh
make build        # go build ./...
make test         # go test ./...   (no network; uses httptest)
make coverage     # go test with coverage profile; fails below 85%
make vet          # go vet ./...
make lint         # golangci-lint run ./... (if installed)
make verify       # vet + coverage + lint + secret-scan
go run .          # needs TANDOOR_URL and TANDOOR_TOKEN in the environment
```

## Layout

- `main.go` — entrypoint: read env config, build client, serve MCP over stdio
  or HTTP when configured.
- `internal/tandoor/` — HTTP client. `Do` (JSON) and `Upload` (multipart), Bearer
  auth, query building, safe-read retry/backoff with jitter and `Retry-After`,
  upstream concurrency cap, circuit breaker, `APIError` carrying the verbatim API
  body, and `OutcomeUnknownError` for mutating requests whose commit status
  cannot be proven after a temporary failure or cancellation after the request is
  attempted. Treats bodies as opaque JSON on purpose — the API is large and
  version-dependent.
- `internal/server/` — MCP tools, in two layers.
  - **Designed tools** (the point of this server) — task-oriented, ergonomic for
    agents: name-based inputs, parsed quantities, compact/readable output.
    - `recipes.go` — find/get/create/import/update/image/related/log_cooked.
    - `cooklog.go` — compact cook-history reads.
    - `planning.go` — `plan_meal`, `get_meal_plan`.
    - `shopping.go` — shopping list read/add/update/clear.
    - `pantry.go` / `inventory.go` — on-hand pantry flags and read-only
      inventory entries.
    - `taxonomy.go` — `list_taxonomy` / `create_taxonomy` / `rename_taxonomy`
      / `merge_taxonomy` / `move_taxonomy` (kind-parameterized, names accepted).
    - `ingredients.go` — parse natural lines, build nested ingredient payloads
      with **explicit amount + unit** (never flattened into text).
    - `resolve.go` — resolve names to ids (find-existing / get-or-create).
    - `format.go` — recipe cards, readable render, tolerant number parsing.
  - **Generic tools** (`crud.go`, `resources.go`) — `tandoor_list/get/create/
    update/delete/action` + `tandoor_resources`. A restricted escape hatch for
    non-secret, non-admin, non-raw-log resources the designed tools don't handle.
    NOT the primary surface.
  - `server.go` — server construction, tool registration, per-tool operation
    timeout wrapper, structured error helpers.
  - `http.go` / `readiness.go` — Streamable HTTP/SSE transport and cached,
    sanitized JSON readiness checks against the real Tandoor API.

Design rule: tools are shaped around what an agent is doing, not around REST
endpoints. Prefer a designed tool that hides nested serializer shapes and id
plumbing over exposing raw CRUD. When adding a workflow, resolve names to ids and
keep quantities (amount + unit) first-class.

Generated-image uploads use the common LLM/MCP base64 shape: `image_base64`
contains raw base64 bytes or a `data:image/...;base64,...` URI, and
`image_mime_type` carries `image/png`, `image/jpeg`, or `image/webp` when the MIME
type is not embedded in a data URI. Keep the inline decoded-size cap small enough
for agent context and JSON payload safety.

For `set_recipe_image` with `image_url`, tandoor-mcp validates and forwards the
URL, then Tandoor fetches and processes it server-side. Diagnose public URL
upload failures from Tandoor's upstream response first: `handle_image`/`None`
tracebacks mean the fetch likely succeeded but the returned bytes were not a
processable image for Tandoor. Otherwise check the Tandoor runtime's pod/server
egress, network policy, DNS, TLS/proxy configuration and remote-site blocking.
Use `image_base64` or `image_path` when Tandoor cannot handle external image
URLs.

Release versioning is tag-driven. `internal/server.Version` defaults to `dev`;
Docker/tag builds override it with
`-X github.com/khr4/tandoor-mcp/internal/server.Version=${VERSION}`.

Tools register with `mcp.AddTool[In, any]`: typed input struct (the SDK infers the
input JSON Schema), output type `any` so handlers can return readable text content
plus `structuredContent` without per-tool output-schema constraints.
`structuredContent` must always be an object. If a tool naturally returns a list,
put it under a named key such as `recipes`, `entries`, `items`, `resources` or
`data`; do not return a top-level array.

## Engineering discipline (non-negotiable)

This codebase is maintained to a strict standard. Hold the line:

- **No stubs, no placeholders.** Every function does the real thing against the
  real API. Do not add a tool whose body is `// TODO` or returns a canned value.
  If you cannot implement it correctly now, do not add it.
- **No TODO / FIXME / "later" comments.** Either fix it in the same change or open
  a tracked issue and reference it. The tree stays free of deferred work markers.
- **No theatrical tests.** A test must exercise real behavior and be able to fail.
  - Drive handlers/clients against an `httptest.Server`; assert on the actual
    request (method, path, query, body) and the actual response.
  - The end-to-end test (`integration_test.go`) drives the real MCP client through
    the in-memory transport — keep that path working.
  - Forbidden: assertions that restate the mock, tests with no assertions, tests
    that only check a value you just set, `if err != nil { t.Skip }` as a crutch.
- **No guessing API shapes into permanent code.** If a request/response shape is
  unverified, route it through `tandoor_action` (the documented escape hatch)
  rather than shipping a dedicated tool with a fabricated body. Dedicated tools
  exist only for endpoints whose contract is known.
- **Errors surface, never swallow.** Return `APIError` and wrapped errors up to the
  tool result; do not log-and-continue, silently broaden filters, silently save
  partial imports, or discard the API's message. Partial mutations must set
  `CallToolResult.IsError`; ambiguous writes must use a structured
  `outcome_unknown` result rather than claiming success. Pre-send failures and
  open-circuit fast-fails must surface as `not_attempted`, not `outcome_unknown`.
- **Errors stay agent-safe.** Tool/readiness errors use structured status objects
  and bounded body/cause excerpts so agents are not flooded or confused by huge
  upstream payloads. This is a context-size and agent-safety guard, not a privacy
  redaction boundary; do not rely on it to hide secrets or personal data.
- **Batch writes preserve uncertainty.** When one item in a batch has an
  ambiguous mutating failure, keep the per-item `outcome_unknown` failure and use
  an aggregate status such as `partial_outcome_unknown`. Do not flatten it into a
  generic string.
- **Guarded recipe writes use fresh revisions.** `update_recipe` keyword edits
  and `set_recipe_steps` require `expected_revision`; successful guarded writes
  return a new `edit_revision`. Re-read or use that returned revision before the
  next guarded mutation.
- **Build and test must be green before done.** `make verify` passes, including
  the 85% coverage gate and secret scan, or it is not finished.

## Security and privacy discipline

- **No secrets in the tree.** Do not commit real `TANDOOR_TOKEN`,
  `TANDOOR_API_TOKEN`, `TANDOOR_MCP_TOKEN`, bearer tokens, `.env` files, MCP
  client configs, local agent settings, API keys, private keys, cookies, or
  captured request headers. Documentation examples must use placeholders such as
  `xxxx` and `example.com`.
- **No personal Tandoor data.** Do not commit live recipe exports, shopping
  lists, pantry contents, meal plans, screenshots, logs, or payload captures from
  a real instance. Tests use synthetic fixtures only.
- **Use the tracked hook.** Run `make install-hooks` once in a checkout to enable
  `.githooks/pre-commit`; it requires `git-secrets` and registers this repo's
  provider patterns, including `TANDOOR_*TOKEN` env and JSON forms. Run
  `make secret-scan` before committing if hooks are not installed.
- **Redact before sharing failures.** When copying tool output into issues,
  commits, or docs, replace hostnames, bearer values, tokens, and user-specific
  recipe data with placeholders.

## Adding to the surface

- **New CRUD resource:** add one row to `resources` in `resources.go`. If it can
  expose credentials, account/admin state, uploaded files, logs, invitations,
  personal settings or other raw PII, add it to `restrictedResources` too.
- **New generic mutation:** add the resource to the audited mutation allowlist
  only after checking that raw create/update/delete cannot touch recipes, steps,
  shopping entries, inventory entries, imports, logs, admin/user/file/AI/storage/
  sync state, or other sensitive surfaces. Prefer a designed tool for workflows.
- **New custom endpoint:** `tandoor_action` is allowlist-only. Add a custom path
  to that allowlist only for read/helper endpoints whose response is safe for raw
  model consumption; otherwise add a dedicated tool once the request/response
  contract is verified. Cover either path with an `httptest`-backed test asserting
  the exact method, path, query and body.
- **Generic input validation is part of the contract.** Keep query keys, ordering,
  object ids, action paths and JSON bodies bounded and syntactically constrained.
  Do not relax those checks just to pass through an odd serializer shape; add a
  designed tool or a narrow allowlist extension with tests.
- Optional fields **must** be tagged `,omitempty` (the SDK marks any field without
  it as required). Required fields omit it.
