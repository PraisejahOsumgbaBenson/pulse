// Package discord is the Discord interface for Pulse: slash commands,
// draft cards with buttons, an edit modal, and DM reminders.
package discord

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/PraisejahOsumgbaBenson/pulse/internal/feeds"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/generate"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/linkedin"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/publisher"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/store"
	"github.com/bwmarrin/discordgo"
)

// ErrNoOwner means nobody claimed the bot yet, so scheduled DMs have
// nowhere to go. Running /link or /schedule-set claims it.
var ErrNoOwner = errors.New("no owner recorded, run /link first")

// Deps wires the bot to the rest of Pulse.
type Deps struct {
	Store        *store.Store
	Feeds        *feeds.Service
	Gen          generate.Generator
	LinkedIn     *linkedin.Client
	Pub          *publisher.Publisher
	Location     *time.Location
	TimezoneName string
	OwnerID      string
	DevGuildID   string
	Logger       *slog.Logger
}

// Bot is a running Discord session with Pulse handlers.
type Bot struct {
	session *discordgo.Session
	deps    Deps
	logger  *slog.Logger
}

// New creates a Bot without connecting.
func New(token string, deps Deps) (*Bot, error) {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	session, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, fmt.Errorf("create discord session: %w", err)
	}
	b := &Bot{session: session, deps: deps, logger: deps.Logger}
	session.AddHandler(b.onInteraction)
	return b, nil
}

// Start connects, registers slash commands, and returns. Close with Close.
func (b *Bot) Start() error {
	if err := b.session.Open(); err != nil {
		return fmt.Errorf("open discord session: %w", err)
	}
	cmds := commandDefinitions()
	for _, cmd := range cmds {
		if _, err := b.session.ApplicationCommandCreate(
			b.session.State.User.ID, b.deps.DevGuildID, cmd); err != nil {
			b.session.Close()
			return fmt.Errorf("register command %s: %w", cmd.Name, err)
		}
	}
	b.logger.Info("discord bot started", "commands", len(cmds), "dev_guild", b.deps.DevGuildID != "")
	return nil
}

// Close disconnects the session.
func (b *Bot) Close() error {
	return b.session.Close()
}

// Session exposes the underlying session for shutdown-aware callers.
func (b *Bot) Session() *discordgo.Session {
	return b.session
}

func (b *Bot) onInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		b.onCommand(s, i)
	case discordgo.InteractionMessageComponent:
		b.onComponent(s, i)
	case discordgo.InteractionModalSubmit:
		b.onModalSubmit(s, i)
	}
}

// allowed gates every interaction to the configured or claiming owner.
func (b *Bot) allowed(userID string) bool {
	return b.deps.OwnerID == "" || userID == b.deps.OwnerID
}

func invokerID(i *discordgo.InteractionCreate) string {
	if i.Member != nil && i.Member.User != nil {
		return i.Member.User.ID
	}
	if i.User != nil {
		return i.User.ID
	}
	return ""
}

func (b *Bot) deny(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = respondEphemeral(s, i, "This bot is private to its owner.")
}

// claimOwner records the invoker as the DM target when no owner is configured.
func (b *Bot) claimOwner(ctx context.Context, userID string) {
	if b.deps.OwnerID != "" || userID == "" {
		return
	}
	if err := b.deps.Store.KVSet(ctx, "owner_discord_id", userID); err != nil {
		b.logger.Warn("record owner", "err", err)
	}
}

// Owner resolves who scheduled DMs go to: the configured owner, else the
// user who claimed the bot with /link or /schedule-set.
func (b *Bot) Owner(ctx context.Context) (string, error) {
	if b.deps.OwnerID != "" {
		return b.deps.OwnerID, nil
	}
	if id, ok, err := b.deps.Store.KVGet(ctx, "owner_discord_id"); err == nil && ok && id != "" {
		return id, nil
	}
	return "", ErrNoOwner
}

// SendText DMs plain text to a user.
func (b *Bot) SendText(ctx context.Context, userID, text string) error {
	ch, err := b.session.UserChannelCreate(userID)
	if err != nil {
		return fmt.Errorf("open dm: %w", err)
	}
	if _, err := b.session.ChannelMessageSend(ch.ID, text); err != nil {
		return fmt.Errorf("send dm: %w", err)
	}
	return nil
}

// SendDraftCard DMs the current card for a draft and records the message.
func (b *Bot) SendDraftCard(ctx context.Context, userID string, draftID int64) error {
	d, err := b.deps.Store.GetDraft(ctx, draftID)
	if err != nil {
		return err
	}
	articleTitle := ""
	if art, err := b.deps.Store.GetArticle(ctx, d.ArticleID); err == nil {
		articleTitle = art.Title
	}
	embeds, components := renderCard(d, articleTitle)
	ch, err := b.session.UserChannelCreate(userID)
	if err != nil {
		return fmt.Errorf("open dm: %w", err)
	}
	msg, err := b.session.ChannelMessageSendComplex(ch.ID, &discordgo.MessageSend{
		Embeds:     embeds,
		Components: components,
	})
	if err != nil {
		return fmt.Errorf("send draft card: %w", err)
	}
	if err := b.deps.Store.SetDraftMessage(ctx, draftID, ch.ID, msg.ID); err != nil {
		return err
	}
	return nil
}

// editCard re-renders a draft's original message in place.
func (b *Bot) editCard(ctx context.Context, d store.Draft) error {
	if d.DiscordChannelID == "" || d.DiscordMessageID == "" {
		return fmt.Errorf("draft %d has no discord message recorded", d.ID)
	}
	fresh, err := b.deps.Store.GetDraft(ctx, d.ID)
	if err != nil {
		return err
	}
	articleTitle := ""
	if art, err := b.deps.Store.GetArticle(ctx, fresh.ArticleID); err == nil {
		articleTitle = art.Title
	}
	embeds, components := renderCard(fresh, articleTitle)
	edit := &discordgo.MessageEdit{
		Channel:    fresh.DiscordChannelID,
		ID:         fresh.DiscordMessageID,
		Embeds:     &embeds,
		Components: &components,
	}
	if _, err := b.session.ChannelMessageEditComplex(edit); err != nil {
		return fmt.Errorf("edit draft card: %w", err)
	}
	return nil
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
		return false, name, "expired, run /link"
	case tok.ExpiringSoon(72 * time.Hour):
		left := time.Until(time.Unix(tok.ExpiresAt, 0)).Round(time.Hour)
		return true, name, fmt.Sprintf("expires in %s, reconnect soon with /link", left)
	default:
		left := time.Until(time.Unix(tok.ExpiresAt, 0)).Round(time.Hour)
		return true, name, fmt.Sprintf("valid for %s", left)
	}
}
