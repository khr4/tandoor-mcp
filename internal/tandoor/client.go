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
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// maxResponseBytes caps how much of any response body the client buffers, so a
// malicious or buggy instance cannot exhaust memory.
const maxResponseBytes = 32 << 20

// maxErrorBodyRunes bounds how much of an error response body is echoed back.
const maxErrorBodyRunes = 4000

const (
	defaultTimeout         = 30 * time.Second
	defaultMaxConcurrency  = 8
	defaultRetryMax        = 2
	defaultRetryBaseDelay  = 200 * time.Millisecond
	defaultBreakerFailures = 5
	defaultBreakerCooldown = 10 * time.Second
)

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
	// MaxConcurrency bounds concurrent upstream requests. Zero means 8.
	MaxConcurrency int
	// RetryMax is the number of extra attempts for safe reads. ConfigFromEnv
	// defaults it to 2; zero disables retries for direct Config callers.
	RetryMax int
	// RetryBaseDelay is the base exponential backoff delay. Zero means 200ms.
	RetryBaseDelay time.Duration
	// BreakerFailures opens the circuit after this many consecutive temporary
	// failures. Zero means 5.
	BreakerFailures int
	// BreakerCooldown is how long an open circuit fast-fails before a probe.
	// Zero means 10s.
	BreakerCooldown time.Duration
}

// ConfigFromEnv reads configuration from the standard environment variables:
//
//	TANDOOR_URL                  instance root URL (required)
//	TANDOOR_TOKEN                API token (required; TANDOOR_API_TOKEN also accepted)
//	TANDOOR_INSECURE_SKIP_VERIFY skip TLS verification (optional bool)
//	TANDOOR_TIMEOUT              per-request timeout in whole seconds (optional, default 30)
//	TANDOOR_MAX_CONCURRENCY      max concurrent upstream requests (optional, default 8)
//	TANDOOR_RETRY_MAX            extra attempts for safe reads (optional, default 2)
//	TANDOOR_RETRY_BASE_MS        base retry backoff in milliseconds (optional, default 200)
//	TANDOOR_BREAKER_FAILURES     consecutive temporary failures before opening (optional, default 5)
//	TANDOOR_BREAKER_COOLDOWN_SECONDS open-circuit cooldown in seconds (optional, default 10)
func ConfigFromEnv() (Config, error) {
	cfg := Config{
		BaseURL:         strings.TrimSpace(os.Getenv("TANDOOR_URL")),
		Token:           strings.TrimSpace(firstNonEmpty(os.Getenv("TANDOOR_TOKEN"), os.Getenv("TANDOOR_API_TOKEN"))),
		Timeout:         defaultTimeout,
		MaxConcurrency:  defaultMaxConcurrency,
		RetryMax:        defaultRetryMax,
		RetryBaseDelay:  defaultRetryBaseDelay,
		BreakerFailures: defaultBreakerFailures,
		BreakerCooldown: defaultBreakerCooldown,
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
	if err := parseEnvInt("TANDOOR_MAX_CONCURRENCY", 1, 64, &cfg.MaxConcurrency); err != nil {
		return cfg, err
	}
	if err := parseEnvInt("TANDOOR_RETRY_MAX", 0, 5, &cfg.RetryMax); err != nil {
		return cfg, err
	}
	if err := parseEnvDurationMS("TANDOOR_RETRY_BASE_MS", 1, 5000, &cfg.RetryBaseDelay); err != nil {
		return cfg, err
	}
	if err := parseEnvInt("TANDOOR_BREAKER_FAILURES", 1, 100, &cfg.BreakerFailures); err != nil {
		return cfg, err
	}
	if err := parseEnvDurationSeconds("TANDOOR_BREAKER_COOLDOWN_SECONDS", 1, 3600, &cfg.BreakerCooldown); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Client is a Tandoor API client. It is safe for concurrent use.
type Client struct {
	base           *url.URL
	token          string
	http           *http.Client
	bulkhead       chan struct{}
	retryMax       int
	retryBaseDelay time.Duration
	breaker        *circuitBreaker
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
	if base.Scheme != "https" && base.Scheme != "http" {
		return nil, fmt.Errorf("invalid TANDOOR_URL %q: scheme must be http or https", cfg.BaseURL)
	}
	if base.Scheme == "http" {
		if !isLocalHTTPHost(base.Hostname()) {
			return nil, fmt.Errorf("refusing cleartext TANDOOR_URL %q for a non-local host: use https", cfg.BaseURL)
		}
		log.Printf("warning: using cleartext TANDOOR_URL %s; the API token is visible on that connection", cfg.BaseURL)
	}
	if cfg.Token == "" {
		return nil, fmt.Errorf("token is required")
	}
	timeout := cfg.Timeout
	if timeout < 0 {
		return nil, fmt.Errorf("timeout must be non-negative, got %s", cfg.Timeout)
	}
	if timeout == 0 {
		timeout = defaultTimeout
	}
	maxConcurrency := cfg.MaxConcurrency
	if maxConcurrency == 0 {
		maxConcurrency = defaultMaxConcurrency
	}
	if maxConcurrency < 1 || maxConcurrency > 64 {
		return nil, fmt.Errorf("maxConcurrency must be between 1 and 64 when set, got %d", cfg.MaxConcurrency)
	}
	retryMax := cfg.RetryMax
	if retryMax < 0 || retryMax > 5 {
		return nil, fmt.Errorf("retryMax must be between 0 and 5, got %d", cfg.RetryMax)
	}
	retryBaseDelay := cfg.RetryBaseDelay
	if retryBaseDelay < 0 {
		return nil, fmt.Errorf("retryBaseDelay must be non-negative, got %s", cfg.RetryBaseDelay)
	}
	if retryBaseDelay == 0 {
		retryBaseDelay = defaultRetryBaseDelay
	}
	breakerFailures := cfg.BreakerFailures
	if breakerFailures < 0 {
		return nil, fmt.Errorf("breakerFailures must be non-negative, got %d", cfg.BreakerFailures)
	}
	if breakerFailures == 0 {
		breakerFailures = defaultBreakerFailures
	}
	breakerCooldown := cfg.BreakerCooldown
	if breakerCooldown < 0 {
		return nil, fmt.Errorf("breakerCooldown must be non-negative, got %s", cfg.BreakerCooldown)
	}
	if breakerCooldown == 0 {
		breakerCooldown = defaultBreakerCooldown
	}

	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxConnsPerHost = maxConcurrency
	if tr.MaxIdleConnsPerHost < maxConcurrency {
		tr.MaxIdleConnsPerHost = maxConcurrency
	}
	if tr.MaxIdleConns < maxConcurrency*2 {
		tr.MaxIdleConns = maxConcurrency * 2
	}
	if cfg.Insecure {
		log.Printf("warning: TLS certificate verification disabled; the API token is exposed to network interception")
		tr.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true,             //nolint:gosec // opt-in via TANDOOR_INSECURE_SKIP_VERIFY
			MinVersion:         tls.VersionTLS12, // still refuse downgraded TLS even when not verifying the cert
		}
	}
	hc := &http.Client{Timeout: timeout, Transport: tr}
	return &Client{
		base: base, token: cfg.Token, http: hc,
		bulkhead:       make(chan struct{}, maxConcurrency),
		retryMax:       retryMax,
		retryBaseDelay: retryBaseDelay,
		breaker:        newCircuitBreaker(breakerFailures, breakerCooldown),
	}, nil
}

// APIError is returned for any non-2xx response. It carries the verbatim body so
// callers (and the LLM) can read Tandoor's validation messages.
type APIError struct {
	StatusCode int
	Method     string
	Path       string
	Body       string
	RetryAfter time.Duration
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

// OutcomeUnknownError marks a mutating request whose response failed in a way
// that leaves the server-side commit status unknowable from this process.
type OutcomeUnknownError struct {
	Method string
	Path   string
	Cause  error
}

func (e *OutcomeUnknownError) Error() string {
	return fmt.Sprintf("tandoor API %s %s outcome unknown after temporary failure: %v", e.Method, e.Path, e.Cause)
}

func (e *OutcomeUnknownError) Unwrap() error {
	return e.Cause
}

// Do performs a JSON request. path is relative to the instance root; an "api/"
// prefix is added when absent. body, when non-nil, is JSON-encoded. It returns
// the raw response body (nil for empty/204 responses).
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body any) (json.RawMessage, error) {
	var bodyBytes []byte
	contentType := ""
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encoding request body: %w", err)
		}
		bodyBytes = buf
		contentType = "application/json"
	}
	endpoint := c.endpoint(path, query)
	return c.send(ctx, method, path, contentType, func(ctx context.Context) (*http.Request, error) {
		var reader io.Reader
		if bodyBytes != nil {
			reader = bytes.NewReader(bodyBytes)
		}
		return http.NewRequestWithContext(ctx, method, endpoint, reader)
	})
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
	bodyBytes := buf.Bytes()
	contentType := mw.FormDataContentType()
	endpoint := c.endpoint(path, nil)
	return c.send(ctx, method, path, contentType, func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(bodyBytes))
	})
}

func (c *Client) send(ctx context.Context, method, path, contentType string, newRequest func(context.Context) (*http.Request, error)) (json.RawMessage, error) {
	method = strings.ToUpper(method)
	attempts := 1
	if method == http.MethodGet {
		attempts += c.retryMax
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		raw, err := c.sendOnce(ctx, method, path, contentType, newRequest)
		if err == nil {
			c.breaker.recordSuccess()
			return raw, nil
		}
		lastErr = err
		temporary := isTemporaryFailure(err)
		opened := c.breaker.recordFailure(temporary)
		if method != http.MethodGet && temporary {
			return nil, &OutcomeUnknownError{Method: method, Path: path, Cause: err}
		}
		if method != http.MethodGet || !temporary || attempt == attempts-1 || opened {
			return nil, err
		}
		delay := retryDelay(attempt, c.retryBaseDelay, retryAfter(err))
		if err := sleepContext(ctx, delay); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (c *Client) sendOnce(ctx context.Context, method, path, contentType string, newRequest func(context.Context) (*http.Request, error)) (json.RawMessage, error) {
	if err := c.breaker.allow(); err != nil {
		return nil, err
	}
	release, err := c.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	req, err := newRequest(ctx)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s %s response: %w", method, path, err)
	}
	if len(data) > maxResponseBytes {
		return nil, fmt.Errorf("%s %s: response exceeds %d bytes", method, path, maxResponseBytes)
	}
	if resp.StatusCode >= 400 {
		return nil, &APIError{
			StatusCode: resp.StatusCode,
			Method:     method,
			Path:       path,
			Body:       string(data),
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	return json.RawMessage(data), nil
}

func (c *Client) acquire(ctx context.Context) (func(), error) {
	if c.bulkhead == nil {
		return func() {}, nil
	}
	select {
	case c.bulkhead <- struct{}{}:
		return func() { <-c.bulkhead }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for Tandoor upstream concurrency slot: %w", ctx.Err())
	}
}

func isTemporaryFailure(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusTooManyRequests || apiErr.StatusCode >= 500
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return strings.Contains(err.Error(), "connection refused") ||
		strings.Contains(err.Error(), "connection reset") ||
		strings.Contains(err.Error(), "unexpected EOF")
}

func retryAfter(err error) time.Duration {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.RetryAfter
	}
	return 0
}

func retryDelay(attempt int, base, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}
	if base <= 0 {
		base = defaultRetryBaseDelay
	}
	delay := base << attempt
	jitterMax := delay / 2
	if jitterMax <= 0 {
		return delay
	}
	return delay + time.Duration(rand.Int63n(int64(jitterMax)+1)) //nolint:gosec // non-security jitter
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < d {
		return fmt.Errorf("not enough operation budget for retry delay %s: %w", d, context.DeadlineExceeded)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if when, err := http.ParseTime(v); err == nil {
		if d := time.Until(when); d > 0 {
			return d
		}
	}
	return 0
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

func parseEnvInt(name string, lo, hi int, dst *int) error {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		return fmt.Errorf("%s must be an integer between %d and %d, got %q", name, lo, hi, v)
	}
	*dst = n
	return nil
}

func parseEnvDurationMS(name string, lo, hi int, dst *time.Duration) error {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		return fmt.Errorf("%s must be a number of milliseconds between %d and %d, got %q", name, lo, hi, v)
	}
	*dst = time.Duration(n) * time.Millisecond
	return nil
}

func parseEnvDurationSeconds(name string, lo, hi int, dst *time.Duration) error {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		return fmt.Errorf("%s must be a number of seconds between %d and %d, got %q", name, lo, hi, v)
	}
	*dst = time.Duration(n) * time.Second
	return nil
}

func isLocalHTTPHost(host string) bool {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate()
}

type breakerState int

const (
	breakerClosed breakerState = iota
	breakerOpen
	breakerHalfOpen
)

// BreakerOpenError is returned when the upstream circuit breaker is open and the
// request was not attempted; RetryAfter is how long to wait before retrying.
type BreakerOpenError struct {
	RetryAfter time.Duration
}

func (e *BreakerOpenError) Error() string {
	return fmt.Sprintf("Tandoor upstream circuit breaker is open; retry after %s", e.RetryAfter.Round(time.Millisecond))
}

type circuitBreaker struct {
	mu        sync.Mutex
	state     breakerState
	failures  int
	threshold int
	cooldown  time.Duration
	openedAt  time.Time
	probing   bool
}

func newCircuitBreaker(threshold int, cooldown time.Duration) *circuitBreaker {
	if threshold <= 0 {
		threshold = defaultBreakerFailures
	}
	if cooldown <= 0 {
		cooldown = defaultBreakerCooldown
	}
	return &circuitBreaker{threshold: threshold, cooldown: cooldown}
}

func (b *circuitBreaker) allow() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state != breakerOpen {
		return nil
	}
	remaining := b.cooldown - time.Since(b.openedAt)
	if remaining > 0 {
		return &BreakerOpenError{RetryAfter: remaining}
	}
	if b.probing {
		return &BreakerOpenError{RetryAfter: b.cooldown}
	}
	b.state = breakerHalfOpen
	b.probing = true
	return nil
}

func (b *circuitBreaker) recordSuccess() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state = breakerClosed
	b.failures = 0
	b.probing = false
}

func (b *circuitBreaker) recordFailure(temporary bool) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !temporary {
		if b.state == breakerHalfOpen {
			b.state = breakerClosed
			b.probing = false
			b.failures = 0
		}
		return false
	}
	if b.state == breakerHalfOpen {
		b.openLocked()
		return true
	}
	b.failures++
	if b.failures >= b.threshold {
		b.openLocked()
		return true
	}
	return false
}

func (b *circuitBreaker) openLocked() {
	b.state = breakerOpen
	b.openedAt = time.Now()
	b.probing = false
	b.failures = 0
}
