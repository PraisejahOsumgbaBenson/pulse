package telegram

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PraisejahOsumgbaBenson/pulse/internal/feeds"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/generate"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/linkedin"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/publisher"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/store"
)

type fakeAPI struct {
	sent    []string
	markups []*InlineKeyboardMarkup
}

func (f *fakeAPI) GetMe(context.Context) (User, error) {
	return User{ID: 1, Username: "testbot"}, nil
}

func (f *fakeAPI) GetUpdates(context.Context, int, int) ([]Update, error) {
	return nil, nil
}

func (f *fakeAPI) SendMessage(_ context.Context, chatID int64, text string, markup *InlineKeyboardMarkup) (Message, error) {
	f.sent = append(f.sent, text)
	f.markups = append(f.markups, markup)
	return Message{MessageID: len(f.sent), Chat: Chat{ID: chatID}, Text: text}, nil
}

func (f *fakeAPI) EditMessageText(context.Context, int64, int, string, *InlineKeyboardMarkup) error {
	return nil
}

func (f *fakeAPI) AnswerCallbackQuery(context.Context, string, string) error { return nil }

func (f *fakeAPI) SetMyCommands(context.Context, []BotCommand) error { return nil }

func (f *fakeAPI) SendChatAction(context.Context, int64, string) error { return nil }

func testBot(t *testing.T) (*Bot, *store.Store, *fakeAPI, context.Context) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	logger := slog.Default()
	li := linkedin.New("id", "secret", "http://localhost:8081/oauth/linkedin/callback", "202602", logger)
	f := &fakeAPI{}
	b := &Bot{
		api: f,
		deps: Deps{
			Store:        st,
			Feeds:        feeds.New(st, logger),
			Gen:          generate.New("", "", "", "", logger),
			LinkedIn:     li,
			Pub:          publisher.New(st, li, logger),
			Location:     time.UTC,
			TimezoneName: "UTC",
			Logger:       logger,
		},
		logger: logger,
		stop:   make(chan struct{}),
	}
	return b, st, f, ctx
}

func TestPlainTextBecomesTopicDraft(t *testing.T) {
	b, st, f, ctx := testBot(t)
	b.onMessage(ctx, &Message{
		From: &User{ID: 7},
		Chat: Chat{ID: 7},
		Text: "my first week learning Go and shipping daily",
	})
	drafts, err := st.ListDrafts(ctx, store.DraftPending, 10)
	if err != nil || len(drafts) != 1 {
		t.Fatalf("drafts = %v, %v; want 1 pending draft", drafts, err)
	}
	if len(f.sent) == 0 || !strings.Contains(f.sent[len(f.sent)-1], "Draft #") {
		t.Errorf("sent = %v, want a draft card", f.sent)
	}
}

func TestShortTextGetsHintNotDraft(t *testing.T) {
	b, st, f, ctx := testBot(t)
	b.onMessage(ctx, &Message{From: &User{ID: 7}, Chat: Chat{ID: 7}, Text: "hi"})
	drafts, _ := st.ListDrafts(ctx, store.DraftPending, 10)
	if len(drafts) != 0 {
		t.Errorf("drafts = %d, want 0 for a two-letter message", len(drafts))
	}
	if len(f.sent) == 0 {
		t.Error("sent nothing, want a hint message")
	}
}

func TestLinkExplainsWhenUnconfigured(t *testing.T) {
	b, _, f, ctx := testBot(t)
	b.onMessage(ctx, &Message{From: &User{ID: 7}, Chat: Chat{ID: 7}, Text: "/link"})
	got := f.sent[len(f.sent)-1]
	if !strings.Contains(got, "isn't set up") {
		t.Errorf("reply = %q, want not-set-up explanation", got)
	}
}

func TestScheduleConversation(t *testing.T) {
	b, st, f, ctx := testBot(t)
	say := func(text string) string {
		before := len(f.sent)
		b.onMessage(ctx, &Message{From: &User{ID: 7}, Chat: Chat{ID: 7}, Text: text})
		if len(f.sent) <= before {
			t.Fatalf("no reply to %q", text)
		}
		return f.sent[len(f.sent)-1]
	}
	if got := say("/schedule"); !strings.Contains(got, "What time") {
		t.Fatalf("ask time = %q", got)
	}
	if got := say("3pm"); !strings.Contains(got, "Which days") {
		t.Fatalf("ask days = %q", got)
	}
	if got := say("weekdays"); !strings.Contains(strings.ToLower(got), "automatically") {
		t.Fatalf("ask autopost = %q", got)
	}
	if got := say("yes"); !strings.Contains(got, "Reminders set") {
		t.Fatalf("confirm = %q", got)
	}
	schedules, err := st.ListSchedules(ctx, false)
	if err != nil || len(schedules) != 1 {
		t.Fatalf("schedules = %v, %v; want 1", schedules, err)
	}
	sc := schedules[0]
	if sc.Hour != 15 || sc.Minute != 0 || sc.Days != "1,2,3,4,5" || !sc.Autopost {
		t.Errorf("schedule = %+v, want 15:00 weekdays autopost", sc)
	}
}

func TestSchedulePartialArgsSkipsToDays(t *testing.T) {
	b, _, f, ctx := testBot(t)
	b.onMessage(ctx, &Message{From: &User{ID: 7}, Chat: Chat{ID: 7}, Text: "/schedule_set 09:00"})
	got := f.sent[len(f.sent)-1]
	if !strings.Contains(got, "Which days") {
		t.Errorf("reply = %q, want days question", got)
	}
}

func TestTrendingPickDraftsNewest(t *testing.T) {
	b, st, f, ctx := testBot(t)
	src, _ := st.AddSource(ctx, store.SourceRSS, "https://example.com/feed", "Example")
	if _, err := st.AddArticle(ctx, store.Article{SourceID: src.ID, URL: "https://example.com/a1", Title: "Hot story one", Summary: "s1", PublishedAt: 100}); err != nil {
		t.Fatalf("AddArticle() returned error: %v", err)
	}
	if _, err := st.AddArticle(ctx, store.Article{SourceID: src.ID, URL: "https://example.com/a2", Title: "Hot story two", Summary: "s2", PublishedAt: 200}); err != nil {
		t.Fatalf("AddArticle() returned error: %v", err)
	}
	b.onMessage(ctx, &Message{From: &User{ID: 7}, Chat: Chat{ID: 7}, Text: "/trending"})
	list := f.sent[len(f.sent)-1]
	if !strings.Contains(list, "Hot story one") || !strings.Contains(list, "Hot story two") {
		t.Fatalf("trending list = %q, want both stories", list)
	}
	b.onMessage(ctx, &Message{From: &User{ID: 7}, Chat: Chat{ID: 7}, Text: "1"})
	drafts, _ := st.ListDrafts(ctx, store.DraftPending, 10)
	if len(drafts) != 1 || !strings.Contains(drafts[0].Text, "Hot story two") {
		t.Errorf("drafts = %+v, want 1 draft about the newest pick", drafts)
	}
}

func TestTrendingEmptyOffersPack(t *testing.T) {
	b, _, f, ctx := testBot(t)
	b.onMessage(ctx, &Message{From: &User{ID: 7}, Chat: Chat{ID: 7}, Text: "/trending"})
	last := f.sent[len(f.sent)-1]
	if !strings.Contains(last, "trending starter kit") {
		t.Errorf("reply = %q, want starter kit offer", last)
	}
	mk := f.markups[len(f.markups)-1]
	if mk == nil || len(mk.InlineKeyboard) == 0 || mk.InlineKeyboard[0][0].CallbackData != "pulse:pack" {
		t.Errorf("keyboard = %+v, want pack button", mk)
	}
}
