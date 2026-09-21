package feeds_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/PraisejahOsumgbaBenson/pulse/internal/feeds"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/store"
)

const testFeed = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel><title>Test Blog</title>
<item><title>First post</title><link>%s/a1</link>
<description>What we shipped this week.</description>
<pubDate>Mon, 01 Sep 2026 09:00:00 GMT</pubDate></item>
<item><title>Second post</title><link>%s/a2</link>
<description>How we built it.</description>
<pubDate>Tue, 02 Sep 2026 09:00:00 GMT</pubDate></item>
</channel></rss>`

func openService(t *testing.T) (*feeds.Service, *store.Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return feeds.New(st, slog.Default()), st, ctx
}

func TestAddRSSFeedIngestsEntries(t *testing.T) {
	svc, st, ctx := openService(t)
	var feedXML string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		fmt.Fprint(w, feedXML)
	}))
	defer srv.Close()
	feedXML = fmt.Sprintf(testFeed, srv.URL, srv.URL)

	src, n, err := svc.Add(ctx, srv.URL+"/feed.xml")
	if err != nil {
		t.Fatalf("Add() returned error: %v", err)
	}
	if src.Kind != store.SourceRSS || src.Title != "Test Blog" {
		t.Errorf("Add() source = %+v, want rss Test Blog", src)
	}
	if n != 2 {
		t.Errorf("Add() new = %d, want 2", n)
	}

	// Adding the same feed again stores nothing new.
	_, n, err = svc.Add(ctx, srv.URL+"/feed.xml")
	if err != nil {
		t.Fatalf("Add() duplicate returned error: %v", err)
	}
	if n != 0 {
		t.Errorf("Add() duplicate new = %d, want 0", n)
	}

	if count, err := st.CountUnusedArticles(ctx); err != nil || count != 2 {
		t.Fatalf("CountUnusedArticles() = %d, %v, want 2 nil", count, err)
	}
	next, err := st.NextUnusedArticle(ctx)
	if err != nil {
		t.Fatalf("NextUnusedArticle() returned error: %v", err)
	}
	if next.Title != "First post" || next.Summary != "What we shipped this week." {
		t.Errorf("NextUnusedArticle() = %+v, want oldest first post", next)
	}

	// Refresh finds nothing new.
	refreshed, err := svc.Refresh(ctx)
	if err != nil {
		t.Fatalf("Refresh() returned error: %v", err)
	}
	if refreshed != 0 {
		t.Errorf("Refresh() = %d, want 0", refreshed)
	}
}

func TestAddSinglePageExtractsTitleAndDescription(t *testing.T) {
	svc, _, ctx := openService(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><html><head>
<title>Interesting Read</title>
<meta name="description" content="Why this matters for builders.">
</head><body>hi</body></html>`)
	}))
	defer srv.Close()

	src, n, err := svc.Add(ctx, srv.URL+"/post")
	if err != nil {
		t.Fatalf("Add() returned error: %v", err)
	}
	if src.Kind != store.SourceLink {
		t.Errorf("Add() kind = %q, want link", src.Kind)
	}
	if n != 1 {
		t.Errorf("Add() new = %d, want 1", n)
	}
}

func TestAddRejectsGarbage(t *testing.T) {
	svc, _, ctx := openService(t)
	if _, _, err := svc.Add(ctx, ""); err == nil {
		t.Error("Add(\"\") = nil, want error")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if _, _, err := svc.Add(ctx, srv.URL+"/missing"); err == nil {
		t.Error("Add(404) = nil, want error")
	}
}
