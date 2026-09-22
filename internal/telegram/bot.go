package telegram

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/PraisejahOsumgbaBenson/pulse/internal/feeds"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/generate"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/linkedin"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/publisher"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/store"
)

// ErrNoOwner means nobody claimed the bot yet, so scheduled DMs have
// nowhere to go. Send /start first.
var ErrNoOwner = errors.New("no owner recorded, send /start first")

// pollTimeoutSecs is the long-poll wait per getUpdates call.
const pollTimeoutSecs = 30

// retryPause is the breather between failed poll attempts.
const retryPause = 3 * time.Second

// Deps wires the bot to the rest of Pulse.
type Deps struct {
	Store              *store.Store
	Feeds              *feeds.Service
	Gen                generate.Generator
	LinkedIn           *linkedin.Client
	LinkedInConfigured bool
	Pub                *publisher.Publisher
	Location           *time.Location
	TimezoneName       string
	OwnerID            string
	Logger             *slog.Logger
}

// Bot is a running Telegram bot with Pulse handlers.
type Bot struct {
	api     API
	deps    Deps
	ownerID int64
	logger  *slog.Logger
	stop    chan struct{}
	wg      sync.WaitGroup
	once    sync.Once
}

// New creates a Bot without connecting. ownerID is the optional
// TELEGRAM_OWNER_ID value; empty means the first /start claims the bot.
func New(token string, deps Deps) (*Bot, error) {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	var ownerID int64
	if deps.OwnerID != "" {
		n, err := strconv.ParseInt(deps.OwnerID, 10, 64)
		if err != nil || n == 0 {
			return nil, fmt.Errorf("invalid TELEGRAM_OWNER_ID %q: want a numeric user id", deps.OwnerID)
		}
		ownerID = n
	}
	if token == "" {
		return nil, fmt.Errorf("telegram token is empty")
	}
	return &Bot{api: NewClient(token), deps: deps, ownerID: ownerID,
		logger: deps.Logger, stop: make(chan struct{})}, nil
}

// defaultCommands is the phone command menu.
func defaultCommands() []BotCommand {
	return []BotCommand{
		{Command: "start", Description: "Claim the bot and see what Pulse does"},
		{Command: "link", Description: "Connect your LinkedIn account"},
		{Command: "status", Description: "LinkedIn, schedule, sources and drafts at a glance"},
		{Command: "draft", Description: "Generate a draft right now"},
		{Command: "topic", Description: "Draft from a thought, e.g. my first Go project"},
		{Command: "trending", Description: "See what people are talking about"},
		{Command: "history", Description: "Recent drafts and posts"},
		{Command: "schedule", Description: "Set reminders step by step"},
		{Command: "schedule_set", Description: "Set reminders, e.g. 09:00 monday wednesday friday"},
		{Command: "schedule_status", Description: "Show the current schedule"},
		{Command: "schedule_off", Description: "Stop reminders"},
		{Command: "source_add", Description: "Add an RSS feed or page"},
		{Command: "source_list", Description: "List your sources"},
		{Command: "source_remove", Description: "Remove a source by id"},
		{Command: "autopost", Description: "Post without approval, true or false"},
		{Command: "cancel", Description: "Drop a pending edit"},
		{Command: "help", Description: "Command list"},
	}
}

// Start verifies the token, publishes the command menu, skips stale
// updates, and runs the poll loop until ctx ends or Stop is called.
func (b *Bot) Start(ctx context.Context) error {
	me, err := b.api.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("verify telegram token: %w", err)
	}
	b.logger.Info("telegram bot verified", "username", me.Username)
	if err := b.api.SetMyCommands(ctx, defaultCommands()); err != nil {
		b.logger.Warn("publish command menu", "err", err)
	}
	offset := 0
	if updates, err := b.api.GetUpdates(ctx, 0, 0); err == nil {
		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
		}
	} else {
		b.logger.Warn("skip backlog", "err", err)
	}
	b.wg.Add(1)
	go b.poll(ctx, offset)
	return nil
}

// Stop halts the poll loop.
func (b *Bot) Stop() {
	b.once.Do(func() { close(b.stop) })
	b.wg.Wait()
}

func (b *Bot) poll(ctx context.Context, offset int) {
	defer b.wg.Done()
	for {
		select {
		case <-b.stop:
			return
		case <-ctx.Done():
			return
		default:
		}
		updates, err := b.api.GetUpdates(ctx, offset, pollTimeoutSecs)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			select {
			case <-b.stop:
				return
			case <-ctx.Done():
				return
			case <-time.After(retryPause):
			}
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			b.handleUpdate(ctx, u)
		}
	}
}

func (b *Bot) handleUpdate(ctx context.Context, u Update) {
	if u.CallbackQuery != nil {
		b.onCallback(ctx, u.CallbackQuery)
		return
	}
	if u.Message != nil {
		b.onMessage(ctx, u.Message)
	}
}

// effectiveOwner is the configured owner, else the claiming user, else zero.
func (b *Bot) effectiveOwner(ctx context.Context) int64 {
	if b.ownerID != 0 {
		return b.ownerID
	}
	if id, ok, err := b.deps.Store.KVGet(ctx, "owner_telegram_id"); err == nil && ok {
		if n, err := strconv.ParseInt(id, 10, 64); err == nil && n != 0 {
			return n
		}
	}
	return 0
}

// allowed gates every update to the owner once one exists.
func (b *Bot) allowed(ctx context.Context, userID int64) bool {
	owner := b.effectiveOwner(ctx)
	return owner == 0 || userID == owner
}

// claimOwner records the sender as the DM target when no owner exists yet.
func (b *Bot) claimOwner(ctx context.Context, userID int64) {
	if b.ownerID != 0 || userID == 0 {
		return
	}
	if cur := b.effectiveOwner(ctx); cur != 0 {
		return
	}
	if err := b.deps.Store.KVSet(ctx, "owner_telegram_id", strconv.FormatInt(userID, 10)); err != nil {
		b.logger.Warn("record owner", "err", err)
	}
}

// Owner resolves who scheduled DMs go to for the scheduler.
func (b *Bot) Owner(ctx context.Context) (int64, error) {
	if owner := b.effectiveOwner(ctx); owner != 0 {
		return owner, nil
	}
	return 0, ErrNoOwner
}

// SendText DMs plain text to a chat.
func (b *Bot) SendText(ctx context.Context, chatID int64, text string) error {
	_, err := b.api.SendMessage(ctx, chatID, text, nil)
	if err != nil {
		return fmt.Errorf("send telegram message: %w", err)
	}
	return nil
}

// SendDraftCard DMs the current card for a draft and records the message.
func (b *Bot) SendDraftCard(ctx context.Context, chatID int64, draftID int64) error {
	d, err := b.deps.Store.GetDraft(ctx, draftID)
	if err != nil {
		return err
	}
	articleTitle := ""
	if art, err := b.deps.Store.GetArticle(ctx, d.ArticleID); err == nil {
		articleTitle = art.Title
	}
	text, markup := renderCard(d, articleTitle)
	msg, err := b.api.SendMessage(ctx, chatID, text, markup)
	if err != nil {
		return fmt.Errorf("send draft card: %w", err)
	}
	if err := b.deps.Store.SetDraftMessage(ctx, draftID, chatID, int64(msg.MessageID)); err != nil {
		return err
	}
	return nil
}

// editCard re-renders a draft's original message in place.
func (b *Bot) editCard(ctx context.Context, d store.Draft) error {
	if d.TelegramChatID == 0 || d.TelegramMessageID == 0 {
		return fmt.Errorf("draft %d has no telegram message recorded", d.ID)
	}
	fresh, err := b.deps.Store.GetDraft(ctx, d.ID)
	if err != nil {
		return err
	}
	articleTitle := ""
	if art, err := b.deps.Store.GetArticle(ctx, fresh.ArticleID); err == nil {
		articleTitle = art.Title
	}
	text, markup := renderCard(fresh, articleTitle)
	if err := b.api.EditMessageText(ctx, fresh.TelegramChatID, int(fresh.TelegramMessageID), text, markup); err != nil {
		return fmt.Errorf("edit draft card: %w", err)
	}
	return nil
}

// keepTyping shows "typing..." until stop is called. Callers defer stop().
// It answers "how do I know it is working" during slow AI generations.
func (b *Bot) keepTyping(ctx context.Context, chatID int64) (stop func()) {
	done := make(chan struct{})
	var once sync.Once
	stop = func() { once.Do(func() { close(done) }) }
	go func() {
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()
		b.sendTyping(ctx, chatID)
		for {
			select {
			case <-done:
				return
			case <-b.stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				b.sendTyping(ctx, chatID)
			}
		}
	}()
	return stop
}

func (b *Bot) sendTyping(ctx context.Context, chatID int64) {
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := b.api.SendChatAction(tctx, chatID, "typing"); err != nil {
		b.logger.Debug("typing indicator", "err", err)
	}
}

// renderCard builds the card text and keyboard for a draft's current state.
func renderCard(d store.Draft, articleTitle string) (string, *InlineKeyboardMarkup) {
	switch d.Status {
	case store.DraftPosted:
		url := ""
		if d.LinkedInURN != "" {
			url = linkedin.PostURL(d.LinkedInURN)
		}
		return RenderDraftCard(d.ID, d.Text, "", d.Status, url), nil
	case store.DraftSkipped:
		return RenderDraftCard(d.ID, d.Text, "", d.Status, ""), nil
	}
	return RenderDraftCard(d.ID, d.Text, articleTitle, d.Status, ""), CardKeyboard(d.ID)
}

func tokenStatus(ctx context.Context, st *store.Store) (connected bool, name, detail string) {
	tok, err := st.GetLinkedInToken(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", "not connected"
	}
	if err != nil {
		return false, "", "unreadable"
	}
	name = tok.PersonName
	switch {
	case tok.Expired():
		return false, name, "expired, send /link"
	case tok.ExpiringSoon(72 * time.Hour):
		left := time.Until(time.Unix(tok.ExpiresAt, 0)).Round(time.Hour)
		return true, name, fmt.Sprintf("expires in %s, reconnect soon with /link", left)
	default:
		left := time.Until(time.Unix(tok.ExpiresAt, 0)).Round(time.Hour)
		return true, name, fmt.Sprintf("valid for %s", left)
	}
}
