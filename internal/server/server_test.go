package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/khr4/tandoor-mcp/internal/tandoor"
)

// recorder captures the last request a handler made and replies with a body.
type recorder struct {
	method, path, query, body string
	reply                     string
	status                    int
}

func newHandlers(t *testing.T, rec *recorder) *handlers {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rec.method, rec.path, rec.query, rec.body = r.Method, r.URL.Path, r.URL.RawQuery, string(b)
		if rec.status != 0 {
			w.WriteHeader(rec.status)
		}
		reply := rec.reply
		if reply == "" {
			reply = "{}"
		}
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	c, err := tandoor.New(tandoor.Config{BaseURL: srv.URL, Token: "tok"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return &handlers{c: c}
}

func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		t.Fatal("empty result")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content type = %T, want *mcp.TextContent", res.Content[0])
	}
	return tc.Text
}

func TestListBuildsPathAndQuery(t *testing.T) {
	rec := &recorder{reply: `{"count":1,"results":[{"id":1}]}`}
	h := newHandlers(t, rec)
	page, size := 2, 25
	res, _, err := h.List(context.Background(), nil, listInput{
		Resource: "recipe-book-entry",
		Query:    "x",
		Page:     &page,
		PageSize: &size,
		Ordering: "-id",
		Filters:  map[string]string{"book": "3"},
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if rec.method != http.MethodGet || rec.path != "/api/recipe-book-entry/" {
		t.Errorf("got %s %s, want GET /api/recipe-book-entry/", rec.method, rec.path)
	}
	for _, want := range []string{"query=x", "page=2", "page_size=25", "ordering=-id", "book=3"} {
		if !strings.Contains(rec.query, want) {
			t.Errorf("query %q missing %q", rec.query, want)
		}
	}
	if !strings.Contains(resultText(t, res), `"count": 1`) {
		t.Errorf("result not pretty-printed: %s", resultText(t, res))
	}
}

func TestUnknownResourceErrors(t *testing.T) {
	h := newHandlers(t, &recorder{})
	if _, _, err := h.List(context.Background(), nil, listInput{Resource: "nope"}); err == nil {
		t.Error("expected error for unknown resource")
	}
}

func TestGetPath(t *testing.T) {
	rec := &recorder{}
	h := newHandlers(t, rec)
	if _, _, err := h.Get(context.Background(), nil, getInput{Resource: "recipe", ID: "42"}); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.path != "/api/recipe/42/" {
		t.Errorf("path = %q, want /api/recipe/42/", rec.path)
	}
}

func TestCreateRequiresData(t *testing.T) {
	h := newHandlers(t, &recorder{})
	if _, _, err := h.Create(context.Background(), nil, createInput{Resource: "keyword"}); err == nil {
		t.Error("expected error for empty data")
	}
}

func TestCreatePostsData(t *testing.T) {
	rec := &recorder{}
	h := newHandlers(t, rec)
	_, _, err := h.Create(context.Background(), nil, createInput{Resource: "keyword", Data: map[string]any{"name": "Vegan"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if rec.method != http.MethodPost || rec.path != "/api/keyword/" {
		t.Errorf("got %s %s, want POST /api/keyword/", rec.method, rec.path)
	}
	if !strings.Contains(rec.body, `"name":"Vegan"`) {
		t.Errorf("body = %q, want name=Vegan", rec.body)
	}
}

func TestUpdateMethodSwitches(t *testing.T) {
	rec := &recorder{}
	h := newHandlers(t, rec)
	data := map[string]any{"name": "x"}
	if _, _, err := h.Update(context.Background(), nil, updateInput{Resource: "food", ID: "5", Data: data}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if rec.method != http.MethodPatch {
		t.Errorf("partial update method = %s, want PATCH", rec.method)
	}
	if _, _, err := h.Update(context.Background(), nil, updateInput{Resource: "food", ID: "5", Data: data, Full: true}); err != nil {
		t.Fatalf("Update full: %v", err)
	}
	if rec.method != http.MethodPut {
		t.Errorf("full update method = %s, want PUT", rec.method)
	}
}

func TestDeleteConfirms(t *testing.T) {
	rec := &recorder{status: http.StatusNoContent}
	h := newHandlers(t, rec)
	res, _, err := h.Delete(context.Background(), nil, deleteInput{Resource: "unit", ID: "9"})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if rec.method != http.MethodDelete || rec.path != "/api/unit/9/" {
		t.Errorf("got %s %s, want DELETE /api/unit/9/", rec.method, rec.path)
	}
	if !strings.Contains(resultText(t, res), "deleted unit 9") {
		t.Errorf("result = %q", resultText(t, res))
	}
}

func TestActionValidatesMethod(t *testing.T) {
	h := newHandlers(t, &recorder{})
	if _, _, err := h.Action(context.Background(), nil, actionInput{Method: "FETCH", Path: "x/"}); err == nil {
		t.Error("expected error for bad method")
	}
}

func TestActionCalls(t *testing.T) {
	rec := &recorder{reply: `[{"id":1}]`}
	h := newHandlers(t, rec)
	res, _, err := h.Action(context.Background(), nil, actionInput{
		Method: "get",
		Path:   "switch-active-space/2/",
		Query:  map[string]string{"verbose": "1"},
	})
	if err != nil {
		t.Fatalf("Action: %v", err)
	}
	if rec.method != http.MethodGet || rec.path != "/api/switch-active-space/2/" {
		t.Errorf("got %s %s", rec.method, rec.path)
	}
	if !strings.Contains(rec.query, "verbose=1") {
		t.Errorf("query = %q", rec.query)
	}
	// Array responses must pass through (no object-only output schema).
	if !strings.Contains(resultText(t, res), `"id": 1`) {
		t.Errorf("result = %q", resultText(t, res))
	}
}

func TestCreateRecipeRequiresName(t *testing.T) {
	h := newHandlers(t, &recorder{})
	if _, _, err := h.CreateRecipe(context.Background(), nil, createRecipeInput{}); err == nil {
		t.Error("expected error when name is empty")
	}
}

func TestImportRequiresURL(t *testing.T) {
	h := newHandlers(t, &recorder{})
	if _, _, err := h.ImportRecipeFromURL(context.Background(), nil, importRecipeInput{}); err == nil {
		t.Error("expected error when url is empty")
	}
}

func TestMergeAndMovePaths(t *testing.T) {
	rec := &recorder{}
	h := newHandlers(t, rec)
	if _, _, err := h.KeywordMerge(context.Background(), nil, mergeInput{ID: 1, Target: 2}); err != nil {
		t.Fatalf("KeywordMerge: %v", err)
	}
	if rec.method != http.MethodPut || rec.path != "/api/keyword/1/merge/2/" {
		t.Errorf("got %s %s, want PUT /api/keyword/1/merge/2/", rec.method, rec.path)
	}
	if _, _, err := h.FoodMove(context.Background(), nil, moveInput{ID: 8, Parent: 0}); err != nil {
		t.Fatalf("FoodMove: %v", err)
	}
	if rec.path != "/api/food/8/move/0/" {
		t.Errorf("path = %q, want /api/food/8/move/0/", rec.path)
	}
}

func TestResourcesCatalog(t *testing.T) {
	h := newHandlers(t, &recorder{})
	res, _, err := h.Resources(context.Background(), nil, struct{}{})
	if err != nil {
		t.Fatalf("Resources: %v", err)
	}
	text := resultText(t, res)
	for _, want := range []string{`"recipe"`, `"shopping-list-entry"`, `"meal-plan"`} {
		if !strings.Contains(text, want) {
			t.Errorf("catalog missing %s", want)
		}
	}
}

// TestRegisterAllTools ensures every tool registers without a schema-inference panic.
func TestRegisterAllTools(t *testing.T) {
	c, err := tandoor.New(tandoor.Config{BaseURL: "https://example.com", Token: "tok"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	srv := New(c) // panics on any bad tool schema
	if srv == nil {
		t.Fatal("New returned nil")
	}
}
