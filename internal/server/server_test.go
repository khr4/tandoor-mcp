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

func TestGenericListBuildsPathAndQuery(t *testing.T) {
	rec := &recorder{reply: `{"count":1,"results":[{"id":1}]}`}
	h := newHandlers(t, rec)
	page, size := 2, 25
	res, _, err := h.genericList(context.Background(), nil, listInput{
		Resource: "recipe-book-entry",
		Query:    "x",
		Page:     &page,
		PageSize: &size,
		Ordering: "-id",
		Filters:  map[string]string{"book": "3"},
	})
	if err != nil {
		t.Fatalf("genericList: %v", err)
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
	if _, _, err := h.genericList(context.Background(), nil, listInput{Resource: "nope"}); err == nil {
		t.Error("expected error for unknown resource")
	}
}

func TestGenericGetPath(t *testing.T) {
	rec := &recorder{}
	h := newHandlers(t, rec)
	if _, _, err := h.genericGet(context.Background(), nil, getInput{Resource: "recipe", ID: "42"}); err != nil {
		t.Fatalf("genericGet: %v", err)
	}
	if rec.path != "/api/recipe/42/" {
		t.Errorf("path = %q, want /api/recipe/42/", rec.path)
	}
}

func TestGenericCreateRequiresData(t *testing.T) {
	h := newHandlers(t, &recorder{})
	if _, _, err := h.genericCreate(context.Background(), nil, createInput{Resource: "keyword"}); err == nil {
		t.Error("expected error for empty data")
	}
}

func TestGenericCreatePostsData(t *testing.T) {
	rec := &recorder{}
	h := newHandlers(t, rec)
	_, _, err := h.genericCreate(context.Background(), nil, createInput{Resource: "keyword", Data: map[string]any{"name": "Vegan"}})
	if err != nil {
		t.Fatalf("genericCreate: %v", err)
	}
	if rec.method != http.MethodPost || rec.path != "/api/keyword/" {
		t.Errorf("got %s %s, want POST /api/keyword/", rec.method, rec.path)
	}
	if !strings.Contains(rec.body, `"name":"Vegan"`) {
		t.Errorf("body = %q, want name=Vegan", rec.body)
	}
}

func TestGenericUpdateMethodSwitches(t *testing.T) {
	rec := &recorder{}
	h := newHandlers(t, rec)
	data := map[string]any{"name": "x"}
	if _, _, err := h.genericUpdate(context.Background(), nil, updateInput{Resource: "food", ID: "5", Data: data}); err != nil {
		t.Fatalf("genericUpdate: %v", err)
	}
	if rec.method != http.MethodPatch {
		t.Errorf("partial update method = %s, want PATCH", rec.method)
	}
	if _, _, err := h.genericUpdate(context.Background(), nil, updateInput{Resource: "food", ID: "5", Data: data, Full: true}); err != nil {
		t.Fatalf("genericUpdate full: %v", err)
	}
	if rec.method != http.MethodPut {
		t.Errorf("full update method = %s, want PUT", rec.method)
	}
}

func TestGenericDeleteConfirms(t *testing.T) {
	rec := &recorder{status: http.StatusNoContent}
	h := newHandlers(t, rec)
	res, _, err := h.genericDelete(context.Background(), nil, deleteInput{Resource: "unit", ID: "9"})
	if err != nil {
		t.Fatalf("genericDelete: %v", err)
	}
	if rec.method != http.MethodDelete || rec.path != "/api/unit/9/" {
		t.Errorf("got %s %s, want DELETE /api/unit/9/", rec.method, rec.path)
	}
	if !strings.Contains(resultText(t, res), `"status": "deleted"`) {
		t.Errorf("result = %q", resultText(t, res))
	}
}

func TestGenericToolsDenySensitiveResources(t *testing.T) {
	h := newHandlers(t, &recorder{reply: `{"token":"secret"}`})
	for _, res := range []string{"access-token", "storage", "ai-provider", "connector-config", "invite-link"} {
		if _, _, err := h.genericGet(context.Background(), nil, getInput{Resource: res, ID: "1"}); err == nil {
			t.Errorf("genericGet(%s) should be denied", res)
		}
		if _, _, err := h.genericList(context.Background(), nil, listInput{Resource: res}); err == nil {
			t.Errorf("genericList(%s) should be denied", res)
		}
	}
}

func TestActionValidatesMethodAndPath(t *testing.T) {
	h := newHandlers(t, &recorder{})
	if _, _, err := h.genericAction(context.Background(), nil, actionInput{Method: "FETCH", Path: "x/"}); err == nil {
		t.Error("expected error for bad method")
	}
	if _, _, err := h.genericAction(context.Background(), nil, actionInput{Method: "GET", Path: "../../admin/"}); err == nil {
		t.Error("expected error for path traversal")
	}
	if _, _, err := h.genericAction(context.Background(), nil, actionInput{Method: "GET", Path: "access-token/"}); err == nil {
		t.Error("expected error for sensitive endpoint")
	}
}

func TestActionCalls(t *testing.T) {
	rec := &recorder{reply: `[{"id":1}]`}
	h := newHandlers(t, rec)
	res, _, err := h.genericAction(context.Background(), nil, actionInput{
		Method: "get",
		Path:   "switch-active-space/2/",
		Query:  map[string]string{"verbose": "1"},
	})
	if err != nil {
		t.Fatalf("genericAction: %v", err)
	}
	if rec.method != http.MethodGet || rec.path != "/api/switch-active-space/2/" {
		t.Errorf("got %s %s", rec.method, rec.path)
	}
	if !strings.Contains(rec.query, "verbose=1") {
		t.Errorf("query = %q", rec.query)
	}
	if !strings.Contains(resultText(t, res), `"id": 1`) {
		t.Errorf("result = %q", resultText(t, res))
	}
}

func TestCreateRecipeRequiresName(t *testing.T) {
	h := newHandlers(t, &recorder{})
	if _, _, err := h.createRecipe(context.Background(), nil, createRecipeInput{}); err == nil {
		t.Error("expected error when name is empty")
	}
}

func TestImportRequiresURL(t *testing.T) {
	h := newHandlers(t, &recorder{})
	if _, _, err := h.importRecipeFromURL(context.Background(), nil, importRecipeInput{}); err == nil {
		t.Error("expected error when url is empty")
	}
}

func TestTaxonomyMergeAndMovePaths(t *testing.T) {
	rec := &recorder{}
	h := newHandlers(t, rec)
	if _, _, err := h.mergeTaxonomy(context.Background(), nil, mergeTaxonomyInput{Kind: "keyword", Source: "1", Target: "2"}); err != nil {
		t.Fatalf("mergeTaxonomy: %v", err)
	}
	if rec.method != http.MethodPut || rec.path != "/api/keyword/1/merge/2/" {
		t.Errorf("got %s %s, want PUT /api/keyword/1/merge/2/", rec.method, rec.path)
	}
	if _, _, err := h.moveTaxonomy(context.Background(), nil, moveTaxonomyInput{Kind: "food", Item: "8"}); err != nil {
		t.Fatalf("moveTaxonomy: %v", err)
	}
	if rec.path != "/api/food/8/move/0/" {
		t.Errorf("path = %q, want /api/food/8/move/0/", rec.path)
	}
	if _, _, err := h.moveTaxonomy(context.Background(), nil, moveTaxonomyInput{Kind: "unit", Item: "1"}); err == nil {
		t.Error("move_taxonomy should reject kind=unit")
	}
}

func TestResourcesCatalog(t *testing.T) {
	h := newHandlers(t, &recorder{})
	res, _, err := h.resourceCatalog(context.Background(), nil, struct{}{})
	if err != nil {
		t.Fatalf("resourceCatalog: %v", err)
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
	_ = New(c, Options{}) // panics on any bad tool schema
}
