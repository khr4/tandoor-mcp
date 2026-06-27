package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/khr4/tandoor-mcp/internal/tandoor"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxListTaxonomyLimit = 200

func validKind(kind string, allowed ...string) (string, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case "keywords":
		kind = "keyword"
	case "foods":
		kind = "food"
	case "units":
		kind = "unit"
	}
	if slices.Contains(allowed, kind) {
		return kind, nil
	}
	return "", fmt.Errorf("kind must be one of %s, got %q", strings.Join(allowed, "/"), kind)
}

// resolveTaxonomyID resolves a name-or-id reference for a taxonomy resource
// without creating anything.
func (h *handlers) resolveTaxonomyID(ctx context.Context, kind, ref string) (int, error) {
	ref, err := cleanRef(kind+" reference", ref)
	if err != nil {
		return 0, err
	}
	if id, err := strconv.Atoi(ref); err == nil {
		if err := validatePositiveID(kind+" id", id); err != nil {
			return 0, err
		}
		return id, nil
	}
	id, found, err := h.resolveUniqueExistingID(ctx, kind, kind, ref)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, fmt.Errorf("%s %q not found — use list_taxonomy with kind=%q to see existing names", kind, ref, kind)
	}
	return id, nil
}

// ---- list_taxonomy ----

type listTaxonomyInput struct {
	Kind  string `json:"kind" jsonschema:"one of: keyword, food, unit (plural aliases keywords/foods/units are accepted)"`
	Query string `json:"query,omitempty" jsonschema:"filter by name substring"`
	Limit *int   `json:"limit,omitempty" jsonschema:"max results (default 50)"`
}

// listTaxonomy lists keywords, foods or units (id + name).
func (h *handlers) listTaxonomy(ctx context.Context, _ *mcp.CallToolRequest, in listTaxonomyInput) (*mcp.CallToolResult, any, error) {
	kind, err := validKind(in.Kind, "keyword", "food", "unit")
	if err != nil {
		return nil, nil, err
	}
	q := url.Values{}
	query, err := cleanOptionalName("query", in.Query)
	if err != nil {
		return nil, nil, err
	}
	addStr(q, "query", query)
	limit := 50
	if in.Limit != nil {
		if *in.Limit <= 0 || *in.Limit > maxListTaxonomyLimit {
			return nil, nil, fmt.Errorf("limit must be between 1 and %d", maxListTaxonomyLimit)
		}
		limit = *in.Limit
	}
	q.Set("page_size", strconv.Itoa(limit))
	raw, err := h.c.Do(ctx, http.MethodGet, kind+"/", q, nil)
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
	var items []named
	if err := decodeList(raw, &items); err != nil {
		return nil, nil, fmt.Errorf("decoding %s: %w", kind, err)
	}
	if count == 0 {
		count = len(items)
	}
	return jsonResult(map[string]any{
		"kind":      kind,
		"query":     query,
		"limit":     limit,
		"returned":  len(items),
		"count":     count,
		"truncated": truncated,
		"items":     items,
	})
}

// ---- create_taxonomy ----

type taxonomyItem struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	PluralName  string `json:"plural_name,omitempty"`
	Description string `json:"description,omitempty"`
	ParentID    int    `json:"parent_id,omitempty"`
}

type createTaxonomyInput struct {
	Kind        string `json:"kind" jsonschema:"one of: keyword, food, unit"`
	Name        string `json:"name" jsonschema:"new keyword/food/unit name"`
	Parent      string `json:"parent,omitempty" jsonschema:"parent keyword or food name/id; only valid for keyword and food"`
	PluralName  string `json:"plural_name,omitempty" jsonschema:"plural name; only valid for food and unit"`
	Description string `json:"description,omitempty" jsonschema:"description text"`
}

func (h *handlers) createTaxonomy(ctx context.Context, _ *mcp.CallToolRequest, in createTaxonomyInput) (*mcp.CallToolResult, any, error) {
	kind, err := validKind(in.Kind, "keyword", "food", "unit")
	if err != nil {
		return nil, nil, err
	}
	name, err := cleanName("name", in.Name)
	if err != nil {
		return nil, nil, err
	}
	body := map[string]any{"name": name}
	if desc, err := cleanOptionalFreeText("description", in.Description); err != nil {
		return nil, nil, err
	} else if desc != "" {
		body["description"] = desc
	}
	if plural, err := cleanOptionalName("plural_name", in.PluralName); err != nil {
		return nil, nil, err
	} else if plural != "" {
		if kind == "keyword" {
			return nil, nil, fmt.Errorf("plural_name is only valid for food and unit")
		}
		body["plural_name"] = plural
	}
	parentID := 0
	if strings.TrimSpace(in.Parent) != "" {
		if kind == "unit" {
			return nil, nil, fmt.Errorf("parent is only valid for keyword and food")
		}
		parentID, err = h.resolveTaxonomyID(ctx, kind, in.Parent)
		if err != nil {
			return nil, nil, err
		}
	}
	raw, err := h.c.Do(ctx, http.MethodPost, kind+"/", nil, body)
	if err != nil {
		return nil, nil, err
	}
	item, err := decodeTaxonomyItem(kind, raw)
	if err != nil {
		return nil, nil, err
	}
	if parentID > 0 {
		if _, err := h.c.Do(ctx, http.MethodPut, fmt.Sprintf("%s/%d/move/%d/", kind, item.ID, parentID), nil, nil); err != nil {
			out := map[string]any{"status": "partial", "kind": kind, "item": item, "phase": "move_parent", "parent_id": parentID, "failure": toolErrorObject(err)}
			return jsonErrorResult(out)
		}
		item.ParentID = parentID
	}
	return jsonResult(map[string]any{"status": "created", "kind": kind, "item": item})
}

// ---- rename_taxonomy ----

type renameTaxonomyInput struct {
	Kind        string  `json:"kind" jsonschema:"one of: keyword, food, unit"`
	Item        string  `json:"item" jsonschema:"keyword/food/unit name or id to rename/update"`
	Name        *string `json:"name,omitempty" jsonschema:"new name"`
	PluralName  *string `json:"plural_name,omitempty" jsonschema:"new plural name; only valid for food and unit"`
	Description *string `json:"description,omitempty" jsonschema:"new description"`
}

func (h *handlers) renameTaxonomy(ctx context.Context, _ *mcp.CallToolRequest, in renameTaxonomyInput) (*mcp.CallToolResult, any, error) {
	kind, err := validKind(in.Kind, "keyword", "food", "unit")
	if err != nil {
		return nil, nil, err
	}
	id, err := h.resolveTaxonomyID(ctx, kind, in.Item)
	if err != nil {
		return nil, nil, err
	}
	body := map[string]any{}
	if in.Name != nil {
		name, err := cleanName("name", *in.Name)
		if err != nil {
			return nil, nil, err
		}
		body["name"] = name
	}
	if in.PluralName != nil {
		if kind == "keyword" {
			return nil, nil, fmt.Errorf("plural_name is only valid for food and unit")
		}
		plural, err := cleanOptionalName("plural_name", *in.PluralName)
		if err != nil {
			return nil, nil, err
		}
		body["plural_name"] = plural
	}
	if in.Description != nil {
		desc, err := cleanOptionalFreeText("description", *in.Description)
		if err != nil {
			return nil, nil, err
		}
		body["description"] = desc
	}
	if len(body) == 0 {
		return nil, nil, fmt.Errorf("provide name, plural_name, and/or description")
	}
	raw, err := h.c.Do(ctx, http.MethodPatch, fmt.Sprintf("%s/%d/", kind, id), nil, body)
	if err != nil {
		return nil, nil, err
	}
	item, err := decodeTaxonomyItem(kind, raw)
	if err != nil {
		return nil, nil, err
	}
	return jsonResult(map[string]any{"status": "renamed", "kind": kind, "item": item})
}

func decodeTaxonomyItem(kind string, raw json.RawMessage) (taxonomyItem, error) {
	var item taxonomyItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return taxonomyItem{}, fmt.Errorf("decoding %s: %w", kind, err)
	}
	return item, nil
}

// ---- merge_taxonomy ----

type mergeTaxonomyInput struct {
	Kind   string `json:"kind" jsonschema:"one of: keyword, food, unit"`
	Source string `json:"source" jsonschema:"name or id to merge FROM (removed after merge)"`
	Target string `json:"target" jsonschema:"name or id to merge INTO"`
}

// mergeTaxonomy merges one keyword/food/unit into another.
func (h *handlers) mergeTaxonomy(ctx context.Context, _ *mcp.CallToolRequest, in mergeTaxonomyInput) (*mcp.CallToolResult, any, error) {
	kind, err := validKind(in.Kind, "keyword", "food", "unit")
	if err != nil {
		return nil, nil, err
	}
	src, err := h.resolveTaxonomyID(ctx, kind, in.Source)
	if err != nil {
		return nil, nil, err
	}
	tgt, err := h.resolveTaxonomyID(ctx, kind, in.Target)
	if err != nil {
		return nil, nil, err
	}
	if src == tgt {
		return nil, nil, fmt.Errorf("source and target are the same %s id %d", kind, src)
	}
	if _, err := h.c.Do(ctx, http.MethodPut, fmt.Sprintf("%s/%d/merge/%d/", kind, src, tgt), nil, nil); err != nil {
		var unknown *tandoor.OutcomeUnknownError
		if errors.As(err, &unknown) {
			if exists, lookupErr := h.taxonomyIDExists(ctx, kind, src); lookupErr == nil && !exists {
				return jsonResult(map[string]any{"status": "merged", "kind": kind, "source_id": src, "target_id": tgt, "verified_after_unknown": true})
			} else if lookupErr != nil {
				return outcomeUnknownResult(unknown, map[string]any{"operation": "merge_taxonomy", "kind": kind, "source_id": src, "target_id": tgt, "postcondition_error": lookupErr.Error()})
			}
			return outcomeUnknownResult(unknown, map[string]any{"operation": "merge_taxonomy", "kind": kind, "source_id": src, "target_id": tgt})
		}
		return nil, nil, err
	}
	return jsonResult(map[string]any{"status": "merged", "kind": kind, "source_id": src, "target_id": tgt})
}

// ---- move_taxonomy ----

type moveTaxonomyInput struct {
	Kind   string `json:"kind" jsonschema:"one of: keyword, food (units are not hierarchical)"`
	Item   string `json:"item" jsonschema:"name or id to move"`
	Parent string `json:"parent,omitempty" jsonschema:"new parent name or id; omit for top level"`
}

// moveTaxonomy re-parents a keyword or food in its tree.
func (h *handlers) moveTaxonomy(ctx context.Context, _ *mcp.CallToolRequest, in moveTaxonomyInput) (*mcp.CallToolResult, any, error) {
	kind, err := validKind(in.Kind, "keyword", "food")
	if err != nil {
		return nil, nil, err
	}
	item, err := h.resolveTaxonomyID(ctx, kind, in.Item)
	if err != nil {
		return nil, nil, err
	}
	parent := 0 // top level
	if strings.TrimSpace(in.Parent) != "" {
		parent, err = h.resolveTaxonomyID(ctx, kind, in.Parent)
		if err != nil {
			return nil, nil, err
		}
	}
	if item == parent {
		return nil, nil, fmt.Errorf("item and parent are the same %s id %d", kind, item)
	}
	if _, err := h.c.Do(ctx, http.MethodPut, fmt.Sprintf("%s/%d/move/%d/", kind, item, parent), nil, nil); err != nil {
		return nil, nil, err
	}
	return jsonResult(map[string]any{"status": "moved", "kind": kind, "item_id": item, "parent_id": parent})
}

func (h *handlers) taxonomyIDExists(ctx context.Context, kind string, id int) (bool, error) {
	if _, err := h.c.Do(ctx, http.MethodGet, fmt.Sprintf("%s/%d/", kind, id), nil, nil); err != nil {
		var apiErr *tandoor.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
