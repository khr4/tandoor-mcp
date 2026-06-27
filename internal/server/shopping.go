package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// shoppingScanPages bounds the shopping-list pagination loop so a misbehaving
// instance whose `next` never clears cannot spin forever.
const shoppingScanPages = 50

type apiShoppingEntry struct {
	ID       int     `json:"id"`
	Amount   flexNum `json:"amount"`
	Unit     *named  `json:"unit"`
	Food     *named  `json:"food"`
	Checked  bool    `json:"checked"`
	NoAmount bool    `json:"no_amount"`
}

type shoppingItem struct {
	ID      int    `json:"id"`
	Item    string `json:"item"`
	Amount  string `json:"amount,omitempty"`
	Unit    string `json:"unit,omitempty"`
	Food    string `json:"food,omitempty"`
	Checked bool   `json:"checked"`
}

type shoppingListOutput struct {
	Items     []shoppingItem `json:"items"`
	Truncated bool           `json:"truncated,omitempty"`
	Note      string         `json:"note,omitempty"`
}

func (e apiShoppingEntry) line() string {
	ing := apiIngredient{Amount: e.Amount, NoAmount: e.NoAmount}
	if e.Unit != nil {
		ing.Unit = &apiUnit{Name: e.Unit.Name}
	}
	if e.Food != nil {
		ing.Food = &apiFood{Name: e.Food.Name}
	}
	return formatIngredient(ing)
}

func toShoppingItem(e apiShoppingEntry) shoppingItem {
	item := shoppingItem{ID: e.ID, Item: e.line(), Checked: e.Checked}
	if !e.NoAmount {
		item.Amount = e.Amount.String()
	}
	if e.Unit != nil {
		item.Unit = e.Unit.Name
	}
	if e.Food != nil {
		item.Food = e.Food.Name
	}
	return item
}

// shoppingEntries fetches shopping list entries, following pagination up to a
// hard cap. truncated reports when more entries may exist beyond the cap.
func (h *handlers) shoppingEntries(ctx context.Context) ([]apiShoppingEntry, bool, error) {
	var all []apiShoppingEntry
	for page := 1; page <= shoppingScanPages; page++ {
		q := url.Values{}
		q.Set("page", strconv.Itoa(page))
		q.Set("page_size", "200")
		raw, err := h.c.Do(ctx, http.MethodGet, "shopping-list-entry/", q, nil)
		if err != nil {
			return nil, false, err
		}
		var env listEnvelope
		if err := json.Unmarshal(raw, &env); err == nil && len(env.Results) > 0 {
			var batch []apiShoppingEntry
			if err := json.Unmarshal(env.Results, &batch); err != nil {
				return nil, false, fmt.Errorf("decoding shopping list: %w", err)
			}
			all = append(all, batch...)
			if env.Next == nil || *env.Next == "" {
				break
			}
			if page == shoppingScanPages {
				return all, true, nil
			}
			continue
		}
		// Not a paginated envelope (bare array, or empty): decode once and stop.
		var batch []apiShoppingEntry
		if err := decodeList(raw, &batch); err != nil {
			return nil, false, fmt.Errorf("decoding shopping list: %w", err)
		}
		all = append(all, batch...)
		break
	}
	return all, false, nil
}

// ---- get_shopping_list ----

type getShoppingInput struct {
	IncludeChecked *bool `json:"include_checked,omitempty" jsonschema:"include items already checked off (default false)"`
}

// getShoppingList returns current shopping list entries as readable lines.
func (h *handlers) getShoppingList(ctx context.Context, _ *mcp.CallToolRequest, in getShoppingInput) (*mcp.CallToolResult, any, error) {
	entries, truncated, err := h.shoppingEntries(ctx)
	if err != nil {
		return nil, nil, err
	}
	includeChecked := in.IncludeChecked != nil && *in.IncludeChecked
	items := make([]shoppingItem, 0, len(entries))
	for _, e := range entries {
		if e.Checked && !includeChecked {
			continue
		}
		items = append(items, toShoppingItem(e))
	}
	out := shoppingListOutput{Items: items, Truncated: truncated}
	if truncated {
		out.Note = fmt.Sprintf("scanned the first %d pages; more shopping entries may exist", shoppingScanPages)
	}
	return jsonResult(out)
}

// ---- add_to_shopping_list ----

type addShoppingInput struct {
	Food   string   `json:"food" jsonschema:"food name to add; created if missing"`
	Amount *float64 `json:"amount,omitempty" jsonschema:"quantity; omit for a no-amount item"`
	Unit   string   `json:"unit,omitempty" jsonschema:"unit name for the amount, e.g. g, cup"`
}

// addToShoppingList adds an ad-hoc food to the shopping list.
func (h *handlers) addToShoppingList(ctx context.Context, _ *mcp.CallToolRequest, in addShoppingInput) (*mcp.CallToolResult, any, error) {
	food, err := cleanName("food", in.Food)
	if err != nil {
		return nil, nil, err
	}
	body := map[string]any{
		"food":    map[string]any{"name": food},
		"checked": false,
	}
	// No amount given means a quantity-less item (e.g. "olive oil"), not "1 olive
	// oil"; mark it no-amount rather than defaulting to 1.
	if in.Amount != nil {
		body["amount"] = *in.Amount
	} else {
		body["amount"] = 0
		body["no_amount"] = true
	}
	if u, err := cleanOptionalName("unit", in.Unit); err != nil {
		return nil, nil, err
	} else if u != "" {
		body["unit"] = map[string]any{"name": u}
	}
	raw, err := h.c.Do(ctx, http.MethodPost, "shopping-list-entry/", nil, body)
	if err != nil {
		return nil, nil, err
	}
	var created apiShoppingEntry
	if err := json.Unmarshal(raw, &created); err != nil {
		return nil, nil, fmt.Errorf("decoding created shopping entry: %w", err)
	}
	return jsonResult(map[string]any{"status": "added", "id": created.ID, "item": food, "entry": toShoppingItem(created)})
}

// ---- add_recipe_to_shopping ----

type addRecipeShoppingInput struct {
	Recipe   string `json:"recipe" jsonschema:"recipe name or id whose ingredients to add"`
	Servings *int   `json:"servings,omitempty" jsonschema:"servings to scale the amounts to"`
}

// addRecipeToShopping adds all of a recipe's ingredients to the shopping list.
func (h *handlers) addRecipeToShopping(ctx context.Context, _ *mcp.CallToolRequest, in addRecipeShoppingInput) (*mcp.CallToolResult, any, error) {
	id, err := h.resolveRecipe(ctx, in.Recipe)
	if err != nil {
		return nil, nil, err
	}
	body := map[string]any{}
	if in.Servings != nil && *in.Servings <= 0 {
		return nil, nil, fmt.Errorf("servings must be positive")
	}
	setInt(body, "servings", in.Servings)
	if _, err := h.c.Do(ctx, http.MethodPut, fmt.Sprintf("recipe/%d/shopping/", id), nil, body); err != nil {
		return nil, nil, err
	}
	return jsonResult(map[string]any{"status": "added", "recipe_id": id})
}

// ---- update_shopping_item ----

type updateShoppingInput struct {
	ID      int      `json:"id" jsonschema:"shopping list entry id"`
	Checked *bool    `json:"checked,omitempty" jsonschema:"mark bought (true) or not bought (false)"`
	Amount  *float64 `json:"amount,omitempty" jsonschema:"new quantity"`
}

// updateShoppingItem checks off or edits a single shopping list entry.
func (h *handlers) updateShoppingItem(ctx context.Context, _ *mcp.CallToolRequest, in updateShoppingInput) (*mcp.CallToolResult, any, error) {
	if err := validatePositiveID("shopping list entry id", in.ID); err != nil {
		return nil, nil, err
	}
	body := map[string]any{}
	var changed []string
	if in.Checked != nil {
		body["checked"] = *in.Checked
		changed = append(changed, "checked")
	}
	if in.Amount != nil {
		body["amount"] = *in.Amount
		changed = append(changed, "amount")
	}
	if len(body) == 0 {
		return nil, nil, fmt.Errorf("provide checked and/or amount")
	}
	raw, err := h.c.Do(ctx, http.MethodPatch, fmt.Sprintf("shopping-list-entry/%d/", in.ID), nil, body)
	if err != nil {
		return nil, nil, err
	}
	out := map[string]any{"status": "updated", "id": in.ID, "changed_fields": changed}
	var updated apiShoppingEntry
	if err := json.Unmarshal(raw, &updated); err == nil && updated.ID != 0 {
		out["entry"] = toShoppingItem(updated)
	}
	return jsonResult(out)
}

// ---- clear_shopping_list ----

type clearShoppingInput struct {
	OnlyChecked *bool `json:"only_checked,omitempty" jsonschema:"only remove checked-off items (default true)"`
}

// clearShoppingList removes shopping list entries, reporting the true count even
// if some deletions fail.
func (h *handlers) clearShoppingList(ctx context.Context, _ *mcp.CallToolRequest, in clearShoppingInput) (*mcp.CallToolResult, any, error) {
	onlyChecked := in.OnlyChecked == nil || *in.OnlyChecked
	entries, truncated, err := h.shoppingEntries(ctx)
	if err != nil {
		return nil, nil, err
	}
	if truncated {
		return nil, nil, fmt.Errorf("refusing to clear shopping list after a partial scan of %d pages; use tandoor_list with explicit pagination", shoppingScanPages)
	}
	removed := 0
	attempted := 0
	skippedUnchecked := 0
	var removedIDs []int
	var failures []map[string]any
	for _, e := range entries {
		if onlyChecked && !e.Checked {
			skippedUnchecked++
			continue
		}
		attempted++
		if ctx.Err() != nil {
			failures = append(failures, failureObject(ctx.Err(), map[string]any{"id": e.ID, "item": e.line(), "phase": "delete"}))
			break
		}
		if _, err := h.c.Do(ctx, http.MethodDelete, fmt.Sprintf("shopping-list-entry/%d/", e.ID), nil, nil); err != nil {
			failures = append(failures, failureObject(err, map[string]any{"id": e.ID, "item": e.line(), "phase": "delete"}))
			continue
		}
		removed++
		removedIDs = append(removedIDs, e.ID)
	}
	scope := "all items"
	if onlyChecked {
		scope = "checked-off items"
	}
	out := map[string]any{
		"status":                "cleared",
		"removed":               removed,
		"scope":                 scope,
		"attempted":             attempted,
		"succeeded":             removed,
		"failed":                len(failures),
		"skipped_unchecked":     skippedUnchecked,
		"removed_ids_sample":    sampleInts(removedIDs, maxResultIDSamples),
		"removed_ids_truncated": len(removedIDs) > maxResultIDSamples,
	}
	if len(failures) > 0 {
		out["status"] = "partial"
		if hasFailureStatus(failures, "outcome_unknown") {
			out["status"] = "partial_outcome_unknown"
		}
		out["failures"] = failures
		return jsonErrorResult(out)
	}
	return jsonResult(out)
}

// ---- check_shopping_items ----

type checkShoppingInput struct {
	IDs     []int `json:"ids" jsonschema:"shopping list entry ids to update (from get_shopping_list)"`
	Checked *bool `json:"checked,omitempty" jsonschema:"mark bought (true, default) or not bought (false)"`
}

// checkShoppingItems checks or unchecks many entries in one call via Tandoor's
// bulk endpoint. To uncheck everything, read get_shopping_list with
// include_checked=true, then pass all ids with checked=false.
func (h *handlers) checkShoppingItems(ctx context.Context, _ *mcp.CallToolRequest, in checkShoppingInput) (*mcp.CallToolResult, any, error) {
	if len(in.IDs) == 0 {
		return nil, nil, fmt.Errorf("ids is required")
	}
	for _, id := range in.IDs {
		if err := validatePositiveID("shopping list entry id", id); err != nil {
			return nil, nil, err
		}
	}
	checked := in.Checked == nil || *in.Checked
	body := map[string]any{"ids": in.IDs, "checked": checked}
	if _, err := h.c.Do(ctx, http.MethodPost, "shopping-list-entry/bulk/", nil, body); err != nil {
		return nil, nil, err
	}
	return jsonResult(map[string]any{
		"status":        "updated",
		"count":         len(in.IDs),
		"checked":       checked,
		"ids_sample":    sampleInts(in.IDs, maxResultIDSamples),
		"ids_truncated": len(in.IDs) > maxResultIDSamples,
	})
}
