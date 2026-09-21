package generate_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/PraisejahOsumgbaBenson/pulse/internal/generate"
)

func TestNewPicksFallbackWithoutKey(t *testing.T) {
	if got := generate.New("https://api.openai.com/v1", "gpt-4o-mini", "", "", slog.Default()).Name(); got != "fallback" {
		t.Errorf("New() without key = %q, want fallback", got)
	}
	if got := generate.New("https://example.com/v1", "some-model", "sk-x", "", slog.Default()).Name(); !strings.Contains(got, "some-model") {
		t.Errorf("New() with key = %q, want model in name", got)
	}
}

func TestFallbackBuildsReadablePost(t *testing.T) {
	g := generate.New("", "", "", "", slog.Default())
	text, err := g.Generate(context.Background(), generate.Input{
		Title:   "How we scaled Postgres reads",
		Summary: "We added read replicas. P99 latency dropped by half. The rollout took one careful weekend.",
		URL:     "https://example.com/scaled-postgres",
	})
	if err != nil {
		t.Fatalf("Generate() returned error: %v", err)
	}
	for _, want := range []string{
		"How we scaled Postgres reads",
		"https://example.com/scaled-postgres",
		"#postgres",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("Generate() = %q, missing %q", text, want)
		}
	}
	if n := utf8.RuneCountInString(text); n > 2900 {
		t.Errorf("RuneCountInString() = %d, want at most 2900", n)
	}
}

func TestFallbackNeedsSomethingToSay(t *testing.T) {
	g := generate.New("", "", "", "", slog.Default())
	if _, err := g.Generate(context.Background(), generate.Input{}); err == nil {
		t.Error("Generate(empty) = nil, want error")
	}
}

func TestOpenAICompatPostsAndTrims(t *testing.T) {
	var gotModel string
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		gotModel, _ = body["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "  hello linkedin  "}}},
		})
	}))
	defer srv.Close()

	g := generate.New(srv.URL, "test-model", "sk-test", "", slog.Default())
	text, err := g.Generate(context.Background(), generate.Input{Title: "T", Summary: "S"})
	if err != nil {
		t.Fatalf("Generate() returned error: %v", err)
	}
	if text != "hello linkedin" {
		t.Errorf("Generate() = %q, want trimmed content", text)
	}
	if gotModel != "test-model" {
		t.Errorf("model sent = %q, want test-model", gotModel)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want Bearer sk-test", gotAuth)
	}
}

func TestOpenAICompatSurfacesServerErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer srv.Close()

	g := generate.New(srv.URL, "m", "bad", "", slog.Default())
	if _, err := g.Generate(context.Background(), generate.Input{Title: "T"}); err == nil {
		t.Error("Generate(401) = nil, want error")
	}

	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[]}`))
	}))
	defer empty.Close()
	g2 := generate.New(empty.URL, "m", "k", "", slog.Default())
	if _, err := g2.Generate(context.Background(), generate.Input{Title: "T"}); err == nil {
		t.Error("Generate(no choices) = nil, want error")
	}
}
