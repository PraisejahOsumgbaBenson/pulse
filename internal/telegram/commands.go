package telegram

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/PraisejahOsumgbaBenson/pulse/internal/generate"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/linkedin"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/scheduler"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/store"
)

// snoozeFor is how long Snooze holds a draft before re-announcing it.
const snoozeFor = time.Hour

// editKey tracks which draft awaits revised text in a chat.
func editKey(chatID int64) string {
	return fmt.Sprintf("tg:edit:%d", chatID)
}

func senderOf(m *Message) (userID, chatID int64) {
	if m == nil {
		return 0, 0
	}
	if m.From != nil {
		userID = m.From.ID
	}
	return userID, m.Chat.ID
}

func (b *Bot) reply(ctx context.Context, chatID int64, text string) {
	if err := b.SendText(ctx, chatID, text); err != nil {
		b.logger.Warn("reply", "chat", chatID, "err", err)
	}
}

func (b *Bot) onMessage(ctx context.Context, m *Message) {
	userID, chatID := senderOf(m)
	if userID == 0 || chatID == 0 {
		return
	}
	name, args := ParseCommand(m.Text)
	if name == "" {
		b.onPlainText(ctx, userID, chatID, strings.TrimSpace(m.Text))
		return
	}
	if (name == "start" || name == "link") && b.effectiveOwner(ctx) == 0 {
		b.claimOwner(ctx, userID)
	}
	if !b.allowed(ctx, userID) {
		b.reply(ctx, chatID, "This bot is private to its owner.")
		return
	}
	switch name {
	case "start":
		b.cmdStart(ctx, chatID)
	case "link":
		b.cmdLink(ctx, userID, chatID)
	case "status":
		b.cmdStatus(ctx, chatID)
	case "help":
		b.cmdHelp(ctx, chatID)
	case "source_add":
		b.cmdSourceAdd(ctx, chatID, args)
	case "source_list":
		b.cmdSourceList(ctx, chatID)
	case "source_remove":
		b.cmdSourceRemove(ctx, chatID, args)
	case "schedule", "schedule_set":
		b.cmdScheduleSet(ctx, userID, chatID, args)
	case "schedule_status":
		b.cmdScheduleStatus(ctx, chatID)
	case "schedule_off":
		b.cmdScheduleOff(ctx, chatID)
	case "draft":
		b.cmdDraft(ctx, userID, chatID)
	case "topic":
		b.cmdTopic(ctx, chatID, args)
	case "trending":
		b.cmdTrending(ctx, chatID)
	case "history":
		b.cmdHistory(ctx, chatID, args)
	case "autopost":
		b.cmdAutopost(ctx, chatID, args)
	case "cancel":
		b.cmdCancel(ctx, chatID)
	default:
		b.reply(ctx, chatID, "Unknown command. Send /help.")
	}
}

// onPlainText applies revised text to a draft awaiting an edit.
func (b *Bot) onPlainText(ctx context.Context, userID, chatID int64, text string) {
	if !b.allowed(ctx, userID) {
		b.reply(ctx, chatID, "This bot is private to its owner.")
		return
	}
	if f, ok := b.loadFlow(ctx, chatID); ok {
		if f.Cmd == "schedule" {
			b.handleScheduleFlow(ctx, userID, chatID, text, f)
			return
		}
		b.clearChatState(ctx, chatID)
	}
	raw, ok, err := b.deps.Store.KVGet(ctx, editKey(chatID))
	if err != nil || !ok || raw == "" {
		if ids, ok := b.loadPick(ctx, chatID); ok {
			b.handlePickReply(ctx, chatID, text, ids)
			return
		}
		// No pending edit: plain chat is a thought, draft from it like /topic.
		if utf8.RuneCountInString(text) < 12 {
			b.reply(ctx, chatID, "Tell me a bit more than that, or send /help for commands.")
			return
		}
		b.cmdTopic(ctx, chatID, text)
		return
	}
	draftID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || draftID <= 0 {
		_ = b.deps.Store.KVSet(ctx, editKey(chatID), "")
		b.reply(ctx, chatID, "That edit expired. Tap Edit on the draft card to try again.")
		return
	}
	if text == "" {
		b.reply(ctx, chatID, "The post text cannot be empty. Send the revised text or /cancel.")
		return
	}
	if utf8.RuneCountInString(text) > 3000 {
		b.reply(ctx, chatID, "That is over LinkedIn's 3000-character limit. Trim it and send again, or /cancel.")
		return
	}
	d, err := b.deps.Store.GetDraft(ctx, draftID)
	if err != nil {
		_ = b.deps.Store.KVSet(ctx, editKey(chatID), "")
		b.reply(ctx, chatID, fmt.Sprintf("Draft #%d is gone.", draftID))
		return
	}
	if err := b.deps.Store.UpdateDraftText(ctx, draftID, text); err != nil {
		b.reply(ctx, chatID, "Could not save: "+err.Error())
		return
	}
	_ = b.deps.Store.KVSet(ctx, editKey(chatID), "")
	if err := b.editCard(ctx, d); err != nil {
		b.logger.Warn("edit card after text", "draft", draftID, "err", err)
	}
	b.reply(ctx, chatID, fmt.Sprintf("Draft #%d updated.", draftID))
}

func (b *Bot) cmdStart(ctx context.Context, chatID int64) {
	b.reply(ctx, chatID, `Pulse drafts LinkedIn posts and reminds you to post.

Just type any thought and I turn it into a draft. You can also try /topic, /trending, /draft and /schedule. Every reminder arrives here with buttons: Approve posts it (or hands you the text until LinkedIn is connected), Edit takes revised text, Regenerate draws again. Send /link to connect LinkedIn for auto posting.`)
}

func (b *Bot) cmdLink(ctx context.Context, userID, chatID int64) {
	b.claimOwner(ctx, userID)
	if !b.deps.LinkedInConfigured {
		b.reply(ctx, chatID, "LinkedIn isn't set up on this bot yet, so I can't link. Reminders and drafts work meanwhile, and Approve hands you the text to paste manually.")
		return
	}
	state := randomState()
	url, verifier, err := b.deps.LinkedIn.AuthURL(state)
	if err != nil {
		b.reply(ctx, chatID, "Could not start the LinkedIn flow: "+err.Error())
		return
	}
	if err := b.deps.Store.SaveOAuthState(ctx, store.OAuthState{
		State: state, TelegramUserID: userID, Verifier: verifier,
	}); err != nil {
		b.reply(ctx, chatID, "Could not start the LinkedIn flow: "+err.Error())
		return
	}
	b.reply(ctx, chatID, "Open the link, sign in to LinkedIn, and approve Pulse. I confirm here when it is linked. The link expires in an hour.\n"+url)
}

func (b *Bot) cmdStatus(ctx context.Context, chatID int64) {
	connected, name, detail := tokenStatus(ctx, b.deps.Store)
	liLine := "LinkedIn: not connected, send /link"
	if connected {
		liLine = fmt.Sprintf("LinkedIn: connected as %s (%s)", name, detail)
	} else if name != "" {
		liLine = fmt.Sprintf("LinkedIn: %s, send /link", detail)
	}

	schedLine := "Schedule: off, set one with /schedule_set"
	if schedules, err := b.deps.Store.ListSchedules(ctx, false); err == nil && len(schedules) > 0 {
		var parts []string
		for _, sc := range schedules {
			state := "on"
			if !sc.Enabled {
				state = "off"
			}
			mode := "manual approval"
			if sc.Autopost {
				mode = "autopost"
			}
			parts = append(parts, fmt.Sprintf("%s %02d:%02d (%s, %s)", WeekdayNames(sc.Days), sc.Hour, sc.Minute, mode, state))
		}
		schedLine = "Schedule: " + strings.Join(parts, "; ")
	}

	sources, _ := b.deps.Store.ListSources(ctx)
	pending, _ := b.deps.Store.ListDrafts(ctx, store.DraftPending, 100)
	b.reply(ctx, chatID, fmt.Sprintf(
		"%s\n%s\nSources: %d (manage with /source_add, /source_list, /source_remove)\nPending drafts: %d\nGenerator: %s",
		liLine, schedLine, len(sources), len(pending), b.deps.Gen.Name()))
}

func (b *Bot) cmdHelp(ctx context.Context, chatID int64) {
	b.reply(ctx, chatID, `Pulse drafts LinkedIn posts from your sources and reminds you to post.

/link, connect LinkedIn (run again to reconnect)
/status, where everything stands
/source_add <url>, add an RSS feed or page
/source_list, show sources with ids
/source_remove <id>, drop a source
/schedule_set <time> <days>, e.g. /schedule_set 09:00 monday wednesday friday
/schedule, set reminders step by step with questions
/schedule_status, show the schedule
/schedule_off, stop reminders
/draft, generate one right now
/topic <thought>, draft from an idea with no sources needed
/trending, see what's hot and draft it
/history, recent drafts and posts
/autopost <true|false>, post without approval
/cancel, drop a pending edit

You can also just type a thought instead of using /topic.`)
}

func (b *Bot) cmdSourceAdd(ctx context.Context, chatID int64, args string) {
	url := strings.TrimSpace(args)
	if url == "" {
		b.reply(ctx, chatID, "Usage: /source_add <url>")
		return
	}
	src, n, err := b.deps.Feeds.Add(ctx, url)
	if err != nil {
		b.reply(ctx, chatID, "Could not add that source: "+err.Error())
		return
	}
	b.reply(ctx, chatID, fmt.Sprintf("Added [%s] %s, %d new article(s) stored.", src.Kind, src.Title, n))
}

func (b *Bot) cmdSourceList(ctx context.Context, chatID int64) {
	sources, err := b.deps.Store.ListSources(ctx)
	if err != nil {
		b.reply(ctx, chatID, "Could not list sources: "+err.Error())
		return
	}
	if len(sources) == 0 {
		b.reply(ctx, chatID, "No sources yet. Add one with /source_add <url>.")
		return
	}
	var bld strings.Builder
	for _, src := range sources {
		fmt.Fprintf(&bld, "%d. [%s] %s\n%s\n", src.ID, src.Kind, src.Title, src.URL)
	}
	b.reply(ctx, chatID, strings.TrimSpace(bld.String()))
}

func (b *Bot) cmdSourceRemove(ctx context.Context, chatID int64, args string) {
	id, err := strconv.ParseInt(strings.TrimSpace(args), 10, 64)
	if err != nil || id <= 0 {
		b.reply(ctx, chatID, "Usage: /source_remove <id> from /source_list.")
		return
	}
	if err := b.deps.Store.RemoveSource(ctx, id); errors.Is(err, sql.ErrNoRows) {
		b.reply(ctx, chatID, fmt.Sprintf("No source with id %d.", id))
		return
	} else if err != nil {
		b.reply(ctx, chatID, "Could not remove it: "+err.Error())
		return
	}
	b.reply(ctx, chatID, fmt.Sprintf("Removed source %d.", id))
}

func (b *Bot) cmdScheduleSet(ctx context.Context, userID, chatID int64, args string) {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		b.saveFlow(ctx, chatID, flowState{Cmd: "schedule", Step: "time"})
		b.reply(ctx, chatID, "What time should I remind you? (e.g. 09:00 or 3pm)")
		return
	}
	hour, minute, err := ParseScheduleTime(fields[0])
	if err != nil {
		b.saveFlow(ctx, chatID, flowState{Cmd: "schedule", Step: "time"})
		b.reply(ctx, chatID, "What time should I remind you? (e.g. 09:00 or 3pm)")
		return
	}
	rest := fields[1:]
	autopost := false
	haveAuto := false
	if len(rest) > 0 {
		last := strings.ToLower(rest[len(rest)-1])
		if last == "autopost" || last == "auto" {
			autopost = true
			haveAuto = true
			rest = rest[:len(rest)-1]
		}
	}
	if len(rest) == 0 {
		b.saveFlow(ctx, chatID, flowState{Cmd: "schedule", Step: "days", Hour: hour, Minute: minute})
		b.reply(ctx, chatID, "Which days? (daily, weekdays, weekends, or e.g. monday wednesday friday)")
		return
	}
	days, err := ParseDays(strings.Join(rest, " "))
	if err != nil {
		b.saveFlow(ctx, chatID, flowState{Cmd: "schedule", Step: "days", Hour: hour, Minute: minute})
		b.reply(ctx, chatID, "I didn't get those days. Try daily, weekdays, weekends, or e.g. monday wednesday friday.")
		return
	}
	if !haveAuto {
		b.saveFlow(ctx, chatID, flowState{Cmd: "schedule", Step: "auto", Hour: hour, Minute: minute, Days: days})
		b.reply(ctx, chatID, "Post automatically without asking each time? (yes/no)")
		return
	}
	b.saveSchedule(ctx, userID, chatID, hour, minute, days, autopost)
}

// saveSchedule stores the reminder slot and confirms it.
func (b *Bot) saveSchedule(ctx context.Context, userID, chatID int64, hour, minute int, days string, autopost bool) {
	sc, err := b.deps.Store.ReplaceSchedule(ctx, days, hour, minute, autopost, b.deps.TimezoneName)
	if err != nil {
		b.reply(ctx, chatID, "Could not save the schedule: "+err.Error())
		return
	}
	b.claimOwner(ctx, userID)
	mode := "manual approval"
	if sc.Autopost {
		mode = "autopost"
	}
	b.reply(ctx, chatID, fmt.Sprintf("Reminders set: %s at %02d:%02d %s (%s).",
		WeekdayNames(sc.Days), sc.Hour, sc.Minute, b.deps.TimezoneName, mode))
}

// handleScheduleFlow answers one reply inside the schedule conversation.
func (b *Bot) handleScheduleFlow(ctx context.Context, userID, chatID int64, text string, f flowState) {
	switch f.Step {
	case "time":
		hour, minute, err := ParseScheduleTime(text)
		if err != nil {
			b.reply(ctx, chatID, "I didn't get that time. Try e.g. 09:00 or 3pm. (/cancel to stop)")
			return
		}
		b.saveFlow(ctx, chatID, flowState{Cmd: "schedule", Step: "days", Hour: hour, Minute: minute})
		b.reply(ctx, chatID, "Which days? (daily, weekdays, weekends, or e.g. monday wednesday friday)")
	case "days":
		days, err := ParseDays(text)
		if err != nil {
			b.reply(ctx, chatID, "I didn't get those days: "+err.Error()+". (/cancel to stop)")
			return
		}
		b.saveFlow(ctx, chatID, flowState{Cmd: "schedule", Step: "auto", Hour: f.Hour, Minute: f.Minute, Days: days})
		b.reply(ctx, chatID, "Post automatically without asking each time? (yes/no)")
	case "auto":
		enabled, ok := parseBoolArg(strings.TrimSpace(text))
		if !ok {
			b.reply(ctx, chatID, "Answer yes or no. Post automatically without asking each time? (/cancel to stop)")
			return
		}
		b.clearChatState(ctx, chatID)
		b.saveSchedule(ctx, userID, chatID, f.Hour, f.Minute, f.Days, enabled)
	default:
		b.clearChatState(ctx, chatID)
		b.reply(ctx, chatID, "Let's start over: what time should I remind you?")
	}
}

func (b *Bot) cmdScheduleStatus(ctx context.Context, chatID int64) {
	schedules, err := b.deps.Store.ListSchedules(ctx, false)
	if err != nil {
		b.reply(ctx, chatID, "Could not read the schedule: "+err.Error())
		return
	}
	if len(schedules) == 0 {
		b.reply(ctx, chatID, "No schedule set. Use /schedule_set, e.g. /schedule_set 09:00 monday wednesday friday.")
		return
	}
	var parts []string
	for _, sc := range schedules {
		state := "on"
		if !sc.Enabled {
			state = "off"
		}
		mode := "manual approval"
		if sc.Autopost {
			mode = "autopost"
		}
		parts = append(parts, fmt.Sprintf("%s %02d:%02d %s, %s, %s",
			WeekdayNames(sc.Days), sc.Hour, sc.Minute, b.deps.TimezoneName, mode, state))
	}
	b.reply(ctx, chatID, "Schedule: "+strings.Join(parts, "; "))
}

func (b *Bot) cmdScheduleOff(ctx context.Context, chatID int64) {
	if err := b.deps.Store.DisableSchedules(ctx); err != nil {
		b.reply(ctx, chatID, "Could not stop the schedule: "+err.Error())
		return
	}
	b.reply(ctx, chatID, "Reminders off.")
}

func (b *Bot) cmdDraft(ctx context.Context, _ int64, chatID int64) {
	stop := b.keepTyping(ctx, chatID)
	defer stop()
	id, err := scheduler.GenerateOneDraft(ctx, b.deps.Store, b.deps.Feeds, b.deps.Gen)
	if err != nil {
		b.reply(ctx, chatID, err.Error())
		return
	}
	if err := b.SendDraftCard(ctx, chatID, id); err != nil {
		b.reply(ctx, chatID, "Draft ready but I could not send it: "+err.Error())
	}
}

func (b *Bot) cmdTopic(ctx context.Context, chatID int64, args string) {
	thought := strings.TrimSpace(args)
	if thought == "" {
		b.reply(ctx, chatID, "Usage: /topic <your thought>. Example: /topic my first week learning Go.")
		return
	}
	stop := b.keepTyping(ctx, chatID)
	defer stop()
	id, err := scheduler.GenerateTopicDraft(ctx, b.deps.Store, b.deps.Gen, thought)
	if err != nil {
		b.reply(ctx, chatID, err.Error())
		return
	}
	if err := b.SendDraftCard(ctx, chatID, id); err != nil {
		b.reply(ctx, chatID, "Draft ready but I could not send it: "+err.Error())
	}
}

func (b *Bot) cmdHistory(ctx context.Context, chatID int64, args string) {
	limit := 5
	if strings.TrimSpace(args) != "" {
		n, err := strconv.Atoi(strings.TrimSpace(args))
		if err != nil || n < 1 {
			b.reply(ctx, chatID, "Usage: /history [limit up to 10].")
			return
		}
		limit = n
	}
	if limit > 10 {
		limit = 10
	}
	drafts, err := b.deps.Store.ListDrafts(ctx, "", limit)
	if err != nil {
		b.reply(ctx, chatID, "Could not read history: "+err.Error())
		return
	}
	if len(drafts) == 0 {
		b.reply(ctx, chatID, "Nothing yet. Generate one with /draft.")
		return
	}
	var bld strings.Builder
	for _, d := range drafts {
		mark := map[string]string{
			store.DraftPending: "pending", store.DraftPosted: "posted",
			store.DraftSkipped: "skipped", store.DraftFailed: "failed",
		}[d.Status]
		line := fmt.Sprintf("#%d %s, %s", d.ID, mark, excerpt(d.Text, 80))
		if d.Status == store.DraftPosted && d.LinkedInURN != "" {
			line += "\n" + linkedinPostURL(d.LinkedInURN)
		}
		bld.WriteString(line + "\n")
	}
	b.reply(ctx, chatID, strings.TrimSpace(bld.String()))
}

func (b *Bot) cmdAutopost(ctx context.Context, chatID int64, args string) {
	enabled, ok := parseBoolArg(strings.TrimSpace(args))
	if !ok {
		b.reply(ctx, chatID, "Usage: /autopost true|false.")
		return
	}
	schedules, err := b.deps.Store.ListSchedules(ctx, false)
	if err != nil {
		b.reply(ctx, chatID, "Could not read the schedule: "+err.Error())
		return
	}
	if len(schedules) == 0 {
		b.reply(ctx, chatID, "Set a schedule first with /schedule_set.")
		return
	}
	for _, sc := range schedules {
		if err := b.deps.Store.SetScheduleAutopost(ctx, sc.ID, enabled); err != nil {
			b.reply(ctx, chatID, "Could not update autopost: "+err.Error())
			return
		}
	}
	if enabled {
		b.reply(ctx, chatID, "Autopost on: drafts post themselves at schedule time.")
	} else {
		b.reply(ctx, chatID, "Autopost off: every draft needs your approval.")
	}
}

func (b *Bot) cmdCancel(ctx context.Context, chatID int64) {
	b.clearChatState(ctx, chatID)
	b.reply(ctx, chatID, "Cancelled. Nothing pending.")
}

func parseBoolArg(s string) (bool, bool) {
	switch strings.ToLower(s) {
	case "true", "1", "on", "yes":
		return true, true
	case "false", "0", "off", "no":
		return false, true
	}
	return false, false
}

func (b *Bot) onCallback(ctx context.Context, q *CallbackQuery) {
	if q == nil {
		return
	}
	if !b.allowed(ctx, q.From.ID) {
		_ = b.api.AnswerCallbackQuery(ctx, q.ID, "This bot is private to its owner.")
		return
	}
	if q.Data == "pulse:pack" {
		chatID := q.From.ID
		if q.Message != nil {
			chatID = q.Message.Chat.ID
		}
		b.handlePack(ctx, q, chatID)
		return
	}
	action, draftID, ok := ParseCallbackData(q.Data)
	if !ok {
		_ = b.api.AnswerCallbackQuery(ctx, q.ID, "I do not recognize that button.")
		return
	}
	chatID := q.From.ID
	if q.Message != nil {
		chatID = q.Message.Chat.ID
	}
	d, err := b.deps.Store.GetDraft(ctx, draftID)
	if err != nil {
		_ = b.api.AnswerCallbackQuery(ctx, q.ID, fmt.Sprintf("Draft #%d is gone.", draftID))
		return
	}
	switch action {
	case ActionApprove:
		b.handleApprove(ctx, q, chatID, d)
	case ActionRegen:
		b.handleRegen(ctx, q, chatID, d)
	case ActionSkip:
		b.handleSkip(ctx, q, chatID, d)
	case ActionSnooze:
		b.handleSnooze(ctx, q, chatID, d)
	case ActionEdit:
		_ = b.deps.Store.KVSet(ctx, editKey(chatID), strconv.FormatInt(d.ID, 10))
		_ = b.api.AnswerCallbackQuery(ctx, q.ID, "")
		b.reply(ctx, chatID, fmt.Sprintf("Send the revised text for draft #%d. /cancel keeps the current text.", d.ID))
	default:
		_ = b.api.AnswerCallbackQuery(ctx, q.ID, "I do not recognize that button.")
	}
}

func (b *Bot) handleApprove(ctx context.Context, q *CallbackQuery, chatID int64, d store.Draft) {
	if connected, _, detail := tokenStatus(ctx, b.deps.Store); !connected {
		if detail == "" {
			detail = "not connected"
		}
		_ = b.api.AnswerCallbackQuery(ctx, q.ID, "LinkedIn is not connected.")
		b.reply(ctx, chatID, fmt.Sprintf("I can't post this yet (LinkedIn: %s). Copy the text below into the LinkedIn app yourself:\n\n%s", detail, d.Text))
		return
	}
	urn, err := b.deps.Pub.Publish(ctx, d.ID)
	if err != nil {
		b.logger.Warn("publish failed", "draft", d.ID, "err", err)
		_ = b.editCard(ctx, d)
		_ = b.api.AnswerCallbackQuery(ctx, q.ID, "Could not post: "+err.Error())
		return
	}
	_ = b.editCard(ctx, d)
	_ = b.api.AnswerCallbackQuery(ctx, q.ID, "Posted to LinkedIn.")
	b.reply(ctx, chatID, "Posted to LinkedIn: "+linkedin.PostURL(urn))
}

// isTopicDraft reports whether a draft came from /topic rather than a feed.
func (b *Bot) isTopicDraft(ctx context.Context, d store.Draft) bool {
	art, err := b.deps.Store.GetArticle(ctx, d.ArticleID)
	if err != nil {
		return false
	}
	src, err := b.deps.Store.GetSource(ctx, art.SourceID)
	if err != nil {
		return false
	}
	return src.Kind == store.SourceTopic
}

// regenTopic rewrites a topic draft from its original thought.
func (b *Bot) regenTopic(ctx context.Context, q *CallbackQuery, chatID int64, d store.Draft) {
	art, err := b.deps.Store.GetArticle(ctx, d.ArticleID)
	if err != nil {
		_ = b.api.AnswerCallbackQuery(ctx, q.ID, "The original thought is gone.")
		return
	}
	text, err := b.deps.Gen.Generate(ctx, generateInput(art))
	if err != nil {
		b.logger.Warn("regenerate topic failed", "draft", d.ID, "err", err)
		_ = b.api.AnswerCallbackQuery(ctx, q.ID, "Generation failed.")
		return
	}
	if err := b.deps.Store.UpdateDraftArticle(ctx, d.ID, art.ID, text); err != nil {
		_ = b.api.AnswerCallbackQuery(ctx, q.ID, "Could not save the new draft.")
		return
	}
	_ = b.deps.Store.KVSet(ctx, editKey(chatID), "")
	_ = b.editCard(ctx, d)
	_ = b.api.AnswerCallbackQuery(ctx, q.ID, "Rewritten from your thought.")
}

func (b *Bot) handleRegen(ctx context.Context, q *CallbackQuery, chatID int64, d store.Draft) {
	stop := b.keepTyping(ctx, chatID)
	defer stop()
	if b.isTopicDraft(ctx, d) {
		b.regenTopic(ctx, q, chatID, d)
		return
	}
	art, err := b.deps.Store.NextUnusedArticle(ctx)
	if err != nil {
		_ = b.api.AnswerCallbackQuery(ctx, q.ID, "No fresh articles left. Add sources with /source_add.")
		return
	}
	text, err := b.deps.Gen.Generate(ctx, generateInput(art))
	if err != nil {
		b.logger.Warn("regenerate failed", "draft", d.ID, "err", err)
		_ = b.api.AnswerCallbackQuery(ctx, q.ID, "Generation failed.")
		return
	}
	if err := b.deps.Store.UpdateDraftArticle(ctx, d.ID, art.ID, text); err != nil {
		_ = b.api.AnswerCallbackQuery(ctx, q.ID, "Could not save the new draft.")
		return
	}
	if err := b.deps.Store.MarkArticleUsed(ctx, art.ID); err != nil {
		b.logger.Warn("mark article used", "article", art.ID, "err", err)
	}
	_ = b.editCard(ctx, d)
	_ = b.api.AnswerCallbackQuery(ctx, q.ID, "New draft drawn.")
	_ = b.deps.Store.KVSet(ctx, editKey(chatID), "")
}

func (b *Bot) handleSkip(ctx context.Context, q *CallbackQuery, chatID int64, d store.Draft) {
	if err := b.deps.Store.UpdateDraftStatus(ctx, d.ID, store.DraftSkipped, "", ""); err != nil {
		_ = b.api.AnswerCallbackQuery(ctx, q.ID, "Could not skip.")
		return
	}
	_ = b.deps.Store.ClearSnoozesForDraft(ctx, d.ID)
	_ = b.deps.Store.KVSet(ctx, editKey(chatID), "")
	_ = b.editCard(ctx, d)
	_ = b.api.AnswerCallbackQuery(ctx, q.ID, "Skipped.")
}

func (b *Bot) handleSnooze(ctx context.Context, q *CallbackQuery, _ int64, d store.Draft) {
	if d.Status != store.DraftPending && d.Status != store.DraftFailed {
		_ = b.api.AnswerCallbackQuery(ctx, q.ID, fmt.Sprintf("Draft #%d already moved on.", d.ID))
		return
	}
	if err := b.deps.Store.AddSnooze(ctx, d.ID, time.Now().Add(snoozeFor)); err != nil {
		_ = b.api.AnswerCallbackQuery(ctx, q.ID, "Could not snooze.")
		return
	}
	_ = b.api.AnswerCallbackQuery(ctx, q.ID, fmt.Sprintf("Snoozed draft #%d for an hour.", d.ID))
}

// generateInput shapes an article for the generator.
func generateInput(art store.Article) generate.Input {
	summary := art.Summary
	if summary == "" {
		summary = art.Body
	}
	return generate.Input{Title: art.Title, Summary: summary, URL: art.URL}
}

func linkedinPostURL(urn string) string {
	return "https://www.linkedin.com/feed/update/" + urn + "/"
}

func excerpt(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max-1]) + "…"
}

func randomState() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
