package tandoor

import (
	"context"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPOSTCancellationAfterUpstreamAttemptIsOutcomeUnknown(t *testing.T) {
	entered := make(chan struct{})
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		close(entered)
		time.Sleep(100 * time.Millisecond)
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{BaseURL: srv.URL, Token: "tok", Timeout: time.Second, RetryMax: 0})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.Do(ctx, http.MethodPost, "recipe/", nil, map[string]any{"name": "Soup"})
		done <- err
	}()
	<-entered
	cancel()

	err = <-done
	var unknown *OutcomeUnknownError
	if !errors.As(err, &unknown) {
		t.Fatalf("err = %T %[1]v, want OutcomeUnknownError after an attempted mutating cancellation", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled in the unwrap chain", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want one upstream attempt", attempts)
	}
}

func TestCircuitBreakerHalfOpenSuccessCloses(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `down`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{
		BaseURL: srv.URL, Token: "tok", Timeout: time.Second,
		RetryMax: 0, BreakerFailures: 1, BreakerCooldown: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Do(context.Background(), http.MethodGet, "recipe/", nil, nil); err == nil {
		t.Fatal("first call should open the breaker")
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := c.Do(context.Background(), http.MethodGet, "recipe/", nil, nil); err != nil {
		t.Fatalf("half-open probe should succeed and close breaker: %v", err)
	}
	if _, err := c.Do(context.Background(), http.MethodGet, "recipe/", nil, nil); err != nil {
		t.Fatalf("closed breaker should allow the next call: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want first failure plus two successful upstream calls", attempts)
	}
}

func TestCircuitBreakerHalfOpenFailureReopens(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `down`)
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{
		BaseURL: srv.URL, Token: "tok", Timeout: time.Second,
		RetryMax: 0, BreakerFailures: 1, BreakerCooldown: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Do(context.Background(), http.MethodGet, "recipe/", nil, nil); err == nil {
		t.Fatal("first call should fail")
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := c.Do(context.Background(), http.MethodGet, "recipe/", nil, nil); err == nil {
		t.Fatal("half-open probe should fail")
	}
	if _, err := c.Do(context.Background(), http.MethodGet, "recipe/", nil, nil); err == nil {
		t.Fatal("breaker should fast-fail after failed half-open probe")
	} else {
		var open *BreakerOpenError
		if !errors.As(err, &open) {
			t.Fatalf("err = %T %[1]v, want BreakerOpenError", err)
		}
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want only first failure and half-open probe to reach upstream", attempts)
	}
}

func TestGETRetryBudgetDeadlineStopsBeforeSecondAttempt(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `try later`)
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{
		BaseURL: srv.URL, Token: "tok", Timeout: time.Second,
		RetryMax: 1, RetryBaseDelay: 100 * time.Millisecond, BreakerFailures: 5,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = c.Do(ctx, http.MethodGet, "recipe/", nil, nil)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "not enough operation budget") {
		t.Fatalf("err = %v, want retry-budget deadline error", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want no second upstream attempt", attempts)
	}
}

func TestDoCapturesRetryAfterOn429(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `slow down`)
	})
	_, err := c.Do(context.Background(), http.MethodGet, "recipe/", nil, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %T %[1]v, want APIError", err)
	}
	if apiErr.RetryAfter != 3*time.Second || apiErr.Body != "slow down" || !isTemporaryFailure(err) {
		t.Fatalf("apiErr = %+v temporary=%v, want retry-after/body/temporary", apiErr, isTemporaryFailure(err))
	}
}

func TestUploadFieldsOnlySendsMultipart(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotImageURL string
	var fileParts int
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mt != "multipart/form-data" {
			t.Fatalf("content-type = %q, err = %v", r.Header.Get("Content-Type"), err)
		}
		mr, err := r.MultipartReader()
		if err != nil {
			t.Fatalf("MultipartReader: %v", err)
		}
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("NextPart: %v", err)
			}
			data, _ := io.ReadAll(part)
			if part.FileName() != "" {
				fileParts++
			}
			if part.FormName() == "image_url" {
				gotImageURL = string(data)
			}
		}
		_, _ = io.WriteString(w, `{}`)
	})
	_, err := c.Upload(context.Background(), http.MethodPut, "recipe/5/image/",
		map[string]string{"image_url": "https://images.example.com/pie.png"}, "", "", nil)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if gotMethod != http.MethodPut || gotPath != "/api/recipe/5/image/" || gotAuth != "Bearer secret-token" {
		t.Fatalf("request = %s %s auth=%q", gotMethod, gotPath, gotAuth)
	}
	if gotImageURL != "https://images.example.com/pie.png" || fileParts != 0 {
		t.Fatalf("image_url=%q fileParts=%d, want field-only upload", gotImageURL, fileParts)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

func TestUploadReaderErrorDoesNotAttemptRequest(t *testing.T) {
	var attempted int32
	c := newTestClient(t, func(http.ResponseWriter, *http.Request) {
		atomic.AddInt32(&attempted, 1)
	})
	_, err := c.Upload(context.Background(), http.MethodPut, "recipe/5/image/",
		nil, "image", "bad.png", failingReader{})
	if err == nil || !strings.Contains(err.Error(), "copying file") {
		t.Fatalf("err = %v, want local copy error", err)
	}
	if attempted != 0 {
		t.Fatalf("attempted = %d, upload reader errors must not reach upstream", attempted)
	}
}

func TestOutcomeUnknownErrorMessageAndUnwrap(t *testing.T) {
	cause := errors.New("context canceled")
	err := &OutcomeUnknownError{Method: http.MethodPost, Path: "recipe/", Cause: cause}
	if !strings.Contains(err.Error(), "POST recipe/") || !errors.Is(err, cause) {
		t.Fatalf("err = %q unwrap=%v", err.Error(), errors.Unwrap(err))
	}
}

func TestEndpointPreservesBasePathAndEscapesQuery(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{BaseURL: srv.URL + "/tandoor", Token: "tok"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	q := url.Values{"query": {"soup & stew"}, "tag": {"a/b"}}
	if _, err := c.Do(context.Background(), http.MethodGet, "recipe/", q, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if gotPath != "/tandoor/api/recipe/" {
		t.Fatalf("path = %q, want /tandoor/api/recipe/", gotPath)
	}
	if gotQuery.Get("query") != "soup & stew" || gotQuery.Get("tag") != "a/b" {
		t.Fatalf("query = %v, want escaped values decoded by server", gotQuery)
	}
}

func TestConfigFromEnvRejectsMissingTokenAndBadResilienceValues(t *testing.T) {
	t.Setenv("TANDOOR_URL", "https://recipes.example.com")
	t.Setenv("TANDOOR_TOKEN", "")
	t.Setenv("TANDOOR_API_TOKEN", "")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("expected missing token rejection")
	}

	for _, tc := range []struct {
		name string
		env  string
		val  string
	}{
		{name: "bad max concurrency", env: "TANDOOR_MAX_CONCURRENCY", val: "0"},
		{name: "bad retry max", env: "TANDOOR_RETRY_MAX", val: "6"},
		{name: "bad retry base", env: "TANDOOR_RETRY_BASE_MS", val: "0"},
		{name: "bad breaker failures", env: "TANDOOR_BREAKER_FAILURES", val: "0"},
		{name: "bad breaker cooldown", env: "TANDOOR_BREAKER_COOLDOWN_SECONDS", val: "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TANDOOR_URL", "https://recipes.example.com")
			t.Setenv("TANDOOR_TOKEN", "tok")
			t.Setenv(tc.env, tc.val)
			if _, err := ConfigFromEnv(); err == nil {
				t.Fatalf("expected error for %s=%s", tc.env, tc.val)
			}
		})
	}
}

func TestNewRejectsUnsafeBaseURLShapes(t *testing.T) {
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	for _, cfg := range []Config{
		{BaseURL: "recipes.example.com", Token: "tok"},
		{BaseURL: "ftp://recipes.example.com", Token: "tok"},
		{BaseURL: "https://recipes.example.com", Token: ""},
		{BaseURL: "http://[2001:db8::1]", Token: "tok"},
		{BaseURL: "http://", Token: "tok"},
	} {
		if _, err := New(cfg); err == nil {
			t.Fatalf("New(%+v) succeeded, want rejection", cfg)
		}
	}
}

func TestRetryAfterHTTPDateAndPastDate(t *testing.T) {
	future := time.Now().Add(2 * time.Second).UTC().Format(http.TimeFormat)
	if d := parseRetryAfter(future); d <= 0 || d > 3*time.Second {
		t.Fatalf("future Retry-After parsed as %s, want a small positive duration", d)
	}
	past := time.Now().Add(-time.Second).UTC().Format(http.TimeFormat)
	if d := parseRetryAfter(past); d != 0 {
		t.Fatalf("past Retry-After parsed as %s, want zero", d)
	}
	if d := parseRetryAfter("not a date"); d != 0 {
		t.Fatalf("bad Retry-After parsed as %s, want zero", d)
	}
}
