// Package tandoor is a thin, typed HTTP client for the Tandoor Recipes REST API.
//
// It deliberately treats request and response bodies as opaque JSON: Tandoor's
// resources are large and evolve between releases, so the client moves bytes,
// sets auth, builds query strings and surfaces API errors, while callers decide
// the shape of each payload. Higher layers (the MCP tools) add structure on top.
package tandoor

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// maxResponseBytes caps how much of any response body the client buffers, so a
// malicious or buggy instance cannot exhaust memory.
const maxResponseBytes = 32 << 20

// maxErrorBodyRunes bounds how much of an error response body is echoed back.
const maxErrorBodyRunes = 4000

// Config holds everything needed to talk to a Tandoor instance.
type Config struct {
	// BaseURL is the instance root, e.g. https://recipes.example.com (no /api suffix).
	BaseURL string
	// Token is a Tandoor API token (Settings > API > generate).
	Token string
	// Insecure skips TLS certificate verification (self-signed self-hosted instances).
	Insecure bool
	// Timeout bounds each request. Zero means 30s.
	Timeout time.Duration
}

// ConfigFromEnv reads configuration from the standard environment variables:
//
//	TANDOOR_URL                  instance root URL (required)
//	TANDOOR_TOKEN                API token (required; TANDOOR_API_TOKEN also accepted)
//	TANDOOR_INSECURE_SKIP_VERIFY skip TLS verification (optional bool)
//	TANDOOR_TIMEOUT              per-request timeout in whole seconds (optional, default 30)
func ConfigFromEnv() (Config, error) {
	cfg := Config{
		BaseURL: strings.TrimSpace(os.Getenv("TANDOOR_URL")),
		Token:   strings.TrimSpace(firstNonEmpty(os.Getenv("TANDOOR_TOKEN"), os.Getenv("TANDOOR_API_TOKEN"))),
		Timeout: 30 * time.Second,
	}
	if cfg.BaseURL == "" {
		return cfg, fmt.Errorf("TANDOOR_URL is required (e.g. https://recipes.example.com)")
	}
	if cfg.Token == "" {
		return cfg, fmt.Errorf("TANDOOR_TOKEN is required (Tandoor: Settings > API > generate a token)")
	}
	if v := strings.TrimSpace(os.Getenv("TANDOOR_INSECURE_SKIP_VERIFY")); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return cfg, fmt.Errorf("TANDOOR_INSECURE_SKIP_VERIFY: %w", err)
		}
		cfg.Insecure = b
	}
	if v := strings.TrimSpace(os.Getenv("TANDOOR_TIMEOUT")); v != "" {
		secs, err := strconv.Atoi(v)
		if err != nil || secs < 0 {
			return cfg, fmt.Errorf("TANDOOR_TIMEOUT must be a non-negative number of seconds, got %q", v)
		}
		cfg.Timeout = time.Duration(secs) * time.Second
	}
	return cfg, nil
}

// Client is a Tandoor API client. It is safe for concurrent use.
type Client struct {
	base  *url.URL
	token string
	http  *http.Client
}

// New builds a Client from cfg.
func New(cfg Config) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(cfg.BaseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("invalid TANDOOR_URL: %w", err)
	}
	if base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("invalid TANDOOR_URL %q: need a scheme and host", cfg.BaseURL)
	}
	if cfg.Token == "" {
		return nil, fmt.Errorf("token is required")
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	hc := &http.Client{Timeout: timeout}
	if cfg.Insecure {
		log.Printf("warning: TLS certificate verification disabled; the API token is exposed to network interception")
		hc.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	return &Client{base: base, token: cfg.Token, http: hc}, nil
}

// APIError is returned for any non-2xx response. It carries the verbatim body so
// callers (and the LLM) can read Tandoor's validation messages.
type APIError struct {
	StatusCode int
	Method     string
	Path       string
	Body       string
}

func (e *APIError) Error() string {
	body := strings.TrimSpace(e.Body)
	if utf8.RuneCountInString(body) > maxErrorBodyRunes {
		body = string([]rune(body)[:maxErrorBodyRunes]) + "…(truncated)"
	}
	msg := fmt.Sprintf("tandoor API %s %s -> %d %s", e.Method, e.Path, e.StatusCode, http.StatusText(e.StatusCode))
	if body != "" {
		msg += ": " + body
	}
	return msg
}

// Do performs a JSON request. path is relative to the instance root; an "api/"
// prefix is added when absent. body, when non-nil, is JSON-encoded. It returns
// the raw response body (nil for empty/204 responses).
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body any) (json.RawMessage, error) {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encoding request body: %w", err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint(path, query), reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.send(req, method, path)
}

// Upload performs a multipart/form-data request, used for endpoints that accept
// file uploads (e.g. setting a recipe image). file may be nil to send only fields.
func (c *Client) Upload(ctx context.Context, method, path string, fields map[string]string, fileField, fileName string, file io.Reader) (json.RawMessage, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return nil, fmt.Errorf("writing form field %q: %w", k, err)
		}
	}
	if file != nil && fileField != "" {
		fw, err := mw.CreateFormFile(fileField, fileName)
		if err != nil {
			return nil, fmt.Errorf("creating form file: %w", err)
		}
		if _, err := io.Copy(fw, file); err != nil {
			return nil, fmt.Errorf("copying file: %w", err)
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint(path, nil), &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return c.send(req, method, path)
}

func (c *Client) send(req *http.Request, method, path string) (json.RawMessage, error) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s %s response: %w", method, path, err)
	}
	if len(data) > maxResponseBytes {
		return nil, fmt.Errorf("%s %s: response exceeds %d bytes", method, path, maxResponseBytes)
	}
	if resp.StatusCode >= 400 {
		return nil, &APIError{StatusCode: resp.StatusCode, Method: method, Path: path, Body: string(data)}
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	return json.RawMessage(data), nil
}

// endpoint joins the base URL with an API path and encodes the query string.
func (c *Client) endpoint(p string, query url.Values) string {
	p = strings.TrimLeft(p, "/")
	if !strings.HasPrefix(p, "api/") {
		p = "api/" + p
	}
	u := *c.base
	u.Path = strings.TrimRight(c.base.Path, "/") + "/" + p
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	return u.String()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
