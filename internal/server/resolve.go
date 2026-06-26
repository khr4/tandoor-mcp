package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	resolvePageCap  = 5   // max pages scanned when resolving a name to an id
	resolvePageSize = 100 // page size used while scanning
)

// resolveExistingID finds an object id by exact (case-insensitive) name match
// without creating anything, paginating Tandoor's fuzzy search until the exact
// match is found or the result set is exhausted/capped. found is false if no
// such object exists.
func (h *handlers) resolveExistingID(ctx context.Context, resourcePath, name string) (id int, found bool, err error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, false, nil
	}
	for page := 1; page <= resolvePageCap; page++ {
		q := url.Values{}
		q.Set("query", name)
		q.Set("page", strconv.Itoa(page))
		q.Set("page_size", strconv.Itoa(resolvePageSize))
		raw, err := h.c.Do(ctx, http.MethodGet, resourcePath+"/", q, nil)
		if err != nil {
			return 0, false, err
		}
		var env listEnvelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return 0, false, fmt.Errorf("decoding %s list: %w", resourcePath, err)
		}
		var items []struct {
			ID         int    `json:"id"`
			Name       string `json:"name"`
			PluralName string `json:"plural_name"`
		}
		if err := json.Unmarshal(env.Results, &items); err != nil {
			return 0, false, fmt.Errorf("decoding %s results: %w", resourcePath, err)
		}
		for _, it := range items {
			if strings.EqualFold(it.Name, name) || (it.PluralName != "" && strings.EqualFold(it.PluralName, name)) {
				return it.ID, true, nil
			}
		}
		if len(items) == 0 || env.Next == nil || *env.Next == "" {
			break
		}
	}
	return 0, false, nil
}

// getOrCreateID returns the id of an object with the given name, relying on
// Tandoor's server-side get-or-create-by-exact-name (POST {name}) so neither a
// fuzzy-search miss nor a concurrent call can create a duplicate.
func (h *handlers) getOrCreateID(ctx context.Context, resourcePath, name string) (int, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, fmt.Errorf("name is required")
	}
	raw, err := h.c.Do(ctx, http.MethodPost, resourcePath+"/", nil, map[string]any{"name": name})
	if err != nil {
		return 0, err
	}
	var created named
	if err := json.Unmarshal(raw, &created); err != nil {
		return 0, fmt.Errorf("decoding created %s: %w", resourcePath, err)
	}
	return created.ID, nil
}

// resolveExistingIDs maps names to ids for filtering. A name that does not
// resolve — whether genuinely absent or because the lookup errored — becomes a
// warning rather than failing the whole operation.
func (h *handlers) resolveExistingIDs(ctx context.Context, resourcePath, kind string, names []string) (ids []int, warnings []string) {
	for _, n := range names {
		id, found, err := h.resolveExistingID(ctx, resourcePath, n)
		switch {
		case err != nil:
			warnings = append(warnings, fmt.Sprintf("could not look up %s %q: %v; ignored as a filter", kind, n, err))
		case found:
			ids = append(ids, id)
		default:
			warnings = append(warnings, fmt.Sprintf("%s %q not found; ignored as a filter", kind, n))
		}
	}
	return ids, warnings
}

// resolveRecipe resolves a recipe reference that is either a numeric id or a name.
func (h *handlers) resolveRecipe(ctx context.Context, ref string) (int, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return 0, fmt.Errorf("a recipe (name or id) is required")
	}
	if id, err := strconv.Atoi(ref); err == nil {
		return id, nil
	}
	id, found, err := h.resolveExistingID(ctx, "recipe", ref)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, fmt.Errorf("recipe %q not found", ref)
	}
	return id, nil
}
