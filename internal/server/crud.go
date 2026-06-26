package server

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// resolve validates a resource name against the catalog.
func resolve(name string) (Resource, error) {
	r, ok := lookupResource(strings.TrimSpace(name))
	if !ok {
		return Resource{}, fmt.Errorf("unknown resource %q; call tandoor_resources for the catalog", name)
	}
	return r, nil
}

// Resources returns the resource catalog.
func (h *handlers) Resources(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	return jsonResult(resources)
}

type listInput struct {
	Resource string            `json:"resource" jsonschema:"resource name from tandoor_resources (e.g. recipe, food, keyword, meal-plan, shopping-list-entry)"`
	Query    string            `json:"query,omitempty" jsonschema:"free-text search; applies to resources with a 'query' filter (recipe, food, keyword, ...)"`
	Page     *int              `json:"page,omitempty" jsonschema:"1-based page number"`
	PageSize *int              `json:"page_size,omitempty" jsonschema:"results per page (Tandoor default 50, max 200)"`
	Ordering string            `json:"ordering,omitempty" jsonschema:"order-by field; prefix with - for descending (e.g. -created_at)"`
	Filters  map[string]string `json:"filters,omitempty" jsonschema:"extra query parameters passed verbatim (e.g. {\"updatedon_gte\":\"2024-01-01\"})"`
}

// List lists/searches a resource collection.
func (h *handlers) List(ctx context.Context, _ *mcp.CallToolRequest, in listInput) (*mcp.CallToolResult, any, error) {
	r, err := resolve(in.Resource)
	if err != nil {
		return nil, nil, err
	}
	q := url.Values{}
	if in.Query != "" {
		q.Set("query", in.Query)
	}
	if in.Page != nil {
		q.Set("page", strconv.Itoa(*in.Page))
	}
	if in.PageSize != nil {
		q.Set("page_size", strconv.Itoa(*in.PageSize))
	}
	if in.Ordering != "" {
		q.Set("ordering", in.Ordering)
	}
	for k, v := range in.Filters {
		q.Set(k, v)
	}
	raw, err := h.c.Do(ctx, http.MethodGet, r.Path+"/", q, nil)
	if err != nil {
		return nil, nil, err
	}
	return rawResult(raw)
}

type getInput struct {
	Resource string `json:"resource" jsonschema:"resource name from tandoor_resources"`
	ID       string `json:"id" jsonschema:"object id (primary key)"`
}

// Get fetches one object by id.
func (h *handlers) Get(ctx context.Context, _ *mcp.CallToolRequest, in getInput) (*mcp.CallToolResult, any, error) {
	r, err := resolve(in.Resource)
	if err != nil {
		return nil, nil, err
	}
	raw, err := h.c.Do(ctx, http.MethodGet, objectPath(r, in.ID), nil, nil)
	if err != nil {
		return nil, nil, err
	}
	return rawResult(raw)
}

type createInput struct {
	Resource string         `json:"resource" jsonschema:"resource name from tandoor_resources"`
	Data     map[string]any `json:"data" jsonschema:"object fields to set, matching the resource's Tandoor serializer"`
}

// Create creates an object.
func (h *handlers) Create(ctx context.Context, _ *mcp.CallToolRequest, in createInput) (*mcp.CallToolResult, any, error) {
	r, err := resolve(in.Resource)
	if err != nil {
		return nil, nil, err
	}
	if len(in.Data) == 0 {
		return nil, nil, fmt.Errorf("data is required to create a %s", r.Name)
	}
	raw, err := h.c.Do(ctx, http.MethodPost, r.Path+"/", nil, in.Data)
	if err != nil {
		return nil, nil, err
	}
	return rawResult(raw)
}

type updateInput struct {
	Resource string         `json:"resource" jsonschema:"resource name from tandoor_resources"`
	ID       string         `json:"id" jsonschema:"object id to update"`
	Data     map[string]any `json:"data" jsonschema:"fields to change"`
	Full     bool           `json:"full,omitempty" jsonschema:"replace the entire object with PUT instead of a partial PATCH"`
}

// Update updates an object (PATCH by default, PUT when Full).
func (h *handlers) Update(ctx context.Context, _ *mcp.CallToolRequest, in updateInput) (*mcp.CallToolResult, any, error) {
	r, err := resolve(in.Resource)
	if err != nil {
		return nil, nil, err
	}
	if len(in.Data) == 0 {
		return nil, nil, fmt.Errorf("data is required to update a %s", r.Name)
	}
	method := http.MethodPatch
	if in.Full {
		method = http.MethodPut
	}
	raw, err := h.c.Do(ctx, method, objectPath(r, in.ID), nil, in.Data)
	if err != nil {
		return nil, nil, err
	}
	return rawResult(raw)
}

type deleteInput struct {
	Resource string `json:"resource" jsonschema:"resource name from tandoor_resources"`
	ID       string `json:"id" jsonschema:"object id to delete"`
}

// Delete removes an object.
func (h *handlers) Delete(ctx context.Context, _ *mcp.CallToolRequest, in deleteInput) (*mcp.CallToolResult, any, error) {
	r, err := resolve(in.Resource)
	if err != nil {
		return nil, nil, err
	}
	if _, err := h.c.Do(ctx, http.MethodDelete, objectPath(r, in.ID), nil, nil); err != nil {
		return nil, nil, err
	}
	return textResult(fmt.Sprintf("deleted %s %s", r.Name, in.ID))
}

type actionInput struct {
	Method string            `json:"method" jsonschema:"HTTP method: GET, POST, PUT, PATCH or DELETE"`
	Path   string            `json:"path" jsonschema:"endpoint path relative to /api/, with a trailing slash (e.g. 'recipe/5/related/', 'fdc-search/', 'switch-active-space/2/')"`
	Query  map[string]string `json:"query,omitempty" jsonschema:"query-string parameters"`
	Body   map[string]any    `json:"body,omitempty" jsonschema:"JSON request body for POST/PUT/PATCH"`
}

// Action calls an arbitrary API endpoint.
func (h *handlers) Action(ctx context.Context, _ *mcp.CallToolRequest, in actionInput) (*mcp.CallToolResult, any, error) {
	method := strings.ToUpper(strings.TrimSpace(in.Method))
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return nil, nil, fmt.Errorf("unsupported method %q", in.Method)
	}
	if strings.TrimSpace(in.Path) == "" {
		return nil, nil, fmt.Errorf("path is required")
	}
	q := url.Values{}
	for k, v := range in.Query {
		q.Set(k, v)
	}
	var body any
	if len(in.Body) > 0 {
		body = in.Body
	}
	raw, err := h.c.Do(ctx, method, in.Path, q, body)
	if err != nil {
		return nil, nil, err
	}
	return rawResult(raw)
}

// objectPath builds the detail route for an object, e.g. "recipe/5/".
func objectPath(r Resource, id string) string {
	return r.Path + "/" + url.PathEscape(strings.TrimSpace(id)) + "/"
}
