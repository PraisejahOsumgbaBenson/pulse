package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/PraisejahOsumgbaBenson/pulse/internal/scheduler"
)

// trendingPack is the one-tap hot-topic starter kit: Hacker News, TechCrunch
// AI and Dev.to. URLs verified live before hardcoding.
var trendingPack = []string{
	"https://news.ycombinator.com/rss",
	"https://techcrunch.com/category/artificial-intelligence/feed/",
	"https://dev.to/feed",
}

// maxTrending is how many hot items one /trending shows.
const maxTrending = 5

// pickKey tracks article ids a trending list offered in a chat.
func pickKey(chatID int64) string {
	return fmt.Sprintf("tg:pick:%d", chatID)
}

func (b *Bot) savePick(ctx context.Context, chatID int64, ids []int64) {
	raw, err := json.Marshal(ids)
	if err != nil {
		b.logger.Warn("encode pick", "err", err)
		return
	}
	if err := b.deps.Store.KVSet(ctx, pickKey(chatID), string(raw)); err != nil {
		b.logger.Warn("save pick", "err", err)
	}
}

func (b *Bot) loadPick(ctx context.Context, chatID int64) ([]int64, bool) {
	raw, ok, err := b.deps.Store.KVGet(ctx, pickKey(chatID))
	if err != nil || !ok || raw == "" {
		return nil, false
	}
	var ids []int64
	if err := json.Unmarshal([]byte(raw), &ids); err != nil || len(ids) == 0 {
		return nil, false
	}
	return ids, true
}

func (b *Bot) clearPick(ctx context.Context, chatID int64) {
	if err := b.deps.Store.KVSet(ctx, pickKey(chatID), ""); err != nil {
		b.logger.Warn("clear pick", "err", err)
	}
}

func (b *Bot) cmdTrending(ctx context.Context, chatID int64) {
	stop := b.keepTyping(ctx, chatID)
	defer stop()
	if _, err := b.deps.Feeds.Refresh(ctx); err != nil {
		b.logger.Warn("trending refresh", "err", err)
	}
	arts, err := b.deps.Store.ListUnusedArticles(ctx, maxTrending)
	if err != nil {
		b.reply(ctx, chatID, "Could not read fresh articles: "+err.Error())
		return
	}
	if len(arts) == 0 {
		b.sendWithKeyboard(ctx, chatID,
			"Nothing fresh right now. Add sources with /source_add, or tap below for the trending starter kit: Hacker News, TechCrunch AI and Dev.to.",
			&InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
				{{Text: "Add trending feeds", CallbackData: "pulse:pack"}},
			}})
		return
	}
	var bld strings.Builder
	bld.WriteString("What people are talking about right now:\n")
	ids := make([]int64, 0, len(arts))
	for i, a := range arts {
		name := "a source"
		if src, err := b.deps.Store.GetSource(ctx, a.SourceID); err == nil && src.Title != "" {
			name = src.Title
		}
		fmt.Fprintf(&bld, "\n%d. %s\n   %s\n", i+1, a.Title, name)
		ids = append(ids, a.ID)
	}
	bld.WriteString("\nReply with the number to draft it.")
	b.savePick(ctx, chatID, ids)
	b.reply(ctx, chatID, strings.TrimSpace(bld.String()))
}

func (b *Bot) handlePickReply(ctx context.Context, chatID int64, text string, ids []int64) {
	n, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || n < 1 || n > len(ids) {
		b.reply(ctx, chatID, fmt.Sprintf("Reply with a number 1 to %d, or /cancel.", len(ids)))
		return
	}
	stop := b.keepTyping(ctx, chatID)
	defer stop()
	id, err := scheduler.GenerateDraftFromArticle(ctx, b.deps.Store, b.deps.Gen, ids[n-1])
	if err != nil {
		b.reply(ctx, chatID, err.Error())
		return
	}
	b.clearPick(ctx, chatID)
	if err := b.SendDraftCard(ctx, chatID, id); err != nil {
		b.reply(ctx, chatID, "Draft ready but I could not send it: "+err.Error())
	}
}

func (b *Bot) handlePack(ctx context.Context, q *CallbackQuery, chatID int64) {
	_ = b.api.AnswerCallbackQuery(ctx, q.ID, "Adding trending feeds...")
	stop := b.keepTyping(ctx, chatID)
	defer stop()
	fresh := 0
	for _, u := range trendingPack {
		_, n, err := b.deps.Feeds.Add(ctx, u)
		if err != nil {
			b.logger.Warn("trending pack add failed", "url", u, "err", err)
			continue
		}
		fresh += n
	}
	b.reply(ctx, chatID, fmt.Sprintf("Trending feeds ready (%d new articles). Send /trending to see what's hot.", fresh))
}

// sendWithKeyboard sends text plus an inline keyboard.
func (b *Bot) sendWithKeyboard(ctx context.Context, chatID int64, text string, markup *InlineKeyboardMarkup) {
	if _, err := b.api.SendMessage(ctx, chatID, text, markup); err != nil {
		b.logger.Warn("send with keyboard", "err", err)
	}
}
