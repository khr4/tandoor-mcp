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

// connect wires a real MCP client to the server over an in-memory transport,
// backed by an httptest Tandoor. This exercises input-schema inference,
// validation and dispatch end to end.
func connect(t *testing.T, backend http.HandlerFunc) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	srv := httptest.NewServer(backend)
	t.Cleanup(srv.Close)

	c, err := tandoor.New(tandoor.Config{BaseURL: srv.URL, Token: "tok"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := New(c, Options{}).Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestEndToEndListToolsAndCall(t *testing.T) {
	ctx := context.Background()
	var gotPath, gotQuery string
	cs := connect(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		_, _ = io.WriteString(w, `{"count":1,"results":[{"id":9,"name":"Soup"}]}`)
	})

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := map[string]bool{}
	for _, tl := range tools.Tools {
		names[tl.Name] = true
	}
	for _, want := range []string{
		"tandoor_resources", "tandoor_list", "tandoor_action",
		"find_recipes", "create_recipe", "import_recipe_from_url", "delete_recipe",
		"plan_meal", "get_shopping_list", "set_food_on_hand", "merge_taxonomy",
		"add_recipe_to_book", "remove_recipe_from_book", "list_recipe_books",
		"check_shopping_items",
	} {
		if !names[want] {
			t.Errorf("tool %q not registered (have %d tools)", want, len(tools.Tools))
		}
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "find_recipes",
		Arguments: map[string]any{"text": "soup", "limit": 5},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("call errored: %s", toolText(res))
	}
	if gotPath != "/api/recipe/" {
		t.Errorf("backend path = %q, want /api/recipe/", gotPath)
	}
	if !strings.Contains(gotQuery, "query=soup") || !strings.Contains(gotQuery, "page_size=5") {
		t.Errorf("backend query = %q", gotQuery)
	}
	if !strings.Contains(toolText(res), "Soup") {
		t.Errorf("result = %q", toolText(res))
	}
}

func TestEndToEndInputValidation(t *testing.T) {
	ctx := context.Background()
	cs := connect(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("backend should not be called when input is invalid")
	})
	// tandoor_get requires resource and id; omit both.
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "tandoor_get", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected IsError for missing required fields, got: %s", toolText(res))
	}
}

func TestEndToEndCheckShoppingItems(t *testing.T) {
	ctx := context.Background()
	var gotPath string
	var gotBody []byte
	cs := connect(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{}`)
	})
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name:      "check_shopping_items",
		Arguments: map[string]any{"ids": []int{4, 5}, "checked": false},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("call errored: %s", toolText(res))
	}
	if gotPath != "/api/shopping-list-entry/bulk/" {
		t.Errorf("backend path = %q, want /api/shopping-list-entry/bulk/", gotPath)
	}
	if !strings.Contains(string(gotBody), `"checked":false`) || !strings.Contains(string(gotBody), `"ids":[4,5]`) {
		t.Errorf("backend body = %s", gotBody)
	}
}

func toolText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
