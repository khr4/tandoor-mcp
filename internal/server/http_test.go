package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/khr4/tandoor-mcp/internal/tandoor"
)

// bearerRT injects a static bearer token on every request, mimicking an MCP
// client configured with a token.
type bearerRT struct {
	token string
	base  http.RoundTripper
}

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return b.base.RoundTrip(r)
}

func bearerClient(token string) *http.Client {
	return &http.Client{Transport: bearerRT{token: token, base: http.DefaultTransport}}
}

// mcpServer builds a real MCP server whose tools call the given Tandoor backend
// (an httptest server). backend may be nil for tests that never call a tool.
func mcpServer(t *testing.T, backend http.HandlerFunc) *mcp.Server {
	t.Helper()
	url := "http://127.0.0.1:1"
	if backend != nil {
		tb := httptest.NewServer(backend)
		t.Cleanup(tb.Close)
		url = tb.URL
	}
	c, err := tandoor.New(tandoor.Config{BaseURL: url, Token: "x"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return New(c, Options{})
}

func TestHTTPAuthGate(t *testing.T) {
	front := httptest.NewServer(NewHTTPHandler(mcpServer(t, nil), "secret"))
	t.Cleanup(front.Close)

	// /healthz is unauthenticated.
	resp, err := http.Get(front.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz status = %d, want 200", resp.StatusCode)
	}

	// /mcp without a token is rejected with a challenge.
	noauth, err := http.Post(front.URL+"/mcp", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	noauth.Body.Close()
	if noauth.StatusCode != http.StatusUnauthorized {
		t.Errorf("no-auth /mcp = %d, want 401", noauth.StatusCode)
	}
	if noauth.Header.Get("WWW-Authenticate") == "" {
		t.Error("401 is missing a WWW-Authenticate challenge")
	}

	// A wrong token is rejected.
	req, _ := http.NewRequest(http.MethodPost, front.URL+"/mcp", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer wrong")
	req.Header.Set("Content-Type", "application/json")
	bad, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong-token /mcp = %d, want 401", bad.StatusCode)
	}
}

func TestServeHTTPFailsClosed(t *testing.T) {
	// Both refusal paths return before any listener opens or anything is logged.
	srv := mcpServer(t, nil)

	// Non-loopback bind without a token must be refused before any listener opens.
	err := ServeHTTP(context.Background(), srv, HTTPOptions{Addr: "0.0.0.0:0", Token: ""})
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Errorf("non-loopback without token: err = %v, want a token-related refusal", err)
	}

	// A half-configured TLS pair is rejected.
	err = ServeHTTP(context.Background(), srv, HTTPOptions{Addr: "127.0.0.1:0", TLSCert: "cert.pem"})
	if err == nil || !strings.Contains(err.Error(), "TLS") {
		t.Errorf("TLS cert without key: err = %v, want a TLS pairing error", err)
	}
}

func TestIsLoopback(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:8080": true,
		"[::1]:8080":     true,
		"localhost:8080": true,
		"localhost":      true,
		":8080":          false,
		"0.0.0.0:8080":   false,
		"192.168.1.5:80": false,
	}
	for addr, want := range cases {
		if got := isLoopback(addr); got != want {
			t.Errorf("isLoopback(%q) = %v, want %v", addr, got, want)
		}
	}
}

// roundTripTools connects an MCP client over transport and asserts the full tool
// surface is reachable, then calls find_recipes end to end.
func roundTripTools(t *testing.T, transport mcp.Transport) {
	t.Helper()
	ctx := context.Background()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := map[string]bool{}
	for _, tl := range tools.Tools {
		names[tl.Name] = true
	}
	for _, want := range []string{"find_recipes", "get_recipe", "add_recipe_to_book", "check_shopping_items"} {
		if !names[want] {
			t.Errorf("tool %q missing over HTTP transport (have %d)", want, len(tools.Tools))
		}
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "find_recipes", Arguments: map[string]any{"text": "soup"}})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError || !strings.Contains(toolText(res), "Soup") {
		t.Errorf("find_recipes over HTTP = %q (isError=%v)", toolText(res), res.IsError)
	}
}

func recipeBackend(w http.ResponseWriter, _ *http.Request) {
	_, _ = io.WriteString(w, `{"count":1,"results":[{"id":9,"name":"Soup"}]}`)
}

func TestEndToEndStreamableOverHTTP(t *testing.T) {
	front := httptest.NewServer(NewHTTPHandler(mcpServer(t, recipeBackend), "secret"))
	t.Cleanup(front.Close)
	roundTripTools(t, &mcp.StreamableClientTransport{
		Endpoint:   front.URL + "/mcp",
		HTTPClient: bearerClient("secret"),
	})
}

func TestEndToEndSSEOverHTTP(t *testing.T) {
	front := httptest.NewServer(NewHTTPHandler(mcpServer(t, recipeBackend), "secret"))
	t.Cleanup(front.Close)
	roundTripTools(t, &mcp.SSEClientTransport{
		Endpoint:   front.URL + "/sse",
		HTTPClient: bearerClient("secret"),
	})
}

func TestEndToEndStreamableRejectsBadToken(t *testing.T) {
	front := httptest.NewServer(NewHTTPHandler(mcpServer(t, recipeBackend), "secret"))
	t.Cleanup(front.Close)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   front.URL + "/mcp",
		HTTPClient: bearerClient("wrong"),
	}
	_, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(context.Background(), transport, nil)
	if err == nil {
		t.Fatal("expected connect to fail with an invalid bearer token")
	}
}

// TestH2CCleartextRoundTrip drives the real serveListener path: it proves a
// cleartext HTTP/2 (h2c) request is served as HTTP/2, and that cancelling the
// context shuts the server down gracefully.
func TestH2CCleartextRoundTrip(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- serveListener(ctx, mcpServer(t, nil), HTTPOptions{Addr: ln.Addr().String(), Token: "secret"}, ln)
	}()

	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true) // force prior-knowledge h2c
	client := &http.Client{Transport: &http.Transport{Protocols: protocols}}

	resp, err := client.Get("http://" + ln.Addr().String() + "/healthz")
	if err != nil {
		t.Fatalf("h2c GET: %v", err)
	}
	resp.Body.Close()
	if resp.ProtoMajor != 2 {
		t.Errorf("ProtoMajor = %d, want 2 (cleartext HTTP/2 not negotiated)", resp.ProtoMajor)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz over h2c = %d, want 200", resp.StatusCode)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("serveListener returned %v on shutdown, want nil", err)
	}
}

// TestH2OverTLS proves HTTP/2 is negotiated over TLS via ALPN through the handler.
func TestH2OverTLS(t *testing.T) {
	s := httptest.NewUnstartedServer(NewHTTPHandler(mcpServer(t, nil), ""))
	s.EnableHTTP2 = true
	s.StartTLS()
	t.Cleanup(s.Close)

	resp, err := s.Client().Get(s.URL + "/healthz")
	if err != nil {
		t.Fatalf("TLS GET: %v", err)
	}
	resp.Body.Close()
	if resp.ProtoMajor != 2 {
		t.Errorf("ProtoMajor = %d, want 2 (h2 over TLS/ALPN)", resp.ProtoMajor)
	}
}
