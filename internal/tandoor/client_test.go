package tandoor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestNewInsecureLogsWarning(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(io.Discard); log.SetOutput(nil) })
	if _, err := New(Config{BaseURL: "https://x.example", Token: "t", Insecure: true}); err != nil {
		t.Fatalf("New: %v", err)
	}
	if !strings.Contains(buf.String(), "TLS certificate verification disabled") {
		t.Errorf("expected insecure warning, got %q", buf.String())
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
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
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
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
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
