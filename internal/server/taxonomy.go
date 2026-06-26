package server

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- list_keywords / list_foods / list_units ----

type listNamedInput struct {
	Query string `json:"query,omitempty" jsonschema:"filter by name substring"`
	Limit *int   `json:"limit,omitempty" jsonschema:"maximum results (default 50)"`
}

func (h *handlers) listNamed(ctx context.Context, resourcePath string, in listNamedInput) (*mcp.CallToolResult, any, error) {
	q := url.Values{}
	addStr(q, "query", in.Query)
	limit := 50
	if in.Limit != nil && *in.Limit > 0 {
		limit = *in.Limit
	}
	q.Set("page_size", fmt.Sprintf("%d", limit))
	raw, err := h.c.Do(ctx, http.MethodGet, resourcePath+"/", q, nil)
	if err != nil {
		return nil, nil, err
	}
	var items []named
	if err := decodeList(raw, &items); err != nil {
		return nil, nil, fmt.Errorf("decoding %s: %w", resourcePath, err)
	}
	return jsonResult(items)
}

// ListKeywords lists keyword tags (id + name).
func (h *handlers) ListKeywords(ctx context.Context, _ *mcp.CallToolRequest, in listNamedInput) (*mcp.CallToolResult, any, error) {
	return h.listNamed(ctx, "keyword", in)
}

// ListFoods lists foods (id + name).
func (h *handlers) ListFoods(ctx context.Context, _ *mcp.CallToolRequest, in listNamedInput) (*mcp.CallToolResult, any, error) {
	return h.listNamed(ctx, "food", in)
}

// ListUnits lists measurement units (id + name).
func (h *handlers) ListUnits(ctx context.Context, _ *mcp.CallToolRequest, in listNamedInput) (*mcp.CallToolResult, any, error) {
	return h.listNamed(ctx, "unit", in)
}

// ---- merge / move ----

type mergeInput struct {
	ID     int `json:"id" jsonschema:"source object id (removed after the merge)"`
	Target int `json:"target" jsonschema:"target object id to merge into"`
}

type moveInput struct {
	ID     int `json:"id" jsonschema:"object id to move"`
	Parent int `json:"parent" jsonschema:"new parent id (0 = top level)"`
}

func (h *handlers) merge(ctx context.Context, resourcePath string, in mergeInput) (*mcp.CallToolResult, any, error) {
	raw, err := h.c.Do(ctx, http.MethodPut, fmt.Sprintf("%s/%d/merge/%d/", resourcePath, in.ID, in.Target), nil, nil)
	if err != nil {
		return nil, nil, err
	}
	return rawResult(raw)
}

func (h *handlers) move(ctx context.Context, resourcePath string, in moveInput) (*mcp.CallToolResult, any, error) {
	raw, err := h.c.Do(ctx, http.MethodPut, fmt.Sprintf("%s/%d/move/%d/", resourcePath, in.ID, in.Parent), nil, nil)
	if err != nil {
		return nil, nil, err
	}
	return rawResult(raw)
}

func (h *handlers) KeywordMerge(ctx context.Context, _ *mcp.CallToolRequest, in mergeInput) (*mcp.CallToolResult, any, error) {
	return h.merge(ctx, "keyword", in)
}

func (h *handlers) KeywordMove(ctx context.Context, _ *mcp.CallToolRequest, in moveInput) (*mcp.CallToolResult, any, error) {
	return h.move(ctx, "keyword", in)
}

func (h *handlers) FoodMerge(ctx context.Context, _ *mcp.CallToolRequest, in mergeInput) (*mcp.CallToolResult, any, error) {
	return h.merge(ctx, "food", in)
}

func (h *handlers) FoodMove(ctx context.Context, _ *mcp.CallToolRequest, in moveInput) (*mcp.CallToolResult, any, error) {
	return h.move(ctx, "food", in)
}

func (h *handlers) UnitMerge(ctx context.Context, _ *mcp.CallToolRequest, in mergeInput) (*mcp.CallToolResult, any, error) {
	return h.merge(ctx, "unit", in)
}
