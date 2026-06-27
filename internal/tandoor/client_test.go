package tandoor

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func TestNewInsecureLogsWarning(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	if _, err := New(Config{BaseURL: "https://x.example", Token: "t", Insecure: true}); err != nil {
		t.Fatalf("New: %v", err)
	}
	if !strings.Contains(buf.String(), "TLS certificate verification disabled") {
		t.Errorf("expected insecure warning, got %q", buf.String())
	}
}

func TestInsecureTransportKeepsHTTP2AndProxy(t *testing.T) {
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	c, err := New(Config{BaseURL: "https://x.example", Token: "t", Insecure: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tr, ok := c.http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", c.http.Transport)
	}
	if tr.TLSClientConfig == nil || !tr.TLSClientConfig.InsecureSkipVerify {
		t.Error("InsecureSkipVerify not set on the insecure transport")
	}
	if tr.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %#x, want TLS 1.2: refuse downgraded TLS even when not verifying", tr.TLSClientConfig.MinVersion)
	}
	// The bug: a bare Transport with a custom TLSClientConfig drops these.
	if !tr.ForceAttemptHTTP2 {
		t.Error("ForceAttemptHTTP2 is false: HTTP/2 silently disabled in insecure mode")
	}
	if tr.Proxy == nil {
		t.Error("Proxy is nil: HTTPS_PROXY/NO_PROXY ignored in insecure mode")
	}
	if tr.IdleConnTimeout == 0 {
		t.Error("IdleConnTimeout is zero: connection-pool tunings lost in insecure mode")
	}
}

func TestSecureTransportUsesDefault(t *testing.T) {
	c, err := New(Config{BaseURL: "https://x.example", Token: "t"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tr, ok := c.http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", c.http.Transport)
	}
	if !tr.ForceAttemptHTTP2 {
		t.Error("ForceAttemptHTTP2 is false: HTTP/2 silently disabled")
	}
	if tr.Proxy == nil {
		t.Error("Proxy is nil: HTTPS_PROXY/NO_PROXY ignored")
	}
	if tr.MaxConnsPerHost != defaultMaxConcurrency {
		t.Errorf("MaxConnsPerHost = %d, want %d", tr.MaxConnsPerHost, defaultMaxConcurrency)
	}
}

func TestNewRefusesPublicCleartextURL(t *testing.T) {
	if _, err := New(Config{BaseURL: "http://recipes.example.com", Token: "t"}); err == nil {
		t.Fatal("expected public http TANDOOR_URL to be refused")
	}
	if _, err := New(Config{BaseURL: "http://127.0.0.1:8080", Token: "t"}); err != nil {
		t.Fatalf("loopback http should remain usable for local tests/dev: %v", err)
	}
}

func TestAPIErrorTruncatesOnRuneBoundary(t *testing.T) {
	e := &APIError{StatusCode: 400, Method: "POST", Path: "recipe/", Body: strings.Repeat("é", 5000)}
	msg := e.Error()
	if !utf8.ValidString(msg) {
		t.Error("error message is not valid UTF-8")
	}
	if !strings.Contains(msg, "truncated") {
		t.Errorf("expected truncation marker, got %q", msg[:60])
	}
}

func TestConfigFromEnvInsecureAndBadValues(t *testing.T) {
	t.Setenv("TANDOOR_URL", "https://x.example")
	t.Setenv("TANDOOR_TOKEN", "t")
	t.Setenv("TANDOOR_INSECURE_SKIP_VERIFY", "true")
	cfg, err := ConfigFromEnv()
	if err != nil || !cfg.Insecure {
		t.Fatalf("Insecure = %v, err = %v", cfg.Insecure, err)
	}
	t.Setenv("TANDOOR_INSECURE_SKIP_VERIFY", "nonsense")
	if _, err := ConfigFromEnv(); err == nil {
		t.Error("expected error for bad TANDOOR_INSECURE_SKIP_VERIFY")
	}
	t.Setenv("TANDOOR_INSECURE_SKIP_VERIFY", "")
	t.Setenv("TANDOOR_TIMEOUT", "-3")
	if _, err := ConfigFromEnv(); err == nil {
		t.Error("expected error for negative TANDOOR_TIMEOUT")
	}
}

// newTestClient points a Client at an httptest server.
func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(Config{BaseURL: srv.URL, Token: "secret-token", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestDoGETSendsAuthPathAndQuery(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotAccept, gotQuery string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.RawQuery
		gotAuth, gotAccept = r.Header.Get("Authorization"), r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"count":2,"results":[{"id":1},{"id":2}]}`)
	})

	q := map[string][]string{"query": {"soup"}, "page_size": {"10"}}
	raw, err := c.Do(context.Background(), http.MethodGet, "recipe/", q, nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/api/recipe/" {
		t.Errorf("path = %q, want /api/recipe/", gotPath)
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("auth = %q, want Bearer secret-token", gotAuth)
	}
	if gotAccept != "application/json" {
		t.Errorf("accept = %q, want application/json", gotAccept)
	}
	if !strings.Contains(gotQuery, "query=soup") || !strings.Contains(gotQuery, "page_size=10") {
		t.Errorf("query = %q, want query=soup and page_size=10", gotQuery)
	}
	var env struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || env.Count != 2 {
		t.Errorf("body = %s, err = %v", raw, err)
	}
}

func TestDoPOSTEncodesJSONBody(t *testing.T) {
	var gotCT string
	var gotBody map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":7,"name":"Soup"}`)
	})

	_, err := c.Do(context.Background(), http.MethodPost, "recipe/", nil, map[string]any{"name": "Soup"})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q, want application/json", gotCT)
	}
	if gotBody["name"] != "Soup" {
		t.Errorf("body = %v, want name=Soup", gotBody)
	}
}

func TestDoPrefixesAPIOnlyWhenAbsent(t *testing.T) {
	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, `{}`)
	})
	if _, err := c.Do(context.Background(), http.MethodGet, "api/recipe/5/related/", nil, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if gotPath != "/api/recipe/5/related/" {
		t.Errorf("path = %q, want /api/recipe/5/related/ (no double api/)", gotPath)
	}
}

func TestDoReturnsAPIErrorWithBody(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"name":["This field is required."]}`)
	})
	_, err := c.Do(context.Background(), http.MethodPost, "recipe/", nil, map[string]any{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Error(), "This field is required.") {
		t.Errorf("error %q does not include the API body", apiErr.Error())
	}
}

func TestDoEmptyResponseIsNil(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	raw, err := c.Do(context.Background(), http.MethodDelete, "recipe/5/", nil, nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if raw != nil {
		t.Errorf("raw = %q, want nil for 204", raw)
	}
}

func TestUploadSendsMultipartFileAndFields(t *testing.T) {
	var gotField, gotFileName, gotFileBody string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mt != "multipart/form-data" {
			t.Errorf("content-type = %q", r.Header.Get("Content-Type"))
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			data, _ := io.ReadAll(p)
			switch p.FormName() {
			case "name":
				gotField = string(data)
			case "image":
				gotFileName = p.FileName()
				gotFileBody = string(data)
			}
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	})

	_, err := c.Upload(context.Background(), http.MethodPut, "recipe/5/image/",
		map[string]string{"name": "pie.png"}, "image", "pie.png", strings.NewReader("PNGDATA"))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if gotField != "pie.png" {
		t.Errorf("field name = %q, want pie.png", gotField)
	}
	if gotFileName != "pie.png" || gotFileBody != "PNGDATA" {
		t.Errorf("file = %q/%q, want pie.png/PNGDATA", gotFileName, gotFileBody)
	}
}

func TestConfigFromEnvValidation(t *testing.T) {
	t.Setenv("TANDOOR_URL", "")
	t.Setenv("TANDOOR_TOKEN", "")
	t.Setenv("TANDOOR_API_TOKEN", "")
	if _, err := ConfigFromEnv(); err == nil {
		t.Error("expected error when TANDOOR_URL is unset")
	}

	t.Setenv("TANDOOR_URL", "https://recipes.example.com")
	t.Setenv("TANDOOR_API_TOKEN", "fallback-token")
	t.Setenv("TANDOOR_TIMEOUT", "15")
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.Token != "fallback-token" {
		t.Errorf("token = %q, want fallback-token (from TANDOOR_API_TOKEN)", cfg.Token)
	}
	if cfg.Timeout != 15*time.Second {
		t.Errorf("timeout = %v, want 15s", cfg.Timeout)
	}
}

func TestConfigFromEnvResilienceKnobs(t *testing.T) {
	t.Setenv("TANDOOR_URL", "https://recipes.example.com")
	t.Setenv("TANDOOR_TOKEN", "tok")
	t.Setenv("TANDOOR_MAX_CONCURRENCY", "3")
	t.Setenv("TANDOOR_RETRY_MAX", "0")
	t.Setenv("TANDOOR_RETRY_BASE_MS", "7")
	t.Setenv("TANDOOR_BREAKER_FAILURES", "2")
	t.Setenv("TANDOOR_BREAKER_COOLDOWN_SECONDS", "4")
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.MaxConcurrency != 3 || cfg.RetryMax != 0 || cfg.RetryBaseDelay != 7*time.Millisecond || cfg.BreakerFailures != 2 || cfg.BreakerCooldown != 4*time.Second {
		t.Errorf("cfg = %+v, resilience knobs not parsed", cfg)
	}
}

func TestNewRejectsInvalidResilienceConfig(t *testing.T) {
	base := Config{BaseURL: "https://recipes.example.com", Token: "tok"}
	cases := []struct {
		name string
		cfg  Config
	}{
		{name: "negative timeout", cfg: Config{Timeout: -time.Second}},
		{name: "negative max concurrency", cfg: Config{MaxConcurrency: -1}},
		{name: "excessive max concurrency", cfg: Config{MaxConcurrency: 65}},
		{name: "negative retry max", cfg: Config{RetryMax: -1}},
		{name: "excessive retry max", cfg: Config{RetryMax: 6}},
		{name: "negative retry delay", cfg: Config{RetryBaseDelay: -time.Millisecond}},
		{name: "negative breaker failures", cfg: Config{BreakerFailures: -1}},
		{name: "negative breaker cooldown", cfg: Config{BreakerCooldown: -time.Second}},
	}
	for _, tc := range cases {
		cfg := base
		if tc.cfg.Timeout != 0 {
			cfg.Timeout = tc.cfg.Timeout
		}
		if tc.cfg.MaxConcurrency != 0 {
			cfg.MaxConcurrency = tc.cfg.MaxConcurrency
		}
		if tc.cfg.RetryMax != 0 {
			cfg.RetryMax = tc.cfg.RetryMax
		}
		if tc.cfg.RetryBaseDelay != 0 {
			cfg.RetryBaseDelay = tc.cfg.RetryBaseDelay
		}
		if tc.cfg.BreakerFailures != 0 {
			cfg.BreakerFailures = tc.cfg.BreakerFailures
		}
		if tc.cfg.BreakerCooldown != 0 {
			cfg.BreakerCooldown = tc.cfg.BreakerCooldown
		}
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: expected error", tc.name)
		}
	}
}

func TestGETRetriesTemporaryFailure(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `try again`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{
		BaseURL: srv.URL, Token: "tok", Timeout: time.Second,
		RetryMax: 1, RetryBaseDelay: time.Millisecond, BreakerFailures: 5,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	raw, err := c.Do(context.Background(), http.MethodGet, "recipe/", nil, nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if string(raw) != `{"ok":true}` || attempts != 2 {
		t.Errorf("raw=%s attempts=%d, want success after two attempts", raw, attempts)
	}
}

func TestMutatingTemporaryFailureIsOutcomeUnknownAndNotRetried(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `database restarting`)
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{
		BaseURL: srv.URL, Token: "tok", Timeout: time.Second,
		RetryMax: 3, RetryBaseDelay: time.Millisecond, BreakerFailures: 5,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = c.Do(context.Background(), http.MethodPost, "recipe/", nil, map[string]any{"name": "Soup"})
	var unknown *OutcomeUnknownError
	if !errors.As(err, &unknown) {
		t.Fatalf("err = %T %[1]v, want OutcomeUnknownError", err)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want no retry for mutating call", attempts)
	}
}

func TestTimeoutFailureSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{BaseURL: srv.URL, Token: "tok", Timeout: 5 * time.Millisecond, RetryMax: 0})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = c.Do(context.Background(), http.MethodGet, "recipe/", nil, nil)
	if err == nil || !isTemporaryFailure(err) {
		t.Fatalf("err = %v, want temporary timeout failure", err)
	}
}

func TestCircuitBreakerFastFailsAfterThreshold(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `down`)
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{
		BaseURL: srv.URL, Token: "tok", Timeout: time.Second,
		RetryMax: 0, BreakerFailures: 1, BreakerCooldown: time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Do(context.Background(), http.MethodGet, "recipe/", nil, nil); err == nil {
		t.Fatal("first call should fail")
	}
	if _, err := c.Do(context.Background(), http.MethodGet, "recipe/", nil, nil); err == nil {
		t.Fatal("second call should fast-fail")
	} else {
		var open *BreakerOpenError
		if !errors.As(err, &open) {
			t.Fatalf("err = %T %[1]v, want BreakerOpenError", err)
		}
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want breaker to block second upstream call", attempts)
	}
}

func TestBulkheadCapsConcurrentUpstreamCalls(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-release
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{BaseURL: srv.URL, Token: "tok", Timeout: time.Second, MaxConcurrency: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.Do(context.Background(), http.MethodGet, "recipe/", nil, nil)
		done <- err
	}()
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := c.Do(ctx, http.MethodGet, "recipe/", nil, nil); err == nil || !strings.Contains(err.Error(), "concurrency slot") {
		t.Fatalf("second Do err = %v, want bulkhead wait failure", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first Do: %v", err)
	}
}

func TestMutatingBulkheadFailureIsNotAttempted(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		entered <- struct{}{}
		<-release
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{BaseURL: srv.URL, Token: "tok", Timeout: time.Second, MaxConcurrency: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.Do(context.Background(), http.MethodGet, "recipe/", nil, nil)
		done <- err
	}()
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err = c.Do(ctx, http.MethodPost, "recipe/", nil, map[string]any{"name": "Soup"})
	var notAttempted *NotAttemptedError
	if !errors.As(err, &notAttempted) {
		t.Fatalf("err = %T %[1]v, want NotAttemptedError", err)
	}
	var unknown *OutcomeUnknownError
	if errors.As(err, &unknown) {
		t.Fatalf("err = %T %[1]v, must not be OutcomeUnknown when no upstream request was sent", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first Do: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want only the occupying GET to reach upstream", attempts)
	}
}
