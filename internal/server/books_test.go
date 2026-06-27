package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAddRecipeToBookExistingBook(t *testing.T) {
	var entryBody map[string]any
	bookCreated := false
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/recipe-book/":
			_, _ = io.WriteString(w, `{"results":[{"id":3,"name":"Favorites"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/recipe-book/":
			bookCreated = true
			_, _ = io.WriteString(w, `{"id":99,"name":"Favorites"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/recipe-book-entry/":
			_, _ = io.WriteString(w, `{"results":[]}`) // not yet a member
		case r.Method == http.MethodPost && r.URL.Path == "/api/recipe-book-entry/":
			entryBody = decodeBody(t, r)
			_, _ = io.WriteString(w, `{"id":11,"book":3,"recipe":5}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	res, _, err := h.addRecipeToBook(context.Background(), nil, addRecipeToBookInput{Recipe: "5", Book: "Favorites"})
	if err != nil {
		t.Fatalf("addRecipeToBook: %v", err)
	}
	if bookCreated {
		t.Error("must not create a book that already exists")
	}
	if at(t, entryBody, "book") != 3.0 || at(t, entryBody, "recipe") != 5.0 {
		t.Errorf("entry body = %v, want book 3 recipe 5", entryBody)
	}
	if !strings.Contains(resultText(t, res), `"status": "added"`) {
		t.Errorf("result = %s", resultText(t, res))
	}
}

func TestAddRecipeToBookCreatesMissingBook(t *testing.T) {
	var createBody, entryBody map[string]any
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/recipe-book/":
			_, _ = io.WriteString(w, `{"results":[]}`) // book does not exist yet
		case r.Method == http.MethodPost && r.URL.Path == "/api/recipe-book/":
			createBody = decodeBody(t, r)
			_, _ = io.WriteString(w, `{"id":7,"name":"Weeknight"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/recipe-book-entry/":
			_, _ = io.WriteString(w, `{"results":[]}`) // not yet a member
		case r.Method == http.MethodPost && r.URL.Path == "/api/recipe-book-entry/":
			entryBody = decodeBody(t, r)
			_, _ = io.WriteString(w, `{"id":12}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	_, _, err := h.addRecipeToBook(context.Background(), nil, addRecipeToBookInput{Recipe: "5", Book: "Weeknight"})
	if err != nil {
		t.Fatalf("addRecipeToBook: %v", err)
	}
	if at(t, createBody, "name") != "Weeknight" {
		t.Errorf("create body = %v, want name=Weeknight", createBody)
	}
	// recipe-book requires `shared`; it must be present (empty) or Tandoor 400s.
	if _, ok := createBody["shared"]; !ok {
		t.Errorf("create body missing required `shared` field: %v", createBody)
	}
	if at(t, entryBody, "book") != 7.0 {
		t.Errorf("entry book = %v, want the created book id 7", at(t, entryBody, "book"))
	}
}

func TestAddRecipeToBookAlreadyMemberIsIdempotent(t *testing.T) {
	entryPosted := false
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/recipe-book/":
			_, _ = io.WriteString(w, `{"results":[{"id":3,"name":"Favorites"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/recipe-book-entry/":
			_, _ = io.WriteString(w, `{"results":[{"id":11,"book":3,"recipe":5}]}`) // already a member
		case r.Method == http.MethodPost && r.URL.Path == "/api/recipe-book-entry/":
			entryPosted = true // would 400 on a real instance (unique recipe+book)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	res, _, err := h.addRecipeToBook(context.Background(), nil, addRecipeToBookInput{Recipe: "5", Book: "Favorites"})
	if err != nil {
		t.Fatalf("addRecipeToBook: %v", err)
	}
	if entryPosted {
		t.Error("must not POST a duplicate membership when already a member")
	}
	if !strings.Contains(resultText(t, res), `"status": "already_in_book"`) {
		t.Errorf("result = %s", resultText(t, res))
	}
}

func TestRemoveRecipeFromBookDeletesEntry(t *testing.T) {
	var listQuery, deletePath string
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/recipe-book-entry/":
			listQuery = r.URL.RawQuery
			_, _ = io.WriteString(w, `{"results":[{"id":11,"book":3,"recipe":5}]}`)
		case r.Method == http.MethodDelete:
			deletePath = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	// Book "3" is numeric, so it resolves without a lookup request.
	res, _, err := h.removeRecipeFromBook(context.Background(), nil, removeRecipeFromBookInput{Recipe: "5", Book: "3"})
	if err != nil {
		t.Fatalf("removeRecipeFromBook: %v", err)
	}
	if !strings.Contains(listQuery, "recipe=5") || !strings.Contains(listQuery, "book=3") {
		t.Errorf("list query = %q, want recipe=5 & book=3", listQuery)
	}
	if deletePath != "/api/recipe-book-entry/11/" {
		t.Errorf("delete path = %q, want /api/recipe-book-entry/11/", deletePath)
	}
	if !strings.Contains(resultText(t, res), `"status": "removed"`) {
		t.Errorf("result = %s", resultText(t, res))
	}
}

func TestRemoveRecipeFromBookNotMember(t *testing.T) {
	deleted := false
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/recipe-book-entry/":
			_, _ = io.WriteString(w, `{"results":[]}`)
		case r.Method == http.MethodDelete:
			deleted = true
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	res, _, err := h.removeRecipeFromBook(context.Background(), nil, removeRecipeFromBookInput{Recipe: "5", Book: "3"})
	if err != nil {
		t.Fatalf("removeRecipeFromBook: %v", err)
	}
	if deleted {
		t.Error("must not DELETE when the recipe is not in the book")
	}
	if !strings.Contains(resultText(t, res), `"status": "not_in_book"`) {
		t.Errorf("result = %s", resultText(t, res))
	}
}

func TestListRecipeBooksAll(t *testing.T) {
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/recipe-book/" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"results":[{"id":3,"name":"Favorites","description":"best"},{"id":4,"name":"Quick"}]}`)
	})
	res, _, err := h.listRecipeBooks(context.Background(), nil, listRecipeBooksInput{})
	if err != nil {
		t.Fatalf("listRecipeBooks: %v", err)
	}
	var out listRecipeBooksOutput
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Books) != 2 || out.Books[0].Name != "Favorites" || out.Books[0].Description != "best" {
		t.Errorf("books = %+v", out.Books)
	}
}

func TestListRecipeBooksForRecipe(t *testing.T) {
	var query string
	h := newHandlersFunc(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/recipe-book-entry/" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		query = r.URL.RawQuery
		_, _ = io.WriteString(w, `{"results":[{"id":11,"book":3,"book_content":{"id":3,"name":"Favorites","description":"best"}}]}`)
	})
	res, _, err := h.listRecipeBooks(context.Background(), nil, listRecipeBooksInput{Recipe: "5"})
	if err != nil {
		t.Fatalf("listRecipeBooks: %v", err)
	}
	if !strings.Contains(query, "recipe=5") {
		t.Errorf("query = %q, want recipe=5", query)
	}
	var out listRecipeBooksOutput
	if err := json.Unmarshal([]byte(resultText(t, res)), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Books) != 1 || out.Books[0].Name != "Favorites" {
		t.Errorf("books = %+v", out.Books)
	}
}
