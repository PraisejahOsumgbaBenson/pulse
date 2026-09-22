// Package scheduler fires reminder slots: it refreshes sources, generates
// drafts, announces them over Telegram, auto-posts when enabled, re-fires
// snoozes, and warns about dying LinkedIn tokens.
package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/PraisejahOsumgbaBenson/pulse/internal/feeds"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/generate"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/store"
)

// Notifier reaches the owner over Telegram. The bot implements it.
type Notifier interface {
	Owner(ctx context.Context) (int64, error)
	SendDraftCard(ctx context.Context, chatID int64, draftID int64) error
	SendText(ctx context.Context, chatID int64, text string) error
}

// Publisher posts approved drafts. See internal/publisher.
type Publisher interface {
	Publish(ctx context.Context, draftID int64) (string, error)
}

// Service runs the reminder loop.
type Service struct {
	store    *store.Store
	feeds    *feeds.Service
	gen      generate.Generator
	notify   Notifier
	pub      Publisher
	loc      *time.Location
	tzName   string
	interval time.Duration
	logger   *slog.Logger
}

// New builds a Service. interval is the tick period, typically 30 seconds.
func New(st *store.Store, f *feeds.Service, gen generate.Generator, notify Notifier, pub Publisher, loc *time.Location, tzName string, interval time.Duration, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: st, feeds: f, gen: gen, notify: notify, pub: pub,
		loc: loc, tzName: tzName, interval: interval, logger: logger}
}

// Run ticks until ctx is cancelled.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case at := <-ticker.C:
			if err := s.Tick(ctx, at); err != nil {
				s.logger.Warn("scheduler tick failed", "err", err)
			}
		}
	}
}

// Tick runs one pass: snoozes, schedules, token warnings.
func (s *Service) Tick(ctx context.Context, at time.Time) error {
	owner, err := s.notify.Owner(ctx)
	if err != nil {
		s.logger.Debug("scheduler skipped, no owner yet")
		return nil
	}
	_ = s.store.PruneOAuthStates(ctx, 2*time.Hour)

	for _, sn := range s.dueSnoozes(ctx, at) {
		d, err := s.store.GetDraft(ctx, sn.DraftID)
		if err != nil {
			s.logger.Warn("snoozed draft gone", "draft", sn.DraftID, "err", err)
			_ = s.store.MarkSnoozeFired(ctx, sn.ID)
			continue
		}
		if d.Status != store.DraftPending && d.Status != store.DraftFailed {
			_ = s.store.MarkSnoozeFired(ctx, sn.ID)
			continue
		}
		if err := s.notify.SendDraftCard(ctx, owner, d.ID); err != nil {
			s.logger.Warn("re-announce snoozed draft", "draft", d.ID, "err", err)
			continue
		}
		_ = s.store.MarkSnoozeFired(ctx, sn.ID)
	}

	schedules, err := s.store.ListSchedules(ctx, true)
	if err != nil {
		return fmt.Errorf("list schedules: %w", err)
	}
	for _, sc := range schedules {
		loc := s.loc
		if sc.Timezone != "" {
			if l, err := time.LoadLocation(sc.Timezone); err == nil {
				loc = l
			}
		}
		slot, due := slotDue(sc, at.In(loc))
		if !due {
			continue
		}
		if err := s.store.MarkScheduleFired(ctx, sc.ID, slot); err != nil {
			s.logger.Warn("mark schedule fired", "schedule", sc.ID, "err", err)
		}
		s.runCycle(ctx, owner, sc)
	}

	s.watchToken(ctx, owner, at)
	return nil
}

func (s *Service) dueSnoozes(ctx context.Context, at time.Time) []store.Snooze {
	due, err := s.store.DueSnoozes(ctx, at)
	if err != nil {
		s.logger.Warn("read snoozes", "err", err)
		return nil
	}
	return due
}

// runCycle produces one draft for a fired slot and announces it,
// auto-posting first when the schedule asks for it.
func (s *Service) runCycle(ctx context.Context, owner int64, sc store.Schedule) {
	id, err := GenerateOneDraft(ctx, s.store, s.feeds, s.gen)
	if err != nil {
		s.logger.Warn("scheduled generation failed", "err", err)
		_ = s.notify.SendText(ctx, owner, "Reminder time, but I could not make a draft: "+err.Error())
		return
	}
	if sc.Autopost {
		if _, err := s.pub.Publish(ctx, id); err != nil {
			s.logger.Warn("scheduled autopost failed", "draft", id, "err", err)
		}
	}
	if err := s.notify.SendDraftCard(ctx, owner, id); err != nil {
		s.logger.Warn("announce draft", "draft", id, "err", err)
	}
}

// watchToken warns once about expiring tokens and daily about dead ones.
func (s *Service) watchToken(ctx context.Context, owner int64, at time.Time) {
	tok, err := s.store.GetLinkedInToken(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		s.logger.Warn("read linkedin token", "err", err)
		return
	}
	if tok.Expired() {
		day := at.Format("2006-01-02")
		if seen, _, _ := s.store.KVGet(ctx, "token_expired_day"); seen == day {
			return
		}
		_ = s.notify.SendText(ctx, owner, "Your LinkedIn connection expired. Run /link to reconnect and keep reminders posting.")
		_ = s.store.KVSet(ctx, "token_expired_day", day)
		return
	}
	if tok.ExpiringSoon(72 * time.Hour) {
		mark := strconv.FormatInt(tok.ExpiresAt, 10)
		if seen, _, _ := s.store.KVGet(ctx, "token_warned_exp"); seen == mark {
			return
		}
		left := time.Until(time.Unix(tok.ExpiresAt, 0)).Round(time.Hour)
		_ = s.notify.SendText(ctx, owner, fmt.Sprintf("Your LinkedIn connection expires in %s. Run /link to reconnect before reminders start failing.", left))
		_ = s.store.KVSet(ctx, "token_warned_exp", mark)
	}
}

// GenerateOneDraft refreshes sources, picks the next unused article, asks the
// generator for text, stores the draft and marks the article used.
func GenerateOneDraft(ctx context.Context, st *store.Store, f *feeds.Service, gen generate.Generator) (int64, error) {
	if _, err := f.Refresh(ctx); err != nil {
		return 0, fmt.Errorf("refresh sources: %w", err)
	}
	art, err := st.NextUnusedArticle(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("no fresh articles left. Add sources with /source_add")
	}
	if err != nil {
		return 0, fmt.Errorf("pick article: %w", err)
	}
	text, err := gen.Generate(ctx, generate.Input{Title: art.Title, Summary: firstNonEmpty(art.Summary, art.Body), URL: art.URL})
	if err != nil {
		return 0, fmt.Errorf("generate draft: %w", err)
	}
	d, err := st.CreateDraft(ctx, art.ID, text)
	if err != nil {
		return 0, fmt.Errorf("store draft: %w", err)
	}
	if err := st.MarkArticleUsed(ctx, art.ID); err != nil {
		return 0, fmt.Errorf("mark article used: %w", err)
	}
	return d.ID, nil
}

// topicSourceURL is the stable pseudo-source topic thoughts file under.
// Refresh only touches RSS sources, so it never fetches this.
const topicSourceURL = "telegram:topics"

// GenerateTopicDraft turns a free-text thought into a draft with no feed
// involved. The thought is stored under a dedicated topic source and its
// article is marked used at once, so topic drafts never leak into
// scheduled picks.
func GenerateTopicDraft(ctx context.Context, st *store.Store, gen generate.Generator, thought string) (int64, error) {
	thought = strings.TrimSpace(thought)
	if thought == "" {
		return 0, fmt.Errorf("give a thought after /topic, e.g. /topic my first week learning Go")
	}
	if n := len([]rune(thought)); n > 2000 {
		return 0, fmt.Errorf("that thought is %d characters, keep it under 2000", n)
	}
	src, err := st.AddSource(ctx, store.SourceTopic, topicSourceURL, "Your topics")
	if err != nil {
		return 0, fmt.Errorf("topic source: %w", err)
	}
	title := topicTitle(thought)
	artID, _, err := st.AddArticleID(ctx, store.Article{
		SourceID: src.ID,
		URL:      fmt.Sprintf("topic:%d", time.Now().UnixNano()),
		Title:    title,
		Summary:  thought,
	})
	if err != nil {
		return 0, fmt.Errorf("store thought: %w", err)
	}
	text, err := gen.Generate(ctx, generate.Input{Title: title, Summary: thought})
	if err != nil {
		return 0, fmt.Errorf("generate draft: %w", err)
	}
	d, err := st.CreateDraft(ctx, artID, text)
	if err != nil {
		return 0, fmt.Errorf("store draft: %w", err)
	}
	if err := st.MarkArticleUsed(ctx, artID); err != nil {
		return 0, fmt.Errorf("mark article used: %w", err)
	}
	return d.ID, nil
}

// GenerateDraftFromArticle drafts one specific article, e.g. a trending
// pick, and marks it used.
func GenerateDraftFromArticle(ctx context.Context, st *store.Store, gen generate.Generator, articleID int64) (int64, error) {
	art, err := st.GetArticle(ctx, articleID)
	if err != nil {
		return 0, fmt.Errorf("pick article: %w", err)
	}
	if art.Used {
		return 0, fmt.Errorf("that one is already used, pick another")
	}
	text, err := gen.Generate(ctx, generate.Input{Title: art.Title, Summary: firstNonEmpty(art.Summary, art.Body), URL: art.URL})
	if err != nil {
		return 0, fmt.Errorf("generate draft: %w", err)
	}
	d, err := st.CreateDraft(ctx, art.ID, text)
	if err != nil {
		return 0, fmt.Errorf("store draft: %w", err)
	}
	if err := st.MarkArticleUsed(ctx, art.ID); err != nil {
		return 0, fmt.Errorf("mark article used: %w", err)
	}
	return d.ID, nil
}

// topicTitle squeezes a thought into a one-line title.
func topicTitle(thought string) string {
	line, _, _ := strings.Cut(thought, "\n")
	title := strings.Join(strings.Fields(line), " ")
	if r := []rune(title); len(r) > 80 {
		return string(r[:79]) + "…"
	}
	return title
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// slotDue reports whether a schedule fires at now, and the slot key.
// The key guards against firing twice within the same minute.
func slotDue(sc store.Schedule, now time.Time) (string, bool) {
	if now.Hour() != sc.Hour || now.Minute() != sc.Minute {
		return "", false
	}
	want := false
	for _, tok := range strings.Split(sc.Days, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(tok))
		if err != nil || n < 0 || n > 6 {
			continue
		}
		if time.Weekday(n) == now.Weekday() {
			want = true
			break
		}
	}
	if !want {
		return "", false
	}
	key := now.Format("2006-01-02T15:04")
	if sc.LastFiredSlot == key {
		return "", false
	}
	return key, true
}
