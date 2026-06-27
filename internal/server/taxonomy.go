package server

import (
	"context"
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
	Kind  string `json:"kind" jsonschema:"one of: keyword, food, unit"`
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
	var items []named
	if err := decodeList(raw, &items); err != nil {
		return nil, nil, fmt.Errorf("decoding %s: %w", kind, err)
	}
	return jsonResult(map[string]any{"kind": kind, "items": items})
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
