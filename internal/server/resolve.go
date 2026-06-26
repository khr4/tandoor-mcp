package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// resolveExistingID looks up an object id by exact (case-insensitive) name match
// without creating anything. found is false if no such object exists.
func (h *handlers) resolveExistingID(ctx context.Context, resourcePath, name string) (id int, found bool, err error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, false, nil
	}
	q := url.Values{}
	q.Set("query", name)
	q.Set("page_size", "30")
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
	return 0, false, nil
}

// getOrCreateID returns the id of an object with the given name, creating it if
// it does not already exist (Tandoor get-or-creates by name on these resources).
func (h *handlers) getOrCreateID(ctx context.Context, resourcePath, name string) (int, error) {
	if id, found, err := h.resolveExistingID(ctx, resourcePath, name); err != nil {
		return 0, err
	} else if found {
		return id, nil
	}
	raw, err := h.c.Do(ctx, http.MethodPost, resourcePath+"/", nil, map[string]any{"name": strings.TrimSpace(name)})
	if err != nil {
		return 0, err
	}
	var created named
	if err := json.Unmarshal(raw, &created); err != nil {
		return 0, fmt.Errorf("decoding created %s: %w", resourcePath, err)
	}
	return created.ID, nil
}

// resolveExistingIDs maps names to ids for filtering. Names with no match are
// returned in missing so callers can report them rather than silently dropping.
func (h *handlers) resolveExistingIDs(ctx context.Context, resourcePath string, names []string) (ids []int, missing []string, err error) {
	for _, n := range names {
		id, found, err := h.resolveExistingID(ctx, resourcePath, n)
		if err != nil {
			return nil, nil, err
		}
		if found {
			ids = append(ids, id)
		} else {
			missing = append(missing, n)
		}
	}
	return ids, missing, nil
}
