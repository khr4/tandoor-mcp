// Package server exposes Tandoor Recipes as MCP tools in two layers: designed,
// task-oriented tools (find_recipes, create_recipe, import_recipe_from_url,
// plan_meal, shopping, pantry, ...) that take names and parsed quantities and
// return compact results, and generic tools (tandoor_list/get/create/update/
// delete/action + tandoor_resources) that reach every other API resource.
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/khr4/tandoor-mcp/internal/tandoor"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const defaultOperationTimeout = 120 * time.Second

// Version is reported to MCP clients during initialization. Release builds can
// override it with -ldflags "-X github.com/khr4/tandoor-mcp/internal/server.Version=vX.Y.Z".
var Version = "dev"

func reportedVersion() string {
	if Version != "" && Version != "dev" {
		return Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			return bi.Main.Version
		}
		var rev string
		modified := false
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if len(s.Value) >= 12 {
					rev = s.Value[:12]
				} else {
					rev = s.Value
				}
			case "vcs.modified":
				modified = s.Value == "true"
			}
		}
		if rev != "" {
			if modified {
				return "dev-" + rev + "-dirty"
			}
			return "dev-" + rev
		}
	}
	return "dev"
}

// Options configures server-layer policy that isn't part of the API client.
type Options struct {
	// ImageDir, when non-empty, is the only directory set_recipe_image may read
	// local files from. Empty disables local-file image uploads (image_url only).
	ImageDir string
	// OperationTimeout bounds one MCP tool call across all upstream requests.
	// Zero uses the default.
	OperationTimeout time.Duration
}

// handlers binds tool implementations to a Tandoor client.
type handlers struct {
	c                *tandoor.Client
	imageDir         string
	operationTimeout time.Duration
}

// New builds an MCP server with every Tandoor tool registered.
func New(c *tandoor.Client, opts Options) *mcp.Server {
	if opts.OperationTimeout == 0 {
		opts.OperationTimeout = defaultOperationTimeout
	}
	h := &handlers{c: c, imageDir: opts.ImageDir, operationTimeout: opts.OperationTimeout}
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "tandoor-mcp",
		Title:   "Tandoor Recipes",
		Version: reportedVersion(),
	}, nil)
	h.register(s)
	return s
}

// register wires up all tools. Out is always `any`, so the SDK skips output-schema
// inference and handlers return JSON/text content with no per-tool result schema.
func (h *handlers) register(s *mcp.Server) {
	// Generic API tools (escape hatch over every resource in tandoor_resources).
	addTool(s, h, &mcp.Tool{Name: "tandoor_resources", Description: "List every Tandoor resource the generic tools can operate on, with path and description. The designed tools below are usually a better fit."}, h.resourceCatalog)
	addTool(s, h, &mcp.Tool{Name: "tandoor_list", Description: "List/search a resource collection (raw paginated envelope). Use the designed tools for recipes, shopping, meal plans."}, h.genericList)
	addTool(s, h, &mcp.Tool{Name: "tandoor_get", Description: "Fetch a single object of a resource by id (raw JSON)."}, h.genericGet)
	addTool(s, h, &mcp.Tool{Name: "tandoor_create", Description: "Create an object in a resource collection from raw fields."}, h.genericCreate)
	addTool(s, h, &mcp.Tool{Name: "tandoor_update", Description: "Update an object (PATCH, or PUT with full=true)."}, h.genericUpdate)
	addTool(s, h, &mcp.Tool{Name: "tandoor_delete", Description: "Delete an object of a resource by id."}, h.genericDelete)
	addTool(s, h, &mcp.Tool{Name: "tandoor_action", Description: "Call any other API endpoint not covered by a dedicated tool. Path is relative to /api/, e.g. 'meal-plan/ical/', 'fdc-search/'."}, h.genericAction)

	// Recipes.
	addTool(s, h, &mcp.Tool{Name: "find_recipes", Description: "Search recipes by words, keyword/ingredient names (match ALL), recipe book, minimum rating, or what you can make from on-hand foods. Returns compact recipe cards (id, name, rating, times, tags)."}, h.findRecipes)
	addTool(s, h, &mcp.Tool{Name: "get_recipe", Description: "Get one recipe (by name or id) as structured fields, a structured steps[] array (editable and accepted directly by set_recipe_steps), stored nutrition/properties when present, an edit_revision for safe replacement edits, and a readable Markdown view. Optionally re-scale ingredient amounts to a serving count (nutrition is shown as stored, not re-scaled)."}, h.getRecipe)
	addTool(s, h, &mcp.Tool{Name: "create_recipe", Description: "Create a recipe. Provide each ingredient as EITHER a natural line (\"2 cups flour\", parsed into amount+unit+food) OR structured {amount, unit, food} — not both. Omit amount for no-quantity items (\"salt to taste\"). For a section header use {is_header:true, note:\"For the sauce\"}. Use top-level ingredients for a simple recipe, or steps[] for multi-step (not both). Foods/units/keywords are created by name."}, h.createRecipe)
	addTool(s, h, &mcp.Tool{Name: "import_recipe_from_url", Description: "Import a recipe from a web page (http/https): scrapes and saves it (save=false returns a parsed preview)."}, h.importRecipeFromURL)
	addTool(s, h, &mcp.Tool{Name: "update_recipe", Description: "Edit a recipe (by name or id): name, description, servings, times, source, add/remove keywords. Keyword edits require expected_revision from get_recipe."}, h.updateRecipe)
	addTool(s, h, &mcp.Tool{Name: "set_recipe_steps", Description: "Replace a recipe's steps and ingredients with a new list. Requires expected_revision from get_recipe. To edit, read get_recipe's steps[], change what you need, and pass them back here (same shape)."}, h.setRecipeSteps)
	addTool(s, h, &mcp.Tool{Name: "delete_recipe", Description: "Delete a recipe (by name or id)."}, h.deleteRecipe)
	addTool(s, h, &mcp.Tool{Name: "set_recipe_image", Description: "Set a recipe's image from a remote image URL, or a local file path if the server has an allowed image directory configured."}, h.setRecipeImage)
	addTool(s, h, &mcp.Tool{Name: "find_related_recipes", Description: "List recipes related to a recipe (sharing keywords/foods)."}, h.findRelatedRecipes)
	addTool(s, h, &mcp.Tool{Name: "log_cooked", Description: "Record that a recipe was cooked, optionally with a rating (0-5), servings and comment. This is how recipe ratings are set."}, h.logCooked)

	// Recipe books (organizing recipes into collections).
	addTool(s, h, &mcp.Tool{Name: "add_recipe_to_book", Description: "Add a recipe (by name or id) to a recipe book (by name); the book is created if it does not exist. Idempotent."}, h.addRecipeToBook)
	addTool(s, h, &mcp.Tool{Name: "remove_recipe_from_book", Description: "Remove a recipe (by name or id) from a recipe book (by name or id). Reports not_in_book if it wasn't a member."}, h.removeRecipeFromBook)
	addTool(s, h, &mcp.Tool{Name: "list_recipe_books", Description: "List recipe books (id, name, description). Pass a recipe (name or id) to list only the books that recipe is in. Filter recipes by book with find_recipes' book argument."}, h.listRecipeBooks)

	// Meal planning.
	addTool(s, h, &mcp.Tool{Name: "plan_meal", Description: "Add an entry to the meal-plan calendar for a date and meal type (by name), optionally with a recipe (by name or id) and servings."}, h.planMeal)
	addTool(s, h, &mcp.Tool{Name: "get_meal_plan", Description: "List meal-plan entries, optionally within a date range (YYYY-MM-DD)."}, h.getMealPlan)
	addTool(s, h, &mcp.Tool{Name: "remove_meal_plan_entry", Description: "Remove a meal-plan entry by id (from get_meal_plan)."}, h.removeMealPlanEntry)

	// Shopping list.
	addTool(s, h, &mcp.Tool{Name: "get_shopping_list", Description: "List shopping list entries as readable lines (unchecked by default)."}, h.getShoppingList)
	addTool(s, h, &mcp.Tool{Name: "add_to_shopping_list", Description: "Add an ad-hoc food to the shopping list with an amount and optional unit."}, h.addToShoppingList)
	addTool(s, h, &mcp.Tool{Name: "add_recipe_to_shopping", Description: "Add all of a recipe's ingredients to the shopping list (recipe by name or id), optionally scaling servings."}, h.addRecipeToShopping)
	addTool(s, h, &mcp.Tool{Name: "update_shopping_item", Description: "Check off (or edit the amount of) a single shopping list entry."}, h.updateShoppingItem)
	addTool(s, h, &mcp.Tool{Name: "clear_shopping_list", Description: "Remove shopping list entries (checked-off items by default, or all)."}, h.clearShoppingList)
	addTool(s, h, &mcp.Tool{Name: "check_shopping_items", Description: "Check or uncheck many shopping list entries at once (ids from get_shopping_list). Pass checked=false to un-check; to un-check everything, get_shopping_list with include_checked=true and pass all ids."}, h.checkShoppingItems)

	// Pantry / on-hand.
	addTool(s, h, &mcp.Tool{Name: "get_pantry", Description: "List foods currently marked on-hand (in the pantry)."}, h.getPantry)
	addTool(s, h, &mcp.Tool{Name: "set_food_on_hand", Description: "Mark one or more foods as on-hand (in the pantry) or clear them — pass food for one, or foods[] to stock several in one call. Used by makeable_now searches."}, h.setFoodOnHand)

	// Taxonomy (keywords / foods / units).
	addTool(s, h, &mcp.Tool{Name: "list_taxonomy", Description: "List keywords, foods or units (id + name) for a given kind, optionally filtered by name."}, h.listTaxonomy)
	addTool(s, h, &mcp.Tool{Name: "merge_taxonomy", Description: "Merge one keyword/food/unit into another (by name or id); the source is removed."}, h.mergeTaxonomy)
	addTool(s, h, &mcp.Tool{Name: "move_taxonomy", Description: "Re-parent a keyword or food in its tree (by name or id); omit parent for top level."}, h.moveTaxonomy)
}

func addTool[I any](s *mcp.Server, h *handlers, tool *mcp.Tool, fn func(context.Context, *mcp.CallToolRequest, I) (*mcp.CallToolResult, any, error)) {
	mcp.AddTool(s, tool, withOperationBudget(h, fn))
}

func withOperationBudget[I any](h *handlers, fn func(context.Context, *mcp.CallToolRequest, I) (*mcp.CallToolResult, any, error)) func(context.Context, *mcp.CallToolRequest, I) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, in I) (*mcp.CallToolResult, any, error) {
		if h.operationTimeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, h.operationTimeout)
			defer cancel()
		}
		res, out, err := fn(ctx, req, in)
		if err != nil {
			var unknown *tandoor.OutcomeUnknownError
			if errors.As(err, &unknown) {
				return outcomeUnknownResult(unknown, nil)
			}
		}
		return res, out, err
	}
}

// --- result helpers ---

// rawResult returns Tandoor's JSON response as pretty-printed text content.
func rawResult(raw json.RawMessage) (*mcp.CallToolResult, any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return textResult("(empty response)")
	}
	var structured any
	if err := json.Unmarshal(raw, &structured); err != nil {
		return textResult(string(raw)) // not JSON; pass through verbatim
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return textResult(string(raw)) // not JSON; pass through verbatim
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: buf.String()}},
		StructuredContent: structured,
	}, nil, nil
}

// jsonResult marshals a Go value to pretty JSON text content.
func jsonResult(v any) (*mcp.CallToolResult, any, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("encoding result: %w", err)
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(b)}},
		StructuredContent: v,
	}, nil, nil
}

func jsonErrorResult(v any) (*mcp.CallToolResult, any, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("encoding error result: %w", err)
	}
	return &mcp.CallToolResult{
		IsError:           true,
		Content:           []mcp.Content{&mcp.TextContent{Text: string(b)}},
		StructuredContent: v,
	}, nil, nil
}

func outcomeUnknownResult(err *tandoor.OutcomeUnknownError, extra map[string]any) (*mcp.CallToolResult, any, error) {
	out := map[string]any{
		"status":  "outcome_unknown",
		"method":  err.Method,
		"path":    err.Path,
		"message": err.Error(),
	}
	for k, v := range extra {
		out[k] = v
	}
	return jsonErrorResult(out)
}

func textResult(text string) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
}
