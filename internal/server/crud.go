package server

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxGenericPageSize = 200

var genericMutationResources = map[string]bool{
	"keyword":                       true,
	"food":                          true,
	"unit":                          true,
	"unit-conversion":               true,
	"property":                      true,
	"property-type":                 true,
	"meal-type":                     true,
	"recipe-book":                   true,
	"shopping-list":                 true,
	"supermarket":                   true,
	"supermarket-category":          true,
	"supermarket-category-relation": true,
	"inventory-location":            true,
	"custom-filter":                 true,
}

// resolve validates a resource name against the catalog and restricted denylist.
func resolve(name string) (Resource, error) {
	name = strings.TrimSpace(name)
	if restrictedResources[name] {
		return Resource{}, fmt.Errorf("resource %q is restricted and not available through the generic tools", name)
	}
	r, ok := lookupResource(name)
	if !ok {
		return Resource{}, fmt.Errorf("unknown resource %q; call tandoor_resources for the catalog", name)
	}
	return r, nil
}

func resolveMutation(name string) (Resource, error) {
	r, err := resolve(name)
	if err != nil {
		return Resource{}, err
	}
	if !genericMutationResources[r.Name] {
		return Resource{}, fmt.Errorf("resource %q is read-only through generic mutation tools; use a designed tool or an audited mutation resource", r.Name)
	}
	return r, nil
}

// resourceCatalog returns the resource catalog.
func (h *handlers) resourceCatalog(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	visible := make([]Resource, 0, len(resources))
	for _, r := range resources {
		if !restrictedResources[r.Name] {
			visible = append(visible, r)
		}
	}
	return jsonResult(map[string]any{"resources": visible})
}

type listInput struct {
	Resource string            `json:"resource" jsonschema:"resource name from tandoor_resources (e.g. recipe, food, keyword, meal-plan, shopping-list-entry)"`
	Query    string            `json:"query,omitempty" jsonschema:"free-text search; applies to resources with a 'query' filter (recipe, food, keyword, ...)"`
	Page     *int              `json:"page,omitempty" jsonschema:"1-based page number"`
	PageSize *int              `json:"page_size,omitempty" jsonschema:"results per page (Tandoor default 50, max 200)"`
	Ordering string            `json:"ordering,omitempty" jsonschema:"order-by field; prefix with - for descending (e.g. -created_at)"`
	Filters  map[string]string `json:"filters,omitempty" jsonschema:"extra query parameters passed verbatim (e.g. {\"updatedon_gte\":\"2024-01-01\"})"`
}

// genericList lists/searches a resource collection.
func (h *handlers) genericList(ctx context.Context, _ *mcp.CallToolRequest, in listInput) (*mcp.CallToolResult, any, error) {
	r, err := resolve(in.Resource)
	if err != nil {
		return nil, nil, err
	}
	q := url.Values{}
	if in.Query != "" {
		query, err := validateBoundedString("query", in.Query, maxGenericQueryValueRunes, false)
		if err != nil {
			return nil, nil, err
		}
		q.Set("query", query)
	}
	if in.Page != nil {
		if *in.Page <= 0 {
			return nil, nil, fmt.Errorf("page must be positive")
		}
		q.Set("page", strconv.Itoa(*in.Page))
	}
	if in.PageSize != nil {
		if *in.PageSize <= 0 || *in.PageSize > maxGenericPageSize {
			return nil, nil, fmt.Errorf("page_size must be between 1 and %d", maxGenericPageSize)
		}
		q.Set("page_size", strconv.Itoa(*in.PageSize))
	}
	if in.Ordering != "" {
		if err := validateOrdering(in.Ordering); err != nil {
			return nil, nil, err
		}
		q.Set("ordering", in.Ordering)
	}
	for k, v := range in.Filters {
		key, err := validateGenericQueryKey("filter key", k)
		if err != nil {
			return nil, nil, err
		}
		if err := validateGenericQueryValue("filter value", v); err != nil {
			return nil, nil, err
		}
		q.Set(key, v)
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

// genericGet fetches one object by id.
func (h *handlers) genericGet(ctx context.Context, _ *mcp.CallToolRequest, in getInput) (*mcp.CallToolResult, any, error) {
	r, err := resolve(in.Resource)
	if err != nil {
		return nil, nil, err
	}
	object, err := objectPath(r, in.ID)
	if err != nil {
		return nil, nil, err
	}
	raw, err := h.c.Do(ctx, http.MethodGet, object, nil, nil)
	if err != nil {
		return nil, nil, err
	}
	return rawResult(raw)
}

type createInput struct {
	Resource string         `json:"resource" jsonschema:"resource name from tandoor_resources"`
	Data     map[string]any `json:"data" jsonschema:"object fields to set, matching the resource's Tandoor serializer"`
}

// genericCreate creates an object.
func (h *handlers) genericCreate(ctx context.Context, _ *mcp.CallToolRequest, in createInput) (*mcp.CallToolResult, any, error) {
	r, err := resolveMutation(in.Resource)
	if err != nil {
		return nil, nil, err
	}
	if len(in.Data) == 0 {
		return nil, nil, fmt.Errorf("data is required to create a %s", r.Name)
	}
	if err := validateGenericBody(in.Data); err != nil {
		return nil, nil, err
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

// genericUpdate updates an object (PATCH by default, PUT when Full).
func (h *handlers) genericUpdate(ctx context.Context, _ *mcp.CallToolRequest, in updateInput) (*mcp.CallToolResult, any, error) {
	r, err := resolveMutation(in.Resource)
	if err != nil {
		return nil, nil, err
	}
	if len(in.Data) == 0 {
		return nil, nil, fmt.Errorf("data is required to update a %s", r.Name)
	}
	if err := validateGenericBody(in.Data); err != nil {
		return nil, nil, err
	}
	object, err := objectPath(r, in.ID)
	if err != nil {
		return nil, nil, err
	}
	method := http.MethodPatch
	if in.Full {
		method = http.MethodPut
	}
	raw, err := h.c.Do(ctx, method, object, nil, in.Data)
	if err != nil {
		return nil, nil, err
	}
	return rawResult(raw)
}

type deleteInput struct {
	Resource string `json:"resource" jsonschema:"resource name from tandoor_resources"`
	ID       string `json:"id" jsonschema:"object id to delete"`
}

// genericDelete removes an object.
func (h *handlers) genericDelete(ctx context.Context, _ *mcp.CallToolRequest, in deleteInput) (*mcp.CallToolResult, any, error) {
	r, err := resolveMutation(in.Resource)
	if err != nil {
		return nil, nil, err
	}
	object, err := objectPath(r, in.ID)
	if err != nil {
		return nil, nil, err
	}
	if _, err := h.c.Do(ctx, http.MethodDelete, object, nil, nil); err != nil {
		return nil, nil, err
	}
	return jsonResult(map[string]any{"status": "deleted", "resource": r.Name, "id": in.ID})
}

type actionInput struct {
	Method        string            `json:"method" jsonschema:"HTTP method: GET, POST, PUT, PATCH or DELETE"`
	Path          string            `json:"path" jsonschema:"allowlisted endpoint path relative to /api/, with a trailing slash (e.g. 'recipe/5/related/', 'recipe/flat/', 'fdc-search/')"`
	Query         map[string]string `json:"query,omitempty" jsonschema:"query-string parameters with one value per key"`
	QueryParams   []queryParam      `json:"query_params,omitempty" jsonschema:"ordered query-string parameters, allowing repeated names"`
	Body          map[string]any    `json:"body,omitempty" jsonschema:"JSON object request body for POST/PUT/PATCH"`
	SendEmptyBody bool              `json:"send_empty_body,omitempty" jsonschema:"send an explicit empty JSON object when body is empty"`
}

type queryParam struct {
	Name  string `json:"name" jsonschema:"query parameter name"`
	Value string `json:"value" jsonschema:"query parameter value"`
}

// genericAction calls an arbitrary API endpoint.
func (h *handlers) genericAction(ctx context.Context, _ *mcp.CallToolRequest, in actionInput) (*mcp.CallToolResult, any, error) {
	method := strings.ToUpper(strings.TrimSpace(in.Method))
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return nil, nil, fmt.Errorf("unsupported method %q", in.Method)
	}
	cleanPath, err := validateActionPath(method, in.Path)
	if err != nil {
		return nil, nil, err
	}
	q := url.Values{}
	for k, v := range in.Query {
		key, err := validateGenericQueryKey("query key", k)
		if err != nil {
			return nil, nil, err
		}
		if err := validateGenericQueryValue("query value", v); err != nil {
			return nil, nil, err
		}
		q.Set(key, v)
	}
	for _, p := range in.QueryParams {
		name, err := validateGenericQueryKey("query parameter name", p.Name)
		if err != nil {
			return nil, nil, err
		}
		if err := validateGenericQueryValue("query parameter value", p.Value); err != nil {
			return nil, nil, err
		}
		q.Add(name, p.Value)
	}
	if err := validateGenericBody(in.Body); err != nil {
		return nil, nil, err
	}
	var body any
	if len(in.Body) > 0 {
		body = in.Body
	} else if in.SendEmptyBody {
		body = map[string]any{}
	}
	raw, err := h.c.Do(ctx, method, cleanPath, q, body)
	if err != nil {
		return nil, nil, err
	}
	return rawResult(raw)
}

// validateActionPath rejects empty paths, traversal, and every custom endpoint
// except the small audited allowlist below.
func validateActionPath(method, p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("path is required")
	}
	unescaped, err := url.PathUnescape(p)
	if err != nil {
		return "", fmt.Errorf("invalid path escape: %w", err)
	}
	clean := strings.Trim(unescaped, "/")
	if clean == "api" {
		clean = ""
	} else {
		clean = strings.TrimPrefix(clean, "api/")
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("path may not contain empty, '.' or '..' segments")
		}
	}
	canonical := strings.TrimPrefix(path.Clean("/"+clean), "/")
	if canonical == "." || canonical == "" {
		return "", fmt.Errorf("path is required")
	}
	segs := strings.Split(canonical, "/")
	for _, seg := range segs {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("path may not contain empty, '.' or '..' segments")
		}
		if _, err := validateBoundedString("path segment", seg, maxGenericQueryKeyRunes, false); err != nil {
			return "", err
		}
	}
	if !allowedActionPath(method, segs) {
		return "", fmt.Errorf("endpoint %q is not in the tandoor_action allowlist", canonical)
	}
	return canonical + "/", nil
}

func allowedActionPath(method string, segs []string) bool {
	if method == http.MethodGet && len(segs) == 3 && segs[0] == "recipe" && segs[2] == "related" {
		_, err := validatePositiveIDString("recipe id", segs[1])
		return err == nil
	}
	if method == http.MethodGet && len(segs) == 2 && segs[0] == "recipe" && segs[1] == "flat" {
		return true
	}
	if method == http.MethodPost && len(segs) == 2 && segs[0] == "ingredient-parser" && segs[1] == "post" {
		return true
	}
	if method == http.MethodGet && len(segs) == 1 && segs[0] == "fdc-search" {
		return true
	}
	if method == http.MethodGet && len(segs) == 2 && segs[0] == "server-settings" && segs[1] == "current" {
		return true
	}
	return false
}

// objectPath builds the detail route for an object, e.g. "recipe/5/".
func objectPath(r Resource, id string) (string, error) {
	cleanID, err := validatePositiveIDString("id", id)
	if err != nil {
		return "", err
	}
	return r.Path + "/" + url.PathEscape(cleanID) + "/", nil
}
