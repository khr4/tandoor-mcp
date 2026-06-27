package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	front := httptest.NewServer(newHTTPHandler(mcpServer(t, nil), "secret"))
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
	err := Serve(context.Background(), srv, HTTPOptions{Addr: "0.0.0.0:0", Token: ""})
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Errorf("non-loopback without token: err = %v, want a token-related refusal", err)
	}

	// A half-configured TLS pair is rejected.
	err = Serve(context.Background(), srv, HTTPOptions{Addr: "127.0.0.1:0", TLSCert: "cert.pem"})
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
	front := httptest.NewServer(newHTTPHandler(mcpServer(t, recipeBackend), "secret"))
	t.Cleanup(front.Close)
	roundTripTools(t, &mcp.StreamableClientTransport{
		Endpoint:   front.URL + "/mcp",
		HTTPClient: bearerClient("secret"),
	})
}

func TestEndToEndSSEOverHTTP(t *testing.T) {
	front := httptest.NewServer(newHTTPHandler(mcpServer(t, recipeBackend), "secret"))
	t.Cleanup(front.Close)
	roundTripTools(t, &mcp.SSEClientTransport{
		Endpoint:   front.URL + "/sse",
		HTTPClient: bearerClient("secret"),
	})
}

func TestEndToEndStreamableRejectsBadToken(t *testing.T) {
	front := httptest.NewServer(newHTTPHandler(mcpServer(t, recipeBackend), "secret"))
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

// writeSelfSignedCert writes a throwaway cert/key pair valid for 127.0.0.1 and
// returns their paths, so tests can drive the real ServeTLS path.
func writeSelfSignedCert(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

// TestH2OverTLSServeListener drives the real serveListener ServeTLS path and
// proves HTTP/2 is negotiated over TLS via ALPN, then that shutdown is graceful.
func TestH2OverTLSServeListener(t *testing.T) {
	certPath, keyPath := writeSelfSignedCert(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- serveListener(ctx, mcpServer(t, nil), HTTPOptions{
			Addr: ln.Addr().String(), Token: "secret", TLSCert: certPath, TLSKey: keyPath,
		}, ln)
	}()

	// A client with a custom TLS config must also force HTTP/2 (same gotcha the
	// production client fix addresses) to negotiate h2 via ALPN.
	tr := &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // throwaway self-signed cert
		ForceAttemptHTTP2: true,
	}
	client := &http.Client{Transport: tr}
	resp, err := client.Get("https://" + ln.Addr().String() + "/healthz")
	if err != nil {
		t.Fatalf("TLS GET: %v", err)
	}
	resp.Body.Close()
	if resp.ProtoMajor != 2 {
		t.Errorf("ProtoMajor = %d, want 2 (h2 over TLS/ALPN, real ServeTLS path)", resp.ProtoMajor)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("serveListener returned %v on shutdown, want nil", err)
	}
}

// TestServeHappyPath covers Serve's loopback-no-token path end to end: it binds,
// logs the warning, serves, and shuts down cleanly on context cancel.
func TestServeHappyPath(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, mcpServer(t, nil), HTTPOptions{Addr: "127.0.0.1:0"}) }()
	// Give Serve a moment to bind, then cancel and require a clean return.
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve returned %v, want nil on shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return within 5s of cancel")
	}
}

// TestServeBindFailureSurfaces checks that a failed bind is surfaced (not
// swallowed) with the address named.
func TestServeBindFailureSurfaces(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	// Binding the already-taken loopback address must fail loudly.
	err = Serve(context.Background(), mcpServer(t, nil), HTTPOptions{Addr: ln.Addr().String()})
	if err == nil || !strings.Contains(err.Error(), ln.Addr().String()) {
		t.Errorf("Serve on a busy address: err = %v, want a listen error naming %s", err, ln.Addr().String())
	}
}

// TestRebindProtectionDisabledWithToken proves the reverse-proxy deployment
// works: a request arriving on loopback with a non-loopback Host header and a
// valid bearer is NOT rejected (403) by the SDK's DNS-rebinding guard.
func TestRebindProtectionDisabledWithToken(t *testing.T) {
	front := httptest.NewServer(newHTTPHandler(mcpServer(t, nil), "secret"))
	t.Cleanup(front.Close)
	req, _ := http.NewRequest(http.MethodPost, front.URL+"/mcp", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	req.Host = "recipes.example.com" // a same-host proxy would forward this
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		t.Errorf("got 403: DNS-rebinding guard still rejects a proxied Host with a valid token")
	}
}

// TestRebindProtectionOnWithoutToken keeps the guard for the unauthenticated
// loopback case: a non-loopback Host must be rejected.
func TestRebindProtectionOnWithoutToken(t *testing.T) {
	front := httptest.NewServer(newHTTPHandler(mcpServer(t, nil), ""))
	t.Cleanup(front.Close)
	req, _ := http.NewRequest(http.MethodPost, front.URL+"/mcp", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "evil.example.com"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (DNS-rebinding guard must stay on without a token)", resp.StatusCode)
	}
}

// TestBearerSchemeCaseInsensitive checks RFC 7235 case-insensitive scheme
// matching, while a wrong token is still rejected.
func TestBearerSchemeCaseInsensitive(t *testing.T) {
	front := httptest.NewServer(newHTTPHandler(mcpServer(t, nil), "secret"))
	t.Cleanup(front.Close)
	do := func(authz string) int {
		req, _ := http.NewRequest(http.MethodPost, front.URL+"/mcp", strings.NewReader(`{}`))
		req.Header.Set("Authorization", authz)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := do("bearer secret"); got == http.StatusUnauthorized {
		t.Error("lowercase \"bearer\" scheme was rejected; should match case-insensitively")
	}
	if got := do("Bearer nope"); got != http.StatusUnauthorized {
		t.Errorf("wrong token status = %d, want 401", got)
	}
}

// TestGracefulShutdownWithActiveSSEStream is the regression guard for the
// load-bearing BaseContext=ctx wiring: an in-flight SSE session must unwind on
// shutdown so Serve returns nil well before the 10s drain timeout.
func TestGracefulShutdownWithActiveSSEStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- serveListener(ctx, mcpServer(t, recipeBackend), HTTPOptions{Addr: ln.Addr().String(), Token: "secret"}, ln)
	}()

	// Establish a live SSE session and keep it open.
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(
		context.Background(),
		&mcp.SSEClientTransport{Endpoint: "http://" + ln.Addr().String() + "/sse", HTTPClient: bearerClient("secret")},
		nil,
	)
	if err != nil {
		t.Fatalf("connect SSE: %v", err)
	}
	if _, err := cs.ListTools(context.Background(), nil); err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	start := time.Now()
	cancel() // shut down with the SSE stream still connected
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serveListener returned %v with an active stream, want nil", err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("shutdown took %v (≈the %v drain timeout): the in-flight stream did not unwind via ctx", elapsed, httpShutdownTimeout)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("serveListener did not return within 8s; shutdown hung on the active stream")
	}
}
