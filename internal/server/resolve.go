package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/khr4/tandoor-mcp/internal/tandoor"
)

const (
	resolvePageCap  = 5   // max pages scanned when resolving a name to an id
	resolvePageSize = 100 // page size used while scanning
)

type namedPlural struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	PluralName string `json:"plural_name"`
}

func (n namedPlural) matches(name string) bool {
	return strings.EqualFold(n.Name, name) || (n.PluralName != "" && strings.EqualFold(n.PluralName, name))
}

// searchPage fetches one page of a resource's fuzzy search for name.
func (h *handlers) searchPage(ctx context.Context, resourcePath, name string, page int) (items []namedPlural, hasNext bool, err error) {
	q := url.Values{}
	q.Set("query", name)
	q.Set("page", strconv.Itoa(page))
	q.Set("page_size", strconv.Itoa(resolvePageSize))
	raw, err := h.c.Do(ctx, http.MethodGet, resourcePath+"/", q, nil)
	if err != nil {
		return nil, false, err
	}
	var env listEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, false, fmt.Errorf("decoding %s list: %w", resourcePath, err)
	}
	if err := json.Unmarshal(env.Results, &items); err != nil {
		return nil, false, fmt.Errorf("decoding %s results: %w", resourcePath, err)
	}
	hasNext = env.Next != nil && *env.Next != "" && len(items) > 0
	return items, hasNext, nil
}

// exactMatchIDs returns every object id whose name (or plural name) exactly
// matches, case-insensitively, paginating up to the page cap.
func (h *handlers) exactMatchIDs(ctx context.Context, resourcePath, name string) ([]int, error) {
	name = strings.TrimSpace(name)
	var ids []int
	if name == "" {
		return ids, nil
	}
	for page := 1; page <= resolvePageCap; page++ {
		items, hasNext, err := h.searchPage(ctx, resourcePath, name, page)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			if it.matches(name) {
				ids = append(ids, it.ID)
			}
		}
		if !hasNext {
			break
		}
	}
	return ids, nil
}

// resolveExistingID finds an object id by exact (case-insensitive) name match
// without creating anything. found is false if no such object exists.
func (h *handlers) resolveExistingID(ctx context.Context, resourcePath, name string) (id int, found bool, err error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, false, nil
	}
	for page := 1; page <= resolvePageCap; page++ {
		items, hasNext, err := h.searchPage(ctx, resourcePath, name, page)
		if err != nil {
			return 0, false, err
		}
		for _, it := range items {
			if it.matches(name) {
				return it.ID, true, nil
			}
		}
		if !hasNext {
			break
		}
	}
	return 0, false, nil
}

// getOrCreateID returns the id of an object with the given name, relying on
// Tandoor's server-side get-or-create-by-exact-name (POST {name}). If an
// instance instead rejects a duplicate name with 400/409, it falls back to a
// read so the common "already exists" case still resolves.
//
// This performs a write by design and must only be called from tools whose
// contract is "create if missing".
func (h *handlers) getOrCreateID(ctx context.Context, resourcePath, name string) (int, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, fmt.Errorf("name is required")
	}
	raw, err := h.c.Do(ctx, http.MethodPost, resourcePath+"/", nil, map[string]any{"name": name})
	if err != nil {
		var apiErr *tandoor.APIError
		if errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusBadRequest || apiErr.StatusCode == http.StatusConflict) {
			if id, found, e := h.resolveExistingID(ctx, resourcePath, name); e == nil && found {
				return id, nil
			}
		}
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

// resolveRecipe resolves a recipe reference that is either a numeric id or a
// name. A name matching more than one recipe is an error rather than a silent
// first-match pick, since the resolved id feeds destructive operations.
func (h *handlers) resolveRecipe(ctx context.Context, ref string) (int, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return 0, fmt.Errorf("a recipe (name or id) is required")
	}
	if id, err := strconv.Atoi(ref); err == nil {
		return id, nil
	}
	ids, err := h.exactMatchIDs(ctx, "recipe", ref)
	if err != nil {
		return 0, err
	}
	switch len(ids) {
	case 0:
		return 0, fmt.Errorf("recipe %q not found", ref)
	case 1:
		return ids[0], nil
	default:
		return 0, fmt.Errorf("recipe name %q is ambiguous; it matches ids %v — pass a specific id", ref, ids)
	}
}
