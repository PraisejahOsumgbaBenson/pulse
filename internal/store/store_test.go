package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/PraisejahOsumgbaBenson/pulse/internal/store"
)

func openTestStore(t *testing.T) (*store.Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st, ctx
}

func TestKVRoundTrip(t *testing.T) {
	st, ctx := openTestStore(t)
	if _, ok, err := st.KVGet(ctx, "missing"); err != nil || ok {
		t.Fatalf("KVGet(missing) = %v, %v, want empty miss", ok, err)
	}
	if err := st.KVSet(ctx, "owner", "123"); err != nil {
		t.Fatalf("KVSet() returned error: %v", err)
	}
	v, ok, err := st.KVGet(ctx, "owner")
	if err != nil || !ok || v != "123" {
		t.Fatalf("KVGet(owner) = %q, %v, %v, want 123 true nil", v, ok, err)
	}
}

func TestLinkedInTokenRoundTrip(t *testing.T) {
	st, ctx := openTestStore(t)
	if _, err := st.GetLinkedInToken(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetLinkedInToken() = %v, want sql.ErrNoRows", err)
	}
	tok := store.LinkedInToken{
		AccessToken:  "abc",
		RefreshToken: "r",
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
		Scope:        "openid profile w_member_social",
		PersonID:     "urn:li:person:1",
		PersonName:   "Test User",
	}
	if err := st.SaveLinkedInToken(ctx, tok); err != nil {
		t.Fatalf("SaveLinkedInToken() returned error: %v", err)
	}
	got, err := st.GetLinkedInToken(ctx)
	if err != nil {
		t.Fatalf("GetLinkedInToken() returned error: %v", err)
	}
	if got.AccessToken != "abc" || got.RefreshToken != "r" || got.PersonID != "urn:li:person:1" {
		t.Errorf("GetLinkedInToken() = %+v, want stored values", got)
	}
	if got.Expired() {
		t.Error("Expired() = true, want false for future expiry")
	}
	if !got.ExpiringSoon(2 * time.Hour) {
		t.Error("ExpiringSoon(2h) = false, want true")
	}
	if err := st.ClearLinkedInToken(ctx); err != nil {
		t.Fatalf("ClearLinkedInToken() returned error: %v", err)
	}
	if _, err := st.GetLinkedInToken(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetLinkedInToken() after clear = %v, want sql.ErrNoRows", err)
	}
	expired := store.LinkedInToken{AccessToken: "x", ExpiresAt: time.Now().Add(-time.Hour).Unix()}
	if !expired.Expired() {
		t.Error("Expired() = false, want true for past expiry")
	}
}

func TestOAuthStateConsumeOnce(t *testing.T) {
	st, ctx := openTestStore(t)
	want := store.OAuthState{State: "s1", DiscordUserID: "u1", Verifier: "v1"}
	if err := st.SaveOAuthState(ctx, want); err != nil {
		t.Fatalf("SaveOAuthState() returned error: %v", err)
	}
	got, err := st.ConsumeOAuthState(ctx, "s1")
	if err != nil {
		t.Fatalf("ConsumeOAuthState() returned error: %v", err)
	}
	if got.DiscordUserID != "u1" || got.Verifier != "v1" {
		t.Errorf("ConsumeOAuthState() = %+v, want stored values", got)
	}
	if _, err := st.ConsumeOAuthState(ctx, "s1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second ConsumeOAuthState() = %v, want sql.ErrNoRows", err)
	}
}

func TestSourcesAndArticles(t *testing.T) {
	st, ctx := openTestStore(t)
	src, err := st.AddSource(ctx, store.SourceRSS, "https://example.com/feed", "Example")
	if err != nil {
		t.Fatalf("AddSource() returned error: %v", err)
	}
	again, err := st.AddSource(ctx, store.SourceRSS, "https://example.com/feed", "Renamed")
	if err != nil {
		t.Fatalf("AddSource() duplicate returned error: %v", err)
	}
	if again.ID != src.ID || again.Title != "Renamed" {
		t.Errorf("AddSource() duplicate = %+v, want same id with updated title", again)
	}
	sources, err := st.ListSources(ctx)
	if err != nil || len(sources) != 1 {
		t.Fatalf("ListSources() = %v, %v, want 1 row", sources, err)
	}

	a1 := store.Article{SourceID: src.ID, URL: "https://example.com/a1", Title: "A1", Summary: "first"}
	a2 := store.Article{SourceID: src.ID, URL: "https://example.com/a2", Title: "A2", Summary: "second"}
	if _, err := st.AddArticle(ctx, a1); err != nil {
		t.Fatalf("AddArticle() returned error: %v", err)
	}
	if inserted, err := st.AddArticle(ctx, a1); err != nil || inserted {
		t.Fatalf("AddArticle() duplicate = inserted %v, %v, want false nil", inserted, err)
	}
	if _, err := st.AddArticle(ctx, a2); err != nil {
		t.Fatalf("AddArticle() returned error: %v", err)
	}
	if n, err := st.CountUnusedArticles(ctx); err != nil || n != 2 {
		t.Fatalf("CountUnusedArticles() = %d, %v, want 2 nil", n, err)
	}

	next, err := st.NextUnusedArticle(ctx)
	if err != nil {
		t.Fatalf("NextUnusedArticle() returned error: %v", err)
	}
	if next.Title != "A1" {
		t.Errorf("NextUnusedArticle() = %q, want oldest A1", next.Title)
	}
	if err := st.MarkArticleUsed(ctx, next.ID); err != nil {
		t.Fatalf("MarkArticleUsed() returned error: %v", err)
	}
	next, err = st.NextUnusedArticle(ctx)
	if err != nil {
		t.Fatalf("NextUnusedArticle() returned error: %v", err)
	}
	if next.Title != "A2" {
		t.Errorf("NextUnusedArticle() = %q, want A2", next.Title)
	}

	if err := st.RemoveSource(ctx, src.ID); err != nil {
		t.Fatalf("RemoveSource() returned error: %v", err)
	}
	if _, err := st.NextUnusedArticle(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("NextUnusedArticle() after source delete = %v, want sql.ErrNoRows", err)
	}
}

func TestDraftLifecycle(t *testing.T) {
	st, ctx := openTestStore(t)
	src, _ := st.AddSource(ctx, store.SourceRSS, "https://example.com/feed", "Example")
	if _, err := st.AddArticle(ctx, store.Article{SourceID: src.ID, URL: "https://example.com/a", Title: "A"}); err != nil {
		t.Fatalf("AddArticle() returned error: %v", err)
	}

	art, err := st.NextUnusedArticle(ctx)
	if err != nil {
		t.Fatalf("NextUnusedArticle() returned error: %v", err)
	}
	d, err := st.CreateDraft(ctx, art.ID, "hello world")
	if err != nil {
		t.Fatalf("CreateDraft() returned error: %v", err)
	}
	if d.Status != store.DraftPending || d.Text != "hello world" {
		t.Errorf("CreateDraft() = %+v, want pending hello world", d)
	}
	if err := st.UpdateDraftText(ctx, d.ID, "edited"); err != nil {
		t.Fatalf("UpdateDraftText() returned error: %v", err)
	}
	if err := st.SetDraftMessage(ctx, d.ID, "chan1", "msg1"); err != nil {
		t.Fatalf("SetDraftMessage() returned error: %v", err)
	}
	if err := st.UpdateDraftStatus(ctx, d.ID, store.DraftPosted, "urn:li:share:1", ""); err != nil {
		t.Fatalf("UpdateDraftStatus() returned error: %v", err)
	}
	got, err := st.GetDraft(ctx, d.ID)
	if err != nil {
		t.Fatalf("GetDraft() returned error: %v", err)
	}
	if got.Text != "edited" || got.Status != store.DraftPosted || got.LinkedInURN != "urn:li:share:1" {
		t.Errorf("GetDraft() = %+v, want edited posted urn", got)
	}
	if got.DiscordChannelID != "chan1" || got.DiscordMessageID != "msg1" {
		t.Errorf("GetDraft() message refs = %q %q, want chan1 msg1", got.DiscordChannelID, got.DiscordMessageID)
	}
	posted, err := st.ListDrafts(ctx, store.DraftPosted, 10)
	if err != nil || len(posted) != 1 {
		t.Fatalf("ListDrafts(posted) = %v, %v, want 1 row", posted, err)
	}
	pending, err := st.ListDrafts(ctx, store.DraftPending, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("ListDrafts(pending) = %v, %v, want 0 rows", pending, err)
	}
}

func TestScheduleReplaceAndFire(t *testing.T) {
	st, ctx := openTestStore(t)
	sc, err := st.ReplaceSchedule(ctx, "1,3,5", 9, 0, false, "UTC")
	if err != nil {
		t.Fatalf("ReplaceSchedule() returned error: %v", err)
	}
	if sc.Days != "1,3,5" || sc.Hour != 9 || sc.Autopost || !sc.Enabled {
		t.Errorf("ReplaceSchedule() = %+v, want 1,3,5 09:00 manual enabled", sc)
	}
	if err := st.SetScheduleAutopost(ctx, sc.ID, true); err != nil {
		t.Fatalf("SetScheduleAutopost() returned error: %v", err)
	}
	got, _ := st.GetSchedule(ctx, sc.ID)
	if !got.Autopost {
		t.Error("GetSchedule() autopost = false, want true")
	}
	if err := st.MarkScheduleFired(ctx, sc.ID, "2026-09-21T09:00"); err != nil {
		t.Fatalf("MarkScheduleFired() returned error: %v", err)
	}
	got, _ = st.GetSchedule(ctx, sc.ID)
	if got.LastFiredSlot != "2026-09-21T09:00" {
		t.Errorf("LastFiredSlot = %q, want slot key", got.LastFiredSlot)
	}
	again, err := st.ReplaceSchedule(ctx, "2,4", 18, 30, false, "UTC")
	if err != nil {
		t.Fatalf("ReplaceSchedule() returned error: %v", err)
	}
	all, err := st.ListSchedules(ctx, false)
	if err != nil || len(all) != 1 || all[0].ID != again.ID {
		t.Fatalf("ListSchedules() = %v, %v, want only the replacement", all, err)
	}
	if err := st.DisableSchedules(ctx); err != nil {
		t.Fatalf("DisableSchedules() returned error: %v", err)
	}
	enabled, err := st.ListSchedules(ctx, true)
	if err != nil || len(enabled) != 0 {
		t.Fatalf("ListSchedules(enabled) = %v, %v, want 0 rows", enabled, err)
	}
}

func TestSnoozeQueue(t *testing.T) {
	st, ctx := openTestStore(t)
	src, _ := st.AddSource(ctx, store.SourceRSS, "https://example.com/feed", "Example")
	if _, err := st.AddArticle(ctx, store.Article{SourceID: src.ID, URL: "https://example.com/a", Title: "A"}); err != nil {
		t.Fatalf("AddArticle() returned error: %v", err)
	}
	art, _ := st.NextUnusedArticle(ctx)
	d, _ := st.CreateDraft(ctx, art.ID, "text")

	fireAt := time.Now().Add(time.Hour).Round(time.Second)
	if err := st.AddSnooze(ctx, d.ID, fireAt); err != nil {
		t.Fatalf("AddSnooze() returned error: %v", err)
	}
	early, err := st.DueSnoozes(ctx, time.Now())
	if err != nil || len(early) != 0 {
		t.Fatalf("DueSnoozes(now) = %v, %v, want 0 rows", early, err)
	}
	due, err := st.DueSnoozes(ctx, fireAt.Add(time.Minute))
	if err != nil || len(due) != 1 || due[0].DraftID != d.ID {
		t.Fatalf("DueSnoozes(later) = %v, %v, want 1 row for draft", due, err)
	}
	if err := st.MarkSnoozeFired(ctx, due[0].ID); err != nil {
		t.Fatalf("MarkSnoozeFired() returned error: %v", err)
	}
	again, _ := st.DueSnoozes(ctx, fireAt.Add(time.Minute))
	if len(again) != 0 {
		t.Fatalf("DueSnoozes() after fire = %v, want 0 rows", again)
	}

	if err := st.AddSnooze(ctx, d.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("AddSnooze() returned error: %v", err)
	}
	if err := st.ClearSnoozesForDraft(ctx, d.ID); err != nil {
		t.Fatalf("ClearSnoozesForDraft() returned error: %v", err)
	}
	rest, _ := st.DueSnoozes(ctx, time.Now().Add(2*time.Hour))
	if len(rest) != 0 {
		t.Fatalf("DueSnoozes() after clear = %v, want 0 rows", rest)
	}
}
