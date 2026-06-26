// Package server exposes Tandoor Recipes as MCP tools in two layers: designed,
// task-oriented tools (find_recipes, create_recipe, import_recipe_from_url,
// plan_meal, shopping, pantry, ...) that take names and parsed quantities and
// return compact results, and generic tools (tandoor_list/get/create/update/
// delete/action + tandoor_resources) that reach every other API resource.
package server

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/khr4/tandoor-mcp/internal/tandoor"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Version is reported to MCP clients during initialization.
const Version = "0.2.0"

// Options configures server-layer policy that isn't part of the API client.
type Options struct {
	// ImageDir, when non-empty, is the only directory set_recipe_image may read
	// local files from. Empty disables local-file image uploads (image_url only).
	ImageDir string
}

// handlers binds tool implementations to a Tandoor client.
type handlers struct {
	c        *tandoor.Client
	imageDir string
}

// New builds an MCP server with every Tandoor tool registered.
func New(c *tandoor.Client, opts Options) *mcp.Server {
	h := &handlers{c: c, imageDir: opts.ImageDir}
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "tandoor-mcp",
		Title:   "Tandoor Recipes",
		Version: Version,
	}, nil)
	h.register(s)
	return s
}

// register wires up all tools. Out is always `any`, so the SDK skips output-schema
// inference and handlers return JSON/text content with no per-tool result schema.
func (h *handlers) register(s *mcp.Server) {
	// Generic API tools (escape hatch over every resource in tandoor_resources).
	mcp.AddTool(s, &mcp.Tool{Name: "tandoor_resources", Description: "List every Tandoor resource the generic tools can operate on, with path and description. The designed tools below are usually a better fit."}, h.resourceCatalog)
	mcp.AddTool(s, &mcp.Tool{Name: "tandoor_list", Description: "List/search a resource collection (raw paginated envelope). Use the designed tools for recipes, shopping, meal plans."}, h.genericList)
	mcp.AddTool(s, &mcp.Tool{Name: "tandoor_get", Description: "Fetch a single object of a resource by id (raw JSON)."}, h.genericGet)
	mcp.AddTool(s, &mcp.Tool{Name: "tandoor_create", Description: "Create an object in a resource collection from raw fields."}, h.genericCreate)
	mcp.AddTool(s, &mcp.Tool{Name: "tandoor_update", Description: "Update an object (PATCH, or PUT with full=true)."}, h.genericUpdate)
	mcp.AddTool(s, &mcp.Tool{Name: "tandoor_delete", Description: "Delete an object of a resource by id."}, h.genericDelete)
	mcp.AddTool(s, &mcp.Tool{Name: "tandoor_action", Description: "Call any other API endpoint not covered by a dedicated tool. Path is relative to /api/, e.g. 'meal-plan/ical/', 'fdc-search/'."}, h.genericAction)

	// Recipes.
	mcp.AddTool(s, &mcp.Tool{Name: "find_recipes", Description: "Search recipes by words, keyword/ingredient names (match ALL), recipe book, minimum rating, or what you can make from on-hand foods. Returns compact recipe cards (id, name, rating, times, tags)."}, h.findRecipes)
	mcp.AddTool(s, &mcp.Tool{Name: "get_recipe", Description: "Get one recipe (by name or id) as structured fields plus a readable Markdown view. Optionally re-scale amounts to a serving count."}, h.getRecipe)
	mcp.AddTool(s, &mcp.Tool{Name: "create_recipe", Description: "Create a recipe. Provide ingredients as natural lines (\"2 cups flour\", parsed into amount+unit+food) or as {amount, unit, food}. Use top-level ingredients for a simple recipe, or steps[] for multi-step. Foods/units/keywords are created by name."}, h.createRecipe)
	mcp.AddTool(s, &mcp.Tool{Name: "import_recipe_from_url", Description: "Import a recipe from a web page (http/https): scrapes and saves it (save=false returns a parsed preview)."}, h.importRecipeFromURL)
	mcp.AddTool(s, &mcp.Tool{Name: "update_recipe", Description: "Edit a recipe (by name or id): name, description, servings, times, source, add/remove keywords."}, h.updateRecipe)
	mcp.AddTool(s, &mcp.Tool{Name: "set_recipe_steps", Description: "Replace a recipe's steps and ingredients with a new list (re-describe them to edit; same ingredient format as create_recipe)."}, h.setRecipeSteps)
	mcp.AddTool(s, &mcp.Tool{Name: "delete_recipe", Description: "Delete a recipe (by name or id)."}, h.deleteRecipe)
	mcp.AddTool(s, &mcp.Tool{Name: "set_recipe_image", Description: "Set a recipe's image from a remote image URL, or a local file path if the server has an allowed image directory configured."}, h.setRecipeImage)
	mcp.AddTool(s, &mcp.Tool{Name: "find_related_recipes", Description: "List recipes related to a recipe (sharing keywords/foods)."}, h.findRelatedRecipes)
	mcp.AddTool(s, &mcp.Tool{Name: "log_cooked", Description: "Record that a recipe was cooked, optionally with a rating (0-5), servings and comment. This is how recipe ratings are set."}, h.logCooked)

	// Meal planning.
	mcp.AddTool(s, &mcp.Tool{Name: "plan_meal", Description: "Add an entry to the meal-plan calendar for a date and meal type (by name), optionally with a recipe (by name or id) and servings."}, h.planMeal)
	mcp.AddTool(s, &mcp.Tool{Name: "get_meal_plan", Description: "List meal-plan entries, optionally within a date range (YYYY-MM-DD)."}, h.getMealPlan)
	mcp.AddTool(s, &mcp.Tool{Name: "remove_meal_plan_entry", Description: "Remove a meal-plan entry by id (from get_meal_plan)."}, h.removeMealPlanEntry)

	// Shopping list.
	mcp.AddTool(s, &mcp.Tool{Name: "get_shopping_list", Description: "List shopping list entries as readable lines (unchecked by default)."}, h.getShoppingList)
	mcp.AddTool(s, &mcp.Tool{Name: "add_to_shopping_list", Description: "Add an ad-hoc food to the shopping list with an amount and optional unit."}, h.addToShoppingList)
	mcp.AddTool(s, &mcp.Tool{Name: "add_recipe_to_shopping", Description: "Add all of a recipe's ingredients to the shopping list (recipe by name or id), optionally scaling servings."}, h.addRecipeToShopping)
	mcp.AddTool(s, &mcp.Tool{Name: "update_shopping_item", Description: "Check off (or edit the amount of) a single shopping list entry."}, h.updateShoppingItem)
	mcp.AddTool(s, &mcp.Tool{Name: "clear_shopping_list", Description: "Remove shopping list entries (checked-off items by default, or all)."}, h.clearShoppingList)

	// Pantry / on-hand.
	mcp.AddTool(s, &mcp.Tool{Name: "get_pantry", Description: "List foods currently marked on-hand (in the pantry)."}, h.getPantry)
	mcp.AddTool(s, &mcp.Tool{Name: "set_food_on_hand", Description: "Mark a food as on-hand (in the pantry) or clear it. Used by makeable_now searches."}, h.setFoodOnHand)

	// Taxonomy (keywords / foods / units).
	mcp.AddTool(s, &mcp.Tool{Name: "list_taxonomy", Description: "List keywords, foods or units (id + name) for a given kind, optionally filtered by name."}, h.listTaxonomy)
	mcp.AddTool(s, &mcp.Tool{Name: "merge_taxonomy", Description: "Merge one keyword/food/unit into another (by name or id); the source is removed."}, h.mergeTaxonomy)
	mcp.AddTool(s, &mcp.Tool{Name: "move_taxonomy", Description: "Re-parent a keyword or food in its tree (by name or id); omit parent for top level."}, h.moveTaxonomy)
}

// --- result helpers ---

// rawResult returns Tandoor's JSON response as pretty-printed text content.
func rawResult(raw json.RawMessage) (*mcp.CallToolResult, any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return textResult("(empty response)")
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return textResult(string(raw)) // not JSON; pass through verbatim
	}
	return textResult(buf.String())
}

// jsonResult marshals a Go value to pretty JSON text content.
func jsonResult(v any) (*mcp.CallToolResult, any, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("encoding result: %w", err)
	}
	return textResult(string(b))
}

func textResult(text string) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
}
