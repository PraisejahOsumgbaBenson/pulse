package publisher_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/PraisejahOsumgbaBenson/pulse/internal/linkedin"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/publisher"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/store"
)

func setup(t *testing.T, postsHandler http.HandlerFunc) (*publisher.Publisher, *store.Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	var postsURL, tokenURL string
	if postsHandler != nil {
		srv := httptest.NewServer(postsHandler)
		t.Cleanup(srv.Close)
		postsURL = srv.URL
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "fresh", "expires_in": 3600,
		})
	})
	tok := httptest.NewServer(mux)
	t.Cleanup(tok.Close)
	tokenURL = tok.URL + "/token"
	if postsURL == "" {
		postsURL = tok.URL + "/unused"
	}

	li := linkedin.NewWithEndpoints("id", "secret", "http://localhost/cb", "202602",
		linkedin.Endpoints{TokenURL: tokenURL, UserInfoURL: tok.URL, PostsURL: postsURL},
		slog.Default())
	return publisher.New(st, li, slog.Default()), st, ctx
}

func seedDraft(t *testing.T, st *store.Store, ctx context.Context) int64 {
	t.Helper()
	src, err := st.AddSource(ctx, store.SourceRSS, "https://example.com/feed", "Example")
	if err != nil {
		t.Fatalf("AddSource() returned error: %v", err)
	}
	if _, err := st.AddArticle(ctx, store.Article{SourceID: src.ID, URL: "https://example.com/a", Title: "A"}); err != nil {
		t.Fatalf("AddArticle() returned error: %v", err)
	}
	art, err := st.NextUnusedArticle(ctx)
	if err != nil {
		t.Fatalf("NextUnusedArticle() returned error: %v", err)
	}
	d, err := st.CreateDraft(ctx, art.ID, "hello linkedin")
	if err != nil {
		t.Fatalf("CreateDraft() returned error: %v", err)
	}
	return d.ID
}

func saveToken(t *testing.T, st *store.Store, ctx context.Context, tok store.LinkedInToken) {
	t.Helper()
	if err := st.SaveLinkedInToken(ctx, tok); err != nil {
		t.Fatalf("SaveLinkedInToken() returned error: %v", err)
	}
}

func TestPublishHappyPath(t *testing.T) {
	pub, st, ctx := setup(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-restli-id", "urn:li:share:7")
		w.WriteHeader(http.StatusCreated)
	})
	id := seedDraft(t, st, ctx)
	saveToken(t, st, ctx, store.LinkedInToken{
		AccessToken: "a", PersonID: "urn:li:person:1",
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	})

	urn, err := pub.Publish(ctx, id)
	if err != nil {
		t.Fatalf("Publish() returned error: %v", err)
	}
	if urn != "urn:li:share:7" {
		t.Errorf("Publish() urn = %q, want urn:li:share:7", urn)
	}
	d, _ := st.GetDraft(ctx, id)
	if d.Status != store.DraftPosted || d.LinkedInURN != "urn:li:share:7" {
		t.Errorf("draft after publish = %+v, want posted with urn", d)
	}
}

func TestPublishWithoutConnection(t *testing.T) {
	pub, st, ctx := setup(t, nil)
	id := seedDraft(t, st, ctx)
	if _, err := pub.Publish(ctx, id); !errors.Is(err, publisher.ErrNotConnected) {
		t.Errorf("Publish() = %v, want ErrNotConnected", err)
	}
}

func TestPublishExpiredWithoutRefresh(t *testing.T) {
	pub, st, ctx := setup(t, nil)
	id := seedDraft(t, st, ctx)
	saveToken(t, st, ctx, store.LinkedInToken{
		AccessToken: "old", PersonID: "urn:li:person:1",
		ExpiresAt: time.Now().Add(-time.Hour).Unix(),
	})
	if _, err := pub.Publish(ctx, id); !errors.Is(err, publisher.ErrTokenExpired) {
		t.Errorf("Publish() = %v, want ErrTokenExpired", err)
	}
}

func TestPublishRefreshesExpiredToken(t *testing.T) {
	pub, st, ctx := setup(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fresh" {
			t.Errorf("Authorization = %q, want refreshed Bearer fresh", r.Header.Get("Authorization"))
		}
		w.Header().Set("x-restli-id", "urn:li:share:8")
		w.WriteHeader(http.StatusCreated)
	})
	id := seedDraft(t, st, ctx)
	saveToken(t, st, ctx, store.LinkedInToken{
		AccessToken: "old", RefreshToken: "r1", PersonID: "urn:li:person:1",
		ExpiresAt: time.Now().Add(-time.Hour).Unix(),
	})
	urn, err := pub.Publish(ctx, id)
	if err != nil {
		t.Fatalf("Publish() returned error: %v", err)
	}
	if urn != "urn:li:share:8" {
		t.Errorf("Publish() urn = %q, want urn:li:share:8", urn)
	}
	tok, _ := st.GetLinkedInToken(ctx)
	if tok.AccessToken != "fresh" {
		t.Errorf("stored access token = %q, want refreshed fresh", tok.AccessToken)
	}
}

func TestPublishUnauthorizedMarksFailed(t *testing.T) {
	pub, st, ctx := setup(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"bad token","status":401}`))
	})
	id := seedDraft(t, st, ctx)
	saveToken(t, st, ctx, store.LinkedInToken{
		AccessToken: "bad", PersonID: "urn:li:person:1",
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	})
	if _, err := pub.Publish(ctx, id); !errors.Is(err, publisher.ErrTokenExpired) {
		t.Errorf("Publish() = %v, want ErrTokenExpired", err)
	}
	d, _ := st.GetDraft(ctx, id)
	if d.Status != store.DraftFailed {
		t.Errorf("draft status = %q, want failed", d.Status)
	}
}

func TestPublishRefusesNonPending(t *testing.T) {
	pub, st, ctx := setup(t, nil)
	id := seedDraft(t, st, ctx)
	st.UpdateDraftStatus(ctx, id, store.DraftPosted, "urn:li:share:1", "")
	if _, err := pub.Publish(ctx, id); err == nil {
		t.Error("Publish(posted) = nil, want error")
	}
}
