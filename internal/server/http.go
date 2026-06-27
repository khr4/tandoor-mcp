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

const (
	// httpShutdownTimeout bounds the graceful drain of in-flight requests when
	// the root context is cancelled.
	httpShutdownTimeout = 10 * time.Second
	// readHeaderTimeout bounds how long request headers may take to arrive.
	readHeaderTimeout = 10 * time.Second
	// idleConnTimeout closes idle keep-alive connections so they cannot
	// accumulate. It applies only between requests, never during an SSE stream.
	idleConnTimeout = 120 * time.Second
)

// HTTPOptions configures the network MCP transport (an alternative to stdio).
type HTTPOptions struct {
	// Addr is the listen address, e.g. ":8080" or "127.0.0.1:8080".
	Addr string
	// Token, when set, is the static bearer token every MCP client must present.
	// When empty, serving is permitted only on a loopback address (see Serve).
	Token string
	// TLSCert and TLSKey, when both set, enable HTTPS (and HTTP/2 via ALPN);
	// otherwise the server is cleartext with HTTP/2 cleartext (h2c) support.
	TLSCert string
	TLSKey  string
	// AllowCleartextNonLoopback permits cleartext on a non-loopback bind. This
	// is unsafe unless a same-network TLS tunnel or proxy is protecting the link.
	AllowCleartextNonLoopback bool
	// ReadyCheck, when set, backs /readyz with a real upstream Tandoor check.
	ReadyCheck func(context.Context) error
}

// newHTTPHandler builds the MCP HTTP handler: the modern Streamable HTTP
// transport at /mcp (request/response plus server-sent-event streaming) and the
// legacy SSE transport at /sse, both backed by the same server and gated by a
// static bearer token when one is set, plus an unauthenticated /healthz probe.
//
// SAFETY: with token == "" this handler is unauthenticated. The refusal to bind
// a non-loopback address without a token lives in Serve — do not mount this
// handler on a public listener by any other path.
func newHTTPHandler(srv *mcp.Server, token string, readyCheck func(context.Context) error) http.Handler {
	getServer := func(*http.Request) *mcp.Server { return srv }

	// When a bearer token gates the endpoint, the token — not the request's Host
	// header — is the trust boundary, so disable the SDK's DNS-rebinding
	// protection: it otherwise rejects (403) a same-host reverse proxy that
	// forwards a public Host header to this loopback-bound backend, which is the
	// common deployment. With no token (a bare loopback dev server) keep it on,
	// where it usefully blocks browser-driven DNS-rebinding.
	disableRebind := token != ""
	streamable := mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{DisableLocalhostProtection: disableRebind})
	sse := mcp.NewSSEHandler(getServer, &mcp.SSEOptions{DisableLocalhostProtection: disableRebind})
	ready := newReadyProbe(readyCheck)

	mux := http.NewServeMux()
	mux.Handle("/mcp", protect(token, streamable))
	mux.Handle("/sse", protect(token, sse))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if readyCheck == nil {
			writeJSONStatus(w, http.StatusServiceUnavailable, `{"status":"not_configured"}`)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if !ready.checkReady(ctx) {
			writeJSONStatus(w, http.StatusServiceUnavailable, `{"status":"unready"}`)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})
	return mux
}

func writeJSONStatus(w http.ResponseWriter, status int, body string) {
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// protect wraps h with a constant-time static-bearer check. An empty token
// leaves h unauthenticated (permitted only on loopback; enforced by Serve). The
// token is compared as a SHA-256 digest so neither its value nor its length
// leaks through timing. The "Bearer" scheme is matched case-insensitively per
// RFC 7235.
func protect(token string, h http.Handler) http.Handler {
	if token == "" {
		return h
	}
	want := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme, credential, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if !ok || !strings.EqualFold(scheme, "bearer") {
			unauthorized(w)
			return
		}
		got := sha256.Sum256([]byte(strings.TrimSpace(credential)))
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

// Serve runs the MCP HTTP transport on opts.Addr until ctx is cancelled, then
// drains connections within httpShutdownTimeout. It fails closed: a non-loopback
// bind without a token is refused, because the server proxies to Tandoor with
// the configured API token and an open endpoint grants full access.
func Serve(ctx context.Context, srv *mcp.Server, opts HTTPOptions) error {
	if (opts.TLSCert == "") != (opts.TLSKey == "") {
		return fmt.Errorf("TLS needs both TANDOOR_TLS_CERT and TANDOOR_TLS_KEY, or neither")
	}
	if opts.Token == "" && !isLoopback(opts.Addr) {
		return fmt.Errorf("refusing to serve MCP on non-loopback address %q without a token: set TANDOOR_MCP_TOKEN (an open endpoint grants full Tandoor access)", opts.Addr)
	}
	if opts.Token != "" && !isLoopback(opts.Addr) && len(opts.Token) < 24 {
		return fmt.Errorf("refusing to serve MCP on non-loopback address %q with a short token: use at least 24 characters", opts.Addr)
	}
	if opts.Token == "" {
		log.Printf("warning: serving MCP on %s without authentication (loopback only); set TANDOOR_MCP_TOKEN to require a bearer token", opts.Addr)
	}
	if opts.TLSCert == "" && !isLoopback(opts.Addr) && !opts.AllowCleartextNonLoopback {
		return fmt.Errorf("refusing cleartext MCP on non-loopback address %q: bind loopback behind TLS, set TANDOOR_TLS_CERT/KEY, or set TANDOOR_HTTP_ALLOW_CLEAR=true only behind another encrypted transport", opts.Addr)
	}
	if opts.TLSCert == "" && !isLoopback(opts.Addr) {
		log.Printf("warning: serving cleartext on non-loopback %s because TANDOOR_HTTP_ALLOW_CLEAR=true; the bearer token and all data are exposed unless another encrypted transport protects it", opts.Addr)
	}
	ln, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", opts.Addr, err)
	}
	return serveListener(ctx, srv, opts, ln)
}

// serveListener runs the HTTP server on an already-bound listener. Split out so
// tests can drive a real serve/shutdown cycle on an ephemeral port. It owns the
// listener: the port is released on every exit path.
func serveListener(ctx context.Context, srv *mcp.Server, opts HTTPOptions, ln net.Listener) error {
	defer func() { _ = ln.Close() }() // unconditional release; double-close is a harmless ErrClosed
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
		Handler:   newHTTPHandler(srv, opts.Token, opts.ReadyCheck),
		Protocols: protocols,
		// Bound header read and idle keep-alive; deliberately no Read/Write
		// timeout, which would sever long-lived SSE streams. IdleTimeout applies
		// only between requests on a kept-alive connection, never during a stream.
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleConnTimeout,
		// BaseContext must return the cancellable root ctx so that, on shutdown,
		// in-flight SSE/streamable handlers (which select on the request context)
		// unwind promptly instead of forcing Shutdown to wait out its timeout.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}

	log.Printf("MCP HTTP transport on %s (tls=%v, auth=%v); endpoints: /mcp (streamable), /sse (legacy), /healthz, /readyz",
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
