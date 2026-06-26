# tandoor-mcp — agent guide

MCP server exposing the [Tandoor Recipes](https://tandoor.dev) REST API over stdio.
Go, single binary, configured via environment variables.

## Commands

```sh
make build        # go build ./...
make test         # go test ./...   (no network; uses httptest)
make vet          # go vet ./...
make lint         # golangci-lint run (if installed)
go run .          # needs TANDOOR_URL and TANDOOR_TOKEN in the environment
```

## Layout

- `main.go` — entrypoint: read env config, build client, serve MCP over stdio.
- `internal/tandoor/` — HTTP client. `Do` (JSON) and `Upload` (multipart), Bearer
  auth, query building, `APIError` carrying the verbatim API body. Treats bodies
  as opaque JSON on purpose — the API is large and version-dependent.
- `internal/server/` — MCP tools, in two layers.
  - **Designed tools** (the point of this server) — task-oriented, ergonomic for
    agents: name-based inputs, parsed quantities, compact/readable output.
    - `recipes.go` — find/get/create/import/update/image/related/log_cooked.
    - `planning.go` — `plan_meal`, `get_meal_plan`.
    - `shopping.go` — shopping list read/add/update/clear.
    - `pantry.go` — `get_pantry`, `set_food_on_hand`.
    - `taxonomy.go` — `list_taxonomy` / `merge_taxonomy` / `move_taxonomy` (kind-parameterized, names accepted).
    - `ingredients.go` — parse natural lines, build nested ingredient payloads
      with **explicit amount + unit** (never flattened into text).
    - `resolve.go` — resolve names to ids (find-existing / get-or-create).
    - `format.go` — recipe cards, readable render, tolerant number parsing.
  - **Generic tools** (`crud.go`, `resources.go`) — `tandoor_list/get/create/
    update/delete/action` + `tandoor_resources`. A thin escape hatch covering every
    resource for cases the designed tools don't handle. NOT the primary surface.
  - `server.go` — server construction, tool registration, result helpers.

Design rule: tools are shaped around what an agent is doing, not around REST
endpoints. Prefer a designed tool that hides nested serializer shapes and id
plumbing over exposing raw CRUD. When adding a workflow, resolve names to ids and
keep quantities (amount + unit) first-class.

Tools register with `mcp.AddTool[In, any]`: typed input struct (the SDK infers the
input JSON Schema), output type `any` so handlers return JSON/text content with no
output-schema constraints.

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
  tool result; do not log-and-continue or discard the API's message.
- **Build and test must be green before done.** `make vet test` passes, or it is
  not finished.

## Adding to the surface

- **New CRUD resource:** add one row to `resources` in `resources.go`. It is
  immediately reachable via the generic tools. Nothing else required.
- **New custom endpoint:** it already works through `tandoor_action`. Add a
  dedicated tool only when (a) the request/response contract is verified and (b) a
  typed input meaningfully helps the caller. Put it in `recipes.go` (or a sibling),
  give it a precise `jsonschema` description per field, and cover it with an
  `httptest`-backed test asserting the exact path/method/body.
- Optional fields **must** be tagged `,omitempty` (the SDK marks any field without
  it as required). Required fields omit it.
