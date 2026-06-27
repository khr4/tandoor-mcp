package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// apiBook is the {id, name, description} shape of a recipe book. apiBookEntry is
// a recipe↔book membership; book_content embeds the full book so a recipe's books
// can be listed without a second fetch.
type apiBook struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type apiBookEntry struct {
	ID          int     `json:"id"`
	Book        int     `json:"book"`
	BookContent apiBook `json:"book_content"`
}

type bookOut struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type listRecipeBooksOutput struct {
	Books     []bookOut `json:"books"`
	Truncated bool      `json:"truncated,omitempty"`
	Note      string    `json:"note,omitempty"`
}

// ---- add_recipe_to_book ----

type addRecipeToBookInput struct {
	Recipe string `json:"recipe" jsonschema:"recipe name or numeric id"`
	Book   string `json:"book" jsonschema:"recipe book name; created if it does not exist"`
}

// addRecipeToBook files a recipe into a book, creating the book by name if needed.
// The book is resolved by exact name first (recipe-book does not get-or-create on
// POST, so a blind create would duplicate it); the membership POST is idempotent.
func (h *handlers) addRecipeToBook(ctx context.Context, _ *mcp.CallToolRequest, in addRecipeToBookInput) (*mcp.CallToolResult, any, error) {
	recipeID, err := h.resolveRecipe(ctx, in.Recipe)
	if err != nil {
		return nil, nil, err
	}
	book := strings.TrimSpace(in.Book)
	if book == "" {
		return nil, nil, fmt.Errorf("book is required")
	}
	bookID, found, err := h.resolveUniqueExistingID(ctx, "recipe-book", "recipe book", book)
	if err != nil {
		return nil, nil, err
	}
	if !found {
		// recipe-book's serializer requires `shared` (the list of users to share
		// with); a new book is shared with nobody. Verified against a live instance:
		// POST {name} alone is rejected with {"shared":["This field is required."]}.
		raw, err := h.c.Do(ctx, http.MethodPost, "recipe-book/", nil, map[string]any{"name": book, "shared": []any{}})
		if err != nil {
			return nil, nil, err
		}
		var created apiBook
		if err := json.Unmarshal(raw, &created); err != nil {
			return nil, nil, fmt.Errorf("decoding created recipe book: %w", err)
		}
		bookID = created.ID
	}
	// recipe-book-entry enforces a unique (recipe, book) pair and 400s on a
	// duplicate POST — its serializer's get_or_create never runs because the
	// unique validator rejects first (verified live) — so check membership to
	// stay idempotent.
	if _, exists, err := h.bookEntryID(ctx, recipeID, bookID); err != nil {
		return nil, nil, err
	} else if exists {
		return jsonResult(map[string]any{"status": "already_in_book", "recipe_id": recipeID, "book_id": bookID, "book": book})
	}
	if _, err := h.c.Do(ctx, http.MethodPost, "recipe-book-entry/", nil, map[string]any{"book": bookID, "recipe": recipeID}); err != nil {
		return nil, nil, err
	}
	return jsonResult(map[string]any{"status": "added", "recipe_id": recipeID, "book_id": bookID, "book": book})
}

// bookEntryID returns the id of the recipe's membership in a book, if present.
func (h *handlers) bookEntryID(ctx context.Context, recipeID, bookID int) (int, bool, error) {
	q := url.Values{}
	q.Set("recipe", strconv.Itoa(recipeID))
	q.Set("book", strconv.Itoa(bookID))
	raw, err := h.c.Do(ctx, http.MethodGet, "recipe-book-entry/", q, nil)
	if err != nil {
		return 0, false, err
	}
	var entries []apiBookEntry
	if err := decodeList(raw, &entries); err != nil {
		return 0, false, fmt.Errorf("decoding recipe book entries: %w", err)
	}
	if len(entries) == 0 {
		return 0, false, nil
	}
	return entries[0].ID, true, nil
}

// ---- remove_recipe_from_book ----

type removeRecipeFromBookInput struct {
	Recipe string `json:"recipe" jsonschema:"recipe name or numeric id"`
	Book   string `json:"book" jsonschema:"recipe book name or numeric id"`
}

// removeRecipeFromBook deletes the recipe's membership in a book. If the recipe is
// not in the book it reports not_in_book rather than failing.
func (h *handlers) removeRecipeFromBook(ctx context.Context, _ *mcp.CallToolRequest, in removeRecipeFromBookInput) (*mcp.CallToolResult, any, error) {
	recipeID, err := h.resolveRecipe(ctx, in.Recipe)
	if err != nil {
		return nil, nil, err
	}
	bookID, err := h.resolveTaxonomyID(ctx, "recipe-book", in.Book)
	if err != nil {
		return nil, nil, err
	}
	entryID, found, err := h.bookEntryID(ctx, recipeID, bookID)
	if err != nil {
		return nil, nil, err
	}
	if !found {
		return jsonResult(map[string]any{"status": "not_in_book", "recipe_id": recipeID, "book_id": bookID})
	}
	if _, err := h.c.Do(ctx, http.MethodDelete, fmt.Sprintf("recipe-book-entry/%d/", entryID), nil, nil); err != nil {
		return nil, nil, err
	}
	return jsonResult(map[string]any{"status": "removed", "recipe_id": recipeID, "book_id": bookID})
}

// ---- list_recipe_books ----

type listRecipeBooksInput struct {
	Recipe string `json:"recipe,omitempty" jsonschema:"if set (name or id), list only the books this recipe is in"`
}

// listRecipeBooks lists all recipe books, or — when recipe is given — only the
// books that recipe belongs to.
func (h *handlers) listRecipeBooks(ctx context.Context, _ *mcp.CallToolRequest, in listRecipeBooksInput) (*mcp.CallToolResult, any, error) {
	if ref := strings.TrimSpace(in.Recipe); ref != "" {
		recipeID, err := h.resolveRecipe(ctx, ref)
		if err != nil {
			return nil, nil, err
		}
		q := url.Values{}
		q.Set("recipe", strconv.Itoa(recipeID))
		raw, err := h.c.Do(ctx, http.MethodGet, "recipe-book-entry/", q, nil)
		if err != nil {
			return nil, nil, err
		}
		var entries []apiBookEntry
		if err := decodeList(raw, &entries); err != nil {
			return nil, nil, fmt.Errorf("decoding recipe book entries: %w", err)
		}
		books := make([]bookOut, 0, len(entries))
		for _, e := range entries {
			books = append(books, bookOut{ID: e.BookContent.ID, Name: e.BookContent.Name, Description: e.BookContent.Description})
		}
		return jsonResult(listRecipeBooksOutput{Books: books})
	}

	q := url.Values{}
	q.Set("page_size", "200")
	raw, err := h.c.Do(ctx, http.MethodGet, "recipe-book/", q, nil)
	if err != nil {
		return nil, nil, err
	}
	var env listEnvelope
	paginated := json.Unmarshal(raw, &env) == nil && len(env.Results) > 0
	var all []apiBook
	if err := decodeList(raw, &all); err != nil {
		return nil, nil, fmt.Errorf("decoding recipe books: %w", err)
	}
	books := make([]bookOut, 0, len(all))
	for _, b := range all {
		books = append(books, bookOut(b))
	}
	out := listRecipeBooksOutput{Books: books}
	if paginated && env.Next != nil && *env.Next != "" {
		out.Truncated = true
		out.Note = "returned the first page of recipe books; use tandoor_list for explicit pagination"
	}
	return jsonResult(out)
}
