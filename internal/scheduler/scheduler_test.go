package scheduler_test

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/PraisejahOsumgbaBenson/pulse/internal/feeds"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/generate"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/scheduler"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/store"
)

type fakeNotifier struct {
	owner string
	cards []int64
	texts []string
}

func (f *fakeNotifier) Owner(context.Context) (string, error) { return f.owner, nil }
func (f *fakeNotifier) SendDraftCard(_ context.Context, _ string, draftID int64) error {
	f.cards = append(f.cards, draftID)
	return nil
}
func (f *fakeNotifier) SendText(_ context.Context, _ string, text string) error {
	f.texts = append(f.texts, text)
	return nil
}

type fakePublisher struct {
	published []int64
	urn       string
}

func (f *fakePublisher) Publish(_ context.Context, draftID int64) (string, error) {
	f.published = append(f.published, draftID)
	return f.urn, nil
}

func setup(t *testing.T, owner string) (*store.Store, *feeds.Service, context.Context, *fakeNotifier, *fakePublisher, *scheduler.Service) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	f := feeds.New(st, slog.Default())
	gen := generate.New("", "", "", "", slog.Default())
	n := &fakeNotifier{owner: owner}
	p := &fakePublisher{urn: "urn:li:share:1"}
	svc := scheduler.New(st, f, gen, n, p, time.UTC, "UTC", time.Second, slog.Default())
	return st, f, ctx, n, p, svc
}

func seedArticle(t *testing.T, st *store.Store, ctx context.Context, url, title string) {
	t.Helper()
	src, err := st.AddSource(ctx, store.SourceRSS, "https://example.com/feed", "Example")
	if err != nil {
		t.Fatalf("AddSource() returned error: %v", err)
	}
	if _, err := st.AddArticle(ctx, store.Article{SourceID: src.ID, URL: url, Title: title, Summary: "Body text here."}); err != nil {
		t.Fatalf("AddArticle() returned error: %v", err)
	}
}

// Monday 2026-09-21 09:00 UTC.
var slotTime = time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)

func TestTickFiresScheduleOnce(t *testing.T) {
	st, _, ctx, n, p, svc := setup(t, "u1")
	seedArticle(t, st, ctx, "https://example.com/a", "Article A")
	if _, err := st.ReplaceSchedule(ctx, "1,3,5", 9, 0, false, "UTC"); err != nil {
		t.Fatalf("ReplaceSchedule() returned error: %v", err)
	}

	if err := svc.Tick(ctx, slotTime); err != nil {
		t.Fatalf("Tick() returned error: %v", err)
	}
	if len(n.cards) != 1 {
		t.Fatalf("cards sent = %d, want 1", len(n.cards))
	}
	if len(p.published) != 0 {
		t.Fatalf("published = %d, want 0 for manual schedule", len(p.published))
	}

	// Same minute again must not refire.
	if err := svc.Tick(ctx, slotTime.Add(20*time.Second)); err != nil {
		t.Fatalf("Tick() returned error: %v", err)
	}
	if len(n.cards) != 1 {
		t.Errorf("cards sent = %d, want still 1", len(n.cards))
	}

	// Wrong day must not fire.
	if err := svc.Tick(ctx, slotTime.Add(24*time.Hour)); err != nil {
		t.Fatalf("Tick() returned error: %v", err)
	}
	if len(n.cards) != 1 {
		t.Errorf("cards sent = %d, want still 1", len(n.cards))
	}
}

func TestTickAutopostsWhenEnabled(t *testing.T) {
	st, _, ctx, n, p, svc := setup(t, "u1")
	seedArticle(t, st, ctx, "https://example.com/a", "Article A")
	if _, err := st.ReplaceSchedule(ctx, "1", 9, 0, true, "UTC"); err != nil {
		t.Fatalf("ReplaceSchedule() returned error: %v", err)
	}
	if err := svc.Tick(ctx, slotTime); err != nil {
		t.Fatalf("Tick() returned error: %v", err)
	}
	if len(p.published) != 1 || len(n.cards) != 1 {
		t.Errorf("published = %d cards = %d, want 1 and 1", len(p.published), len(n.cards))
	}
}

func TestTickWarnsWhenNoArticles(t *testing.T) {
	// Fresh store, no sources at all: the cycle should say so over DM.
	st, _, ctx, n, _, svc := setup(t, "u1")
	if _, err := st.ReplaceSchedule(ctx, "1", 9, 0, false, "UTC"); err != nil {
		t.Fatalf("ReplaceSchedule() returned error: %v", err)
	}
	if err := svc.Tick(ctx, slotTime); err != nil {
		t.Fatalf("Tick() returned error: %v", err)
	}
	if len(n.texts) == 0 {
		t.Error("texts sent = 0, want a no-articles notice")
	}
}

func TestTickRefiresDueSnoozes(t *testing.T) {
	st, _, ctx, n, _, svc := setup(t, "u1")
	seedArticle(t, st, ctx, "https://example.com/a", "Article A")
	art, _ := st.NextUnusedArticle(ctx)
	d, _ := st.CreateDraft(ctx, art.ID, "snoozed text")
	if err := st.AddSnooze(ctx, d.ID, slotTime.Add(-time.Minute)); err != nil {
		t.Fatalf("AddSnooze() returned error: %v", err)
	}
	if err := svc.Tick(ctx, slotTime); err != nil {
		t.Fatalf("Tick() returned error: %v", err)
	}
	if len(n.cards) != 1 || n.cards[0] != d.ID {
		t.Errorf("cards = %v, want one re-announce of draft %d", n.cards, d.ID)
	}
	due, _ := st.DueSnoozes(ctx, slotTime)
	if len(due) != 0 {
		t.Errorf("due snoozes = %d, want 0 after fire", len(due))
	}
}

func TestTickWarnsAboutDyingTokenOnce(t *testing.T) {
	st, _, ctx, n, _, svc := setup(t, "u1")
	if err := st.SaveLinkedInToken(ctx, store.LinkedInToken{
		AccessToken: "a", PersonID: "urn:li:person:1",
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}); err != nil {
		t.Fatalf("SaveLinkedInToken() returned error: %v", err)
	}
	if err := svc.Tick(ctx, slotTime); err != nil {
		t.Fatalf("Tick() returned error: %v", err)
	}
	if err := svc.Tick(ctx, slotTime.Add(time.Minute)); err != nil {
		t.Fatalf("Tick() returned error: %v", err)
	}
	warnings := 0
	for _, text := range n.texts {
		if len(text) > 20 && text[:20] == "Your LinkedIn connec" {
			warnings++
		}
	}
	if warnings != 1 {
		t.Errorf("token warnings = %d, want exactly 1 across two ticks", warnings)
	}
}

func TestGenerateOneDraft(t *testing.T) {
	st, f, ctx, _, _, _ := setup(t, "u1")
	gen := generate.New("", "", "", "", slog.Default())
	if _, err := scheduler.GenerateOneDraft(ctx, st, f, gen); err == nil {
		t.Fatal("GenerateOneDraft(empty) = nil, want error")
	}
	seedArticle(t, st, ctx, "https://example.com/a", "Test article")
	id, err := scheduler.GenerateOneDraft(ctx, st, f, gen)
	if err != nil {
		t.Fatalf("GenerateOneDraft() returned error: %v", err)
	}
	d, err := st.GetDraft(ctx, id)
	if err != nil {
		t.Fatalf("GetDraft() returned error: %v", err)
	}
	if d.Status != store.DraftPending || d.Text == "" {
		t.Errorf("draft = %+v, want pending with text", d)
	}
	if n, _ := st.CountUnusedArticles(ctx); n != 0 {
		t.Errorf("CountUnusedArticles() = %d, want 0 after use", n)
	}
}
