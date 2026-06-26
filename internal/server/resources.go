package server

// Resource describes one Tandoor API collection reachable through the generic
// CRUD tools. Path is the route prefix under /api/ (the value Tandoor's router
// registers); it doubles as the canonical name callers pass as "resource".
type Resource struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Desc string `json:"description"`
}

// resources is the full catalog of Tandoor router-registered collections, taken
// from cookbook/urls.py. The generic tools accept any name listed here; the API
// itself enforces which verbs each collection supports (returning 405 otherwise).
var resources = []Resource{
	{"recipe", "recipe", "Recipes. Prefer find_recipes / get_recipe for searching and reading."},
	{"recipe-book", "recipe-book", "Recipe books (collections of recipes)."},
	{"recipe-book-entry", "recipe-book-entry", "Membership linking a recipe to a recipe book."},
	{"recipe-import", "recipe-import", "Recipes discovered in synced storage, pending import."},
	{"step", "step", "Steps belonging to a recipe."},
	{"ingredient", "ingredient", "Ingredients (food + amount + unit) within a step."},
	{"food", "food", "Foods (ingredients master list)."},
	{"food-inherit-field", "food-inherit-field", "Fields a child food inherits from its parent."},
	{"keyword", "keyword", "Keywords/tags applied to recipes (tree-structured)."},
	{"unit", "unit", "Measurement units."},
	{"unit-conversion", "unit-conversion", "Unit conversion rules, optionally food-specific."},
	{"property", "property", "Property values (e.g. nutrition) attached to foods/recipes."},
	{"property-type", "property-type", "Property type definitions (e.g. Calories, Fat)."},
	{"meal-plan", "meal-plan", "Meal plan entries on the calendar."},
	{"meal-type", "meal-type", "Meal types (Breakfast, Lunch, ...) used by the meal plan."},
	{"auto-plan", "auto-plan", "Auto meal-plan generator (create-only)."},
	{"shopping-list", "shopping-list", "Shopping lists (legacy aggregate endpoint)."},
	{"shopping-list-entry", "shopping-list-entry", "Individual shopping list entries."},
	{"shopping-list-recipe", "shopping-list-recipe", "Recipe groupings within a shopping list."},
	{"supermarket", "supermarket", "Supermarkets with ordered category layouts."},
	{"supermarket-category", "supermarket-category", "Supermarket aisle/categories."},
	{"supermarket-category-relation", "supermarket-category-relation", "Category ordering within a supermarket."},
	{"inventory-location", "inventory-location", "Inventory/pantry storage locations."},
	{"inventory-entry", "inventory-entry", "On-hand inventory entries."},
	{"inventory-log", "inventory-log", "Inventory change log."},
	{"storage", "storage", "External storage backends (Dropbox, Nextcloud, local)."},
	{"sync", "sync", "Synced storage folders watched for recipe files."},
	{"sync-log", "sync-log", "Storage sync run log."},
	{"connector-config", "connector-config", "Outbound connector configurations (e.g. Home Assistant)."},
	{"automation", "automation", "Import/parse automations applied to incoming recipes."},
	{"custom-filter", "custom-filter", "Saved search filters."},
	{"cook-log", "cook-log", "Per-recipe cook log entries."},
	{"view-log", "view-log", "Per-recipe view history."},
	{"import-log", "import-log", "Bulk import run log."},
	{"export-log", "export-log", "Bulk export run log."},
	{"bookmarklet-import", "bookmarklet-import", "Pages captured via the bookmarklet, pending import."},
	{"user-file", "user-file", "Uploaded user files."},
	{"user", "user", "Users visible in the active space."},
	{"user-preference", "user-preference", "Per-user preferences."},
	{"user-space", "user-space", "User-to-space memberships and roles."},
	{"space", "space", "Spaces (tenants)."},
	{"household", "household", "Households within a space."},
	{"group", "group", "Permission groups."},
	{"invite-link", "invite-link", "Space invite links."},
	{"access-token", "access-token", "API access tokens."},
	{"search-fields", "search-fields", "Available search fields (read-only)."},
	{"search-preference", "search-preference", "Per-user search behaviour preferences."},
	{"ai-provider", "ai-provider", "Configured AI providers."},
	{"ai-log", "ai-log", "AI request log."},
	{"localization", "localization", "Localization settings (read-only)."},
	{"server-settings", "server-settings", "Server settings (read-only)."},
	{"ingredient-parser", "ingredient-parser", "Natural-language ingredient parser."},
}

var resourceByName = func() map[string]Resource {
	m := make(map[string]Resource, len(resources))
	for _, r := range resources {
		m[r.Name] = r
	}
	return m
}()

// sensitiveResources hold credentials or other secrets and must not be reachable
// through the generic tools, which return response bodies verbatim to the model.
var sensitiveResources = map[string]bool{
	"storage":          true,
	"ai-provider":      true,
	"ai-log":           true,
	"access-token":     true,
	"connector-config": true,
}

func lookupResource(name string) (Resource, bool) {
	r, ok := resourceByName[name]
	return r, ok
}
