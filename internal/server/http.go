package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// httpShutdownTimeout bounds the graceful drain of in-flight requests when the
// root context is cancelled.
const httpShutdownTimeout = 10 * time.Second

// HTTPOptions configures the network MCP transport (an alternative to stdio).
type HTTPOptions struct {
	// Addr is the listen address, e.g. ":8080" or "127.0.0.1:8080".
	Addr string
	// Token, when set, is the static bearer token every MCP client must present.
	// When empty, serving is permitted only on a loopback address (see ServeHTTP).
	Token string
	// TLSCert and TLSKey, when both set, enable HTTPS (and HTTP/2 via ALPN);
	// otherwise the server is cleartext with HTTP/2 cleartext (h2c) support.
	TLSCert string
	TLSKey  string
}

// NewHTTPHandler builds the MCP HTTP handler: the modern Streamable HTTP
// transport at /mcp (request/response plus server-sent-event streaming) and the
// legacy SSE transport at /sse, both backed by the same server and gated by a
// static bearer token when one is set, plus an unauthenticated /healthz probe.
func NewHTTPHandler(srv *mcp.Server, token string) http.Handler {
	getServer := func(*http.Request) *mcp.Server { return srv }
	streamable := mcp.NewStreamableHTTPHandler(getServer, nil)
	sse := mcp.NewSSEHandler(getServer, nil)

	mux := http.NewServeMux()
	mux.Handle("/mcp", protect(token, streamable))
	mux.Handle("/sse", protect(token, sse))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	return mux
}

// protect wraps h with a constant-time static-bearer check. An empty token
// leaves h unauthenticated (permitted only on loopback; enforced by ServeHTTP).
// The token is compared as a SHA-256 digest so neither its value nor its length
// leaks through timing.
func protect(token string, h http.Handler) http.Handler {
	if token == "" {
		return h
	}
	want := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		authz := r.Header.Get("Authorization")
		if !strings.HasPrefix(authz, prefix) {
			unauthorized(w)
			return
		}
		got := sha256.Sum256([]byte(strings.TrimPrefix(authz, prefix)))
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			unauthorized(w)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

// ServeHTTP serves the MCP HTTP transport on opts.Addr until ctx is cancelled,
// then drains connections within httpShutdownTimeout. It fails closed: a
// non-loopback bind without a token is refused, because the server proxies to
// Tandoor with the configured API token and an open endpoint grants full access.
func ServeHTTP(ctx context.Context, srv *mcp.Server, opts HTTPOptions) error {
	if (opts.TLSCert == "") != (opts.TLSKey == "") {
		return fmt.Errorf("TLS needs both TANDOOR_TLS_CERT and TANDOOR_TLS_KEY, or neither")
	}
	if opts.Token == "" && !isLoopback(opts.Addr) {
		return fmt.Errorf("refusing to serve MCP on non-loopback address %q without a token: set TANDOOR_MCP_TOKEN (an open endpoint grants full Tandoor access)", opts.Addr)
	}
	if opts.Token == "" {
		log.Printf("warning: serving MCP on %s without authentication (loopback only); set TANDOOR_MCP_TOKEN to require a bearer token", opts.Addr)
	}
	ln, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", opts.Addr, err)
	}
	return serveListener(ctx, srv, opts, ln)
}

// serveListener runs the HTTP server on an already-bound listener. Split out so
// tests can drive a real serve/shutdown cycle on an ephemeral port.
func serveListener(ctx context.Context, srv *mcp.Server, opts HTTPOptions, ln net.Listener) error {
	useTLS := opts.TLSCert != "" && opts.TLSKey != ""

	// Enable HTTP/1.1 and HTTP/2 on every listener. Over TLS, HTTP/2 is
	// negotiated via ALPN; for cleartext, UnencryptedHTTP2 (h2c) lets HTTP/1.1
	// and HTTP/2 share the port. (net/http's native support; no x/net needed.)
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	if !useTLS {
		protocols.SetUnencryptedHTTP2(true)
	}

	hs := &http.Server{
		Handler:   NewHTTPHandler(srv, opts.Token),
		Protocols: protocols,
		// Bound how long headers may take to arrive; deliberately no Read/Write
		// timeout, which would sever long-lived SSE streams.
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	log.Printf("MCP HTTP transport on %s (tls=%v, auth=%v); endpoints: /mcp (streamable), /sse (legacy), /healthz",
		ln.Addr(), useTLS, opts.Token != "")

	errc := make(chan error, 1)
	go func() {
		if useTLS {
			errc <- hs.ServeTLS(ln, opts.TLSCert, opts.TLSKey)
		} else {
			errc <- hs.Serve(ln)
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
		defer cancel()
		return hs.Shutdown(shutdownCtx)
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// isLoopback reports whether addr binds only a loopback interface. A wildcard
// host (":8080" or "0.0.0.0:8080") is not loopback.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.TrimSpace(host)
	switch host {
	case "":
		return false // ":8080" binds all interfaces
	case "localhost":
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
