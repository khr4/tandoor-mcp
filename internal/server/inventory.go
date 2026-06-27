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

const maxInventoryLimit = 200

type getInventoryInput struct {
	Food         string `json:"food,omitempty" jsonschema:"food name or id to filter by"`
	Location     string `json:"location,omitempty" jsonschema:"inventory location name or id to filter by"`
	IncludeEmpty *bool  `json:"include_empty,omitempty" jsonschema:"include zero-amount entries (default false)"`
	Limit        *int   `json:"limit,omitempty" jsonschema:"maximum entries to return (default 50)"`
}

type apiInventoryEntry struct {
	ID                int     `json:"id"`
	InventoryLocation *named  `json:"inventory_location"`
	SubLocation       string  `json:"sub_location"`
	Code              string  `json:"code"`
	Food              *named  `json:"food"`
	Unit              *named  `json:"unit"`
	Amount            flexNum `json:"amount"`
	Expires           string  `json:"expires"`
	Note              string  `json:"note"`
	Label             string  `json:"label"`
	CreatedAt         string  `json:"created_at"`
}

type inventoryEntryOut struct {
	ID          int    `json:"id"`
	Label       string `json:"label,omitempty"`
	Code        string `json:"code,omitempty"`
	Food        string `json:"food,omitempty"`
	FoodID      int    `json:"food_id,omitempty"`
	Amount      string `json:"amount,omitempty"`
	Unit        string `json:"unit,omitempty"`
	UnitID      int    `json:"unit_id,omitempty"`
	Location    string `json:"location,omitempty"`
	LocationID  int    `json:"location_id,omitempty"`
	SubLocation string `json:"sub_location,omitempty"`
	Expires     string `json:"expires,omitempty"`
	Note        string `json:"note,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
}

func (h *handlers) getInventory(ctx context.Context, _ *mcp.CallToolRequest, in getInventoryInput) (*mcp.CallToolResult, any, error) {
	limit := 50
	if in.Limit != nil {
		if *in.Limit <= 0 || *in.Limit > maxInventoryLimit {
			return nil, nil, fmt.Errorf("limit must be between 1 and %d", maxInventoryLimit)
		}
		limit = *in.Limit
	}
	q := url.Values{}
	q.Set("page_size", strconv.Itoa(limit))
	foodID := 0
	if in.Food != "" {
		id, err := resolveFoodID(ctx, h, in.Food)
		if err != nil {
			return nil, nil, err
		}
		foodID = id
		q.Set("food_id", strconv.Itoa(id))
	}
	locationID := 0
	if in.Location != "" {
		id, err := h.resolveInventoryLocationID(ctx, in.Location)
		if err != nil {
			return nil, nil, err
		}
		locationID = id
		q.Set("inventory_location_id", strconv.Itoa(id))
	}
	if in.IncludeEmpty != nil && *in.IncludeEmpty {
		q.Set("empty", "true")
	}
	raw, err := h.c.Do(ctx, http.MethodGet, "inventory-entry/", q, nil)
	if err != nil {
		return nil, nil, err
	}
	count := 0
	truncated := false
	var env listEnvelope
	if err := json.Unmarshal(raw, &env); err == nil && len(env.Results) > 0 {
		count = env.Count
		truncated = env.Next != nil && *env.Next != ""
	}
	var entries []apiInventoryEntry
	if err := decodeList(raw, &entries); err != nil {
		return nil, nil, fmt.Errorf("decoding inventory entries: %w", err)
	}
	if count == 0 {
		count = len(entries)
	}
	out := make([]inventoryEntryOut, 0, len(entries))
	for _, entry := range entries {
		out = append(out, toInventoryEntryOut(entry))
	}
	return jsonResult(map[string]any{
		"entries":     out,
		"returned":    len(out),
		"count":       count,
		"limit":       limit,
		"truncated":   truncated,
		"food_id":     foodID,
		"location_id": locationID,
	})
}

func resolveFoodID(ctx context.Context, h *handlers, ref string) (int, error) {
	ref, err := cleanRef("food", ref)
	if err != nil {
		return 0, err
	}
	if id, err := strconv.Atoi(ref); err == nil {
		if err := validatePositiveID("food id", id); err != nil {
			return 0, err
		}
		return id, nil
	}
	id, found, err := h.resolveUniqueExistingID(ctx, "food", "food", ref)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, fmt.Errorf("food %q not found - use list_taxonomy with kind=\"food\" to see existing foods", ref)
	}
	return id, nil
}

func (h *handlers) resolveInventoryLocationID(ctx context.Context, ref string) (int, error) {
	ref, err := cleanRef("inventory location", ref)
	if err != nil {
		return 0, err
	}
	if id, err := strconv.Atoi(ref); err == nil {
		if err := validatePositiveID("inventory location id", id); err != nil {
			return 0, err
		}
		return id, nil
	}
	id, found, err := h.resolveUniqueExistingID(ctx, "inventory-location", "inventory location", ref)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, fmt.Errorf("inventory location %q not found - use tandoor_list with resource=\"inventory-location\" to see existing locations", ref)
	}
	return id, nil
}

func toInventoryEntryOut(entry apiInventoryEntry) inventoryEntryOut {
	out := inventoryEntryOut{
		ID:          entry.ID,
		Label:       entry.Label,
		Code:        entry.Code,
		Amount:      entry.Amount.String(),
		SubLocation: entry.SubLocation,
		Expires:     entry.Expires,
		Note:        entry.Note,
		CreatedAt:   entry.CreatedAt,
	}
	if entry.Food != nil {
		out.Food = entry.Food.Name
		out.FoodID = entry.Food.ID
	}
	if entry.Unit != nil {
		out.Unit = entry.Unit.Name
		out.UnitID = entry.Unit.ID
	}
	if entry.InventoryLocation != nil {
		out.Location = entry.InventoryLocation.Name
		out.LocationID = entry.InventoryLocation.ID
	}
	return out
}
