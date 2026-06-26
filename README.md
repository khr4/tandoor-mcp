# tandoor-mcp

An MCP server for [Tandoor Recipes](https://tandoor.dev). It runs over stdio and
exposes **task-oriented tools designed for agents** — find/read/create recipes,
plan meals, manage the shopping list and pantry — that hide Tandoor's nested REST
shapes (names instead of ids, parsed quantities, readable output). Generic CRUD
tools sit underneath for full coverage of every API resource.

## Build

```sh
go build -o tandoor-mcp .
# or: go install github.com/khr4/tandoor-mcp@latest
```

Requires Go 1.26+.

## Configuration

Configured entirely via environment variables:

| Variable | Required | Description |
|---|---|---|
| `TANDOOR_URL` | yes | Instance root, e.g. `https://recipes.example.com` (no `/api` suffix). |
| `TANDOOR_TOKEN` | yes | API token — Tandoor: *Settings → API → generate*. (`TANDOOR_API_TOKEN` also accepted.) |
| `TANDOOR_INSECURE_SKIP_VERIFY` | no | `true` to skip TLS verification (self-signed instances). |
| `TANDOOR_TIMEOUT` | no | Per-request timeout in seconds (default `30`). |

The token is sent as `Authorization: Bearer <token>`.

## Run

```sh
TANDOOR_URL=https://recipes.example.com TANDOOR_TOKEN=xxxx ./tandoor-mcp
```

Register it with an MCP client. Example (`claude_desktop_config.json` /
`.mcp.json`):

```json
{
  "mcpServers": {
    "tandoor": {
      "command": "/path/to/tandoor-mcp",
      "env": {
        "TANDOOR_URL": "https://recipes.example.com",
        "TANDOOR_TOKEN": "xxxx"
      }
    }
  }
}
```

## Tools

### Designed tools (use these)

Recipes:

| Tool | Purpose |
|---|---|
| `find_recipes` | Search by words, keyword/ingredient **names**, book, rating, or makeable-now. Returns compact cards. |
| `get_recipe` | One recipe as readable Markdown (metadata, ingredient lines, numbered steps). |
| `create_recipe` | Create a recipe. Ingredients as natural lines (`"2 cups flour"`, parsed into amount+unit+food) or explicit `{amount, unit, food}`. Foods/units/keywords created by name. |
| `import_recipe_from_url` | Scrape a web page and save it (`save=false` for a preview). |
| `update_recipe` | Targeted edits: name, description, servings, times, add/remove keywords. |
| `set_recipe_image` | Set an image from a local file or remote URL. |
| `find_related_recipes` | Recipes sharing keywords/foods. |
| `log_cooked` | Record a cook + rating (how recipes get rated). |

Meal planning, shopping, pantry:

| Tool | Purpose |
|---|---|
| `plan_meal` / `get_meal_plan` | Add to / read the meal-plan calendar (meal type by name). |
| `get_shopping_list` | Current entries as readable lines. |
| `add_to_shopping_list` | Add an ad-hoc food with amount + unit. |
| `add_recipe_to_shopping` | Add a recipe's ingredients (optionally scaled). |
| `update_shopping_item` / `clear_shopping_list` | Check off / edit / clear entries. |
| `get_pantry` / `set_food_on_hand` | Read / set foods marked on-hand. |
| `list_keywords` / `list_foods` / `list_units` | Discover names + ids. |
| `keyword_merge`/`move`, `food_merge`/`move`, `unit_merge` | Tidy the taxonomy. |

Ingredient quantities are always kept explicit: amounts and units are split out
(via Tandoor's parser for natural lines) rather than flattened into free text.

### Generic API tools (escape hatch)

Underneath, these reach **every** resource in `tandoor_resources` and any custom
endpoint, for cases the designed tools don't cover:

`tandoor_resources`, `tandoor_list`, `tandoor_get`, `tandoor_create`,
`tandoor_update`, `tandoor_delete`, `tandoor_action`.

## Development

```sh
make test   # httptest-backed; no network or live instance required
make vet
```

See [CLAUDE.md](CLAUDE.md) for architecture and contribution discipline.
