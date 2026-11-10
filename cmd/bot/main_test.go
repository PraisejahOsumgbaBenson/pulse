package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/PraisejahOsumgbaBenson/pulse/internal/linkedin"
)

func TestLoadDotEnv(t *testing.T) {
	const key = "PULSE_TEST_DOTENV"
	os.Unsetenv(key)
	t.Cleanup(func() { os.Unsetenv(key) })
	t.Chdir(t.TempDir())

	if err := os.WriteFile(".env", []byte(key+"=from-file\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	if err := loadDotEnv(); err != nil {
		t.Fatalf("loadDotEnv() returned error: %v", err)
	}
	if got := os.Getenv(key); got != "from-file" {
		t.Fatalf("%s = %q, want from-file", key, got)
	}
}

func TestLoadDotEnvMissingIsFine(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := loadDotEnv(); err != nil {
		t.Fatalf("loadDotEnv() without .env returned error: %v", err)
	}
}

func testHandler(linkedInConfigured bool, callbackPath string) http.Handler {
	cb := linkedin.NewCallbackServer(nil, nil, nil, slog.Default())
	return newHTTPHandler(cb, linkedInConfigured, callbackPath)
}

func TestHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	testHandler(false, "/").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != "ok\n" {
		t.Fatalf("body = %q, want %q", got, "ok\n")
	}
}

func TestLandingPage(t *testing.T) {
	rec := httptest.NewRecorder()
	testHandler(false, "/").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "Pulse is running") {
		t.Fatalf("body does not look like the landing page: %q", rec.Body.String())
	}
}

func TestUnknownPathReturnsNotFound(t *testing.T) {
	rec := httptest.NewRecorder()
	testHandler(false, "/").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestCallbackRouteStillWorks(t *testing.T) {
	rec := httptest.NewRecorder()
	testHandler(true, "/oauth/linkedin/callback").ServeHTTP(
		rec, httptest.NewRequest(http.MethodGet, "/oauth/linkedin/callback", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "Connection failed") {
		t.Fatalf("callback did not handle the request: %q", rec.Body.String())
	}
}

func TestCallbackAtRootKeepsTheRoute(t *testing.T) {
	rec := httptest.NewRecorder()
	testHandler(true, "/").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if !strings.Contains(rec.Body.String(), "Connection failed") {
		t.Fatalf("root callback was shadowed by the landing page: %q", rec.Body.String())
	}
}

func TestHealthzWorksWhenCallbackOwnsRoot(t *testing.T) {
	rec := httptest.NewRecorder()
	testHandler(true, "/").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK || rec.Body.String() != "ok\n" {
		t.Fatalf("health check = %d %q, want 200 ok", rec.Code, rec.Body.String())
	}
}
