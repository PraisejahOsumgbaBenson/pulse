package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PraisejahOsumgbaBenson/pulse/internal/linkedin"
)

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
