package discord

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

	"github.com/PraisejahOsumgbaBenson/pulse/internal/feeds"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/generate"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/store"
	"github.com/bwmarrin/discordgo"
)

// commandDefinitions lists every slash command Pulse registers.
func commandDefinitions() []*discordgo.ApplicationCommand {
	return []*discordgo.ApplicationCommand{
		{Name: "link", Description: "Connect your LinkedIn account to Pulse"},
		{Name: "status", Description: "Show LinkedIn, schedule, sources and drafts status"},
		{Name: "help", Description: "List what Pulse can do"},
		{
			Name:        "source-add",
			Description: "Add an RSS feed or page to draft from",
			Options: []*discordgo.ApplicationCommandOption{
				{Type: discordgo.ApplicationCommandOptionString, Name: "url", Description: "Feed or page URL", Required: true},
			},
		},
		{Name: "source-list", Description: "List your sources"},
		{
			Name:        "source-remove",
			Description: "Remove a source by its id from /source-list",
			Options: []*discordgo.ApplicationCommandOption{
				{Type: discordgo.ApplicationCommandOptionInteger, Name: "id", Description: "Source id", Required: true},
			},
		},
		{
			Name:        "schedule-set",
			Description: "Set the weekly reminder, replacing any existing one",
			Options: []*discordgo.ApplicationCommandOption{
				{Type: discordgo.ApplicationCommandOptionString, Name: "time", Description: "24-hour time, e.g. 09:00", Required: true},
				{Type: discordgo.ApplicationCommandOptionString, Name: "days", Description: "e.g. monday wednesday friday, or daily", Required: true},
				{Type: discordgo.ApplicationCommandOptionBoolean, Name: "autopost", Description: "Post without approval at schedule time"},
			},
		},
		{Name: "schedule-status", Description: "Show the current schedule"},
		{Name: "schedule-off", Description: "Turn reminders off"},
		{Name: "draft", Description: "Generate a draft right now"},
		{
			Name:        "history",
			Description: "Show recent drafts and posts",
			Options: []*discordgo.ApplicationCommandOption{
				{Type: discordgo.ApplicationCommandOptionInteger, Name: "limit", Description: "How many, 1 to 10", Required: false},
			},
		},
		{
			Name:        "autopost",
			Description: "Toggle posting without approval",
			Options: []*discordgo.ApplicationCommandOption{
				{Type: discordgo.ApplicationCommandOptionBoolean, Name: "enabled", Description: "Post automatically at schedule time", Required: true},
			},
		},
	}
}

func (b *Bot) onCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	userID := invokerID(i)
	if !b.allowed(userID) {
		b.deny(s, i)
		return
	}
	switch i.ApplicationCommandData().Name {
	case "link":
		b.cmdLink(s, i, userID)
	case "status":
		b.cmdStatus(s, i)
	case "help":
		b.cmdHelp(s, i)
	case "source-add":
		b.cmdSourceAdd(s, i)
	case "source-list":
		b.cmdSourceList(s, i)
	case "source-remove":
		b.cmdSourceRemove(s, i)
	case "schedule-set":
		b.cmdScheduleSet(s, i, userID)
	case "schedule-status":
		b.cmdScheduleStatus(s, i)
	case "schedule-off":
		b.cmdScheduleOff(s, i)
	case "draft":
		b.cmdDraft(s, i, userID)
	case "history":
		b.cmdHistory(s, i)
	case "autopost":
		b.cmdAutopost(s, i)
	default:
		_ = respondEphemeral(s, i, "Unknown command. Try /help.")
	}
}

func (b *Bot) cmdLink(s *discordgo.Session, i *discordgo.InteractionCreate, userID string) {
	ctx := context.Background()
	b.claimOwner(ctx, userID)
	state := randomState()
	url, verifier, err := b.deps.LinkedIn.AuthURL(state)
	if err != nil {
		_ = respondEphemeral(s, i, "Could not start the LinkedIn flow: "+err.Error())
		return
	}
	if err := b.deps.Store.SaveOAuthState(ctx, store.OAuthState{
		State: state, DiscordUserID: userID, Verifier: verifier,
	}); err != nil {
		_ = respondEphemeral(s, i, "Could not start the LinkedIn flow: "+err.Error())
		return
	}
	err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "Open the link, sign in to LinkedIn, and approve Pulse. The bot confirms here when it is linked. The link expires in an hour.",
			Flags:   discordgo.MessageFlagsEphemeral,
			Components: []discordgo.MessageComponent{
				&discordgo.ActionsRow{Components: []discordgo.MessageComponent{
					&discordgo.Button{Label: "Connect LinkedIn", Style: discordgo.LinkButton, URL: url},
				}},
			},
		},
	})
	if err != nil {
		b.logger.Warn("respond link", "err", err)
	}
}

func (b *Bot) cmdStatus(s *discordgo.Session, i *discordgo.InteractionCreate) {
	ctx := context.Background()
	connected, name, detail := tokenStatus(ctx, b.deps.Store)
	liLine := "LinkedIn: not connected, run /link"
	if connected {
		liLine = fmt.Sprintf("LinkedIn: connected as %s (%s)", name, detail)
	} else if name != "" {
		liLine = fmt.Sprintf("LinkedIn: %s, run /link", detail)
	}

	schedLine := "Schedule: off, set one with /schedule-set"
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
			parts = append(parts, fmt.Sprintf("%s %02d:%02d (%s, %s)", weekdayNames(sc.Days), sc.Hour, sc.Minute, mode, state))
		}
		schedLine = "Schedule: " + strings.Join(parts, "; ")
	}

	sources, _ := b.deps.Store.ListSources(ctx)
	pending, _ := b.deps.Store.ListDrafts(ctx, store.DraftPending, 100)
	_ = respondEphemeral(s, i, fmt.Sprintf(
		"%s\n%s\nSources: %d (manage with /source-add, /source-list, /source-remove)\nPending drafts: %d\nGenerator: %s",
		liLine, schedLine, len(sources), len(pending), b.deps.Gen.Name()))
}

func (b *Bot) cmdHelp(s *discordgo.Session, i *discordgo.InteractionCreate) {
	_ = respondEphemeral(s, i, `Pulse drafts LinkedIn posts from your sources and reminds you to post.

/link, connect LinkedIn (run again to reconnect)
/status, where everything stands
/source-add <url>, add an RSS feed or page
/source-list, show sources with ids
/source-remove <id>, drop a source
/schedule-set <time> <days>, e.g. /schedule-set 09:00 monday wednesday friday
/schedule-status, show the schedule
/schedule-off, stop reminders
/draft, generate one right now
/history, recent drafts and posts
/autopost <true|false>, post without approval

Every reminder arrives as a DM with a draft card: Approve posts it to
LinkedIn, Edit opens an editor, Regenerate draws another source.`)
}

func (b *Bot) cmdSourceAdd(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := deferEphemeral(s, i); err != nil {
		return
	}
	url := strings.TrimSpace(optionString(i, "url"))
	src, n, err := b.deps.Feeds.Add(context.Background(), url)
	if err != nil {
		_ = followupEphemeral(s, i, "Could not add that source: "+err.Error())
		return
	}
	_ = followupEphemeral(s, i, fmt.Sprintf("Added [%s] %s, %d new article(s) stored.", src.Kind, src.Title, n))
}

func (b *Bot) cmdSourceList(s *discordgo.Session, i *discordgo.InteractionCreate) {
	sources, err := b.deps.Store.ListSources(context.Background())
	if err != nil {
		_ = respondEphemeral(s, i, "Could not list sources: "+err.Error())
		return
	}
	if len(sources) == 0 {
		_ = respondEphemeral(s, i, "No sources yet. Add one with /source-add <url>.")
		return
	}
	var bld strings.Builder
	for _, src := range sources {
		fmt.Fprintf(&bld, "%d. [%s] %s\n<%s>\n", src.ID, src.Kind, src.Title, src.URL)
	}
	_ = respondEphemeral(s, i, strings.TrimSpace(bld.String()))
}

func (b *Bot) cmdSourceRemove(s *discordgo.Session, i *discordgo.InteractionCreate) {
	id := optionInt(i, "id")
	if id <= 0 {
		_ = respondEphemeral(s, i, "Give a source id from /source-list.")
		return
	}
	if err := b.deps.Store.RemoveSource(context.Background(), id); errors.Is(err, sql.ErrNoRows) {
		_ = respondEphemeral(s, i, fmt.Sprintf("No source with id %d.", id))
		return
	} else if err != nil {
		_ = respondEphemeral(s, i, "Could not remove it: "+err.Error())
		return
	}
	_ = respondEphemeral(s, i, fmt.Sprintf("Removed source %d.", id))
}

func (b *Bot) cmdScheduleSet(s *discordgo.Session, i *discordgo.InteractionCreate, userID string) {
	clock, err := parseScheduleTime(optionString(i, "time"))
	if err != nil {
		_ = respondEphemeral(s, i, "Time must look like 09:00, 24-hour clock. "+err.Error())
		return
	}
	days, err := parseDays(optionString(i, "days"))
	if err != nil {
		_ = respondEphemeral(s, i, "Days must be weekday names, e.g. monday wednesday friday, or daily. "+err.Error())
		return
	}
	ctx := context.Background()
	sc, err := b.deps.Store.ReplaceSchedule(ctx, days, clock.hour, clock.minute, optionBool(i, "autopost"), b.deps.TimezoneName)
	if err != nil {
		_ = respondEphemeral(s, i, "Could not save the schedule: "+err.Error())
		return
	}
	b.claimOwner(ctx, userID)
	mode := "manual approval"
	if sc.Autopost {
		mode = "autopost"
	}
	_ = respondEphemeral(s, i, fmt.Sprintf("Reminders set: %s at %02d:%02d %s (%s).",
		weekdayNames(sc.Days), sc.Hour, sc.Minute, b.deps.TimezoneName, mode))
}

func (b *Bot) cmdScheduleStatus(s *discordgo.Session, i *discordgo.InteractionCreate) {
	schedules, err := b.deps.Store.ListSchedules(context.Background(), false)
	if err != nil {
		_ = respondEphemeral(s, i, "Could not read the schedule: "+err.Error())
		return
	}
	if len(schedules) == 0 {
		_ = respondEphemeral(s, i, "No schedule set. Use /schedule-set, e.g. /schedule-set 09:00 monday wednesday friday.")
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
			weekdayNames(sc.Days), sc.Hour, sc.Minute, b.deps.TimezoneName, mode, state))
	}
	_ = respondEphemeral(s, i, "Schedule: "+strings.Join(parts, "; "))
}

func (b *Bot) cmdScheduleOff(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := b.deps.Store.DisableSchedules(context.Background()); err != nil {
		_ = respondEphemeral(s, i, "Could not stop the schedule: "+err.Error())
		return
	}
	_ = respondEphemeral(s, i, "Reminders off.")
}

func (b *Bot) cmdDraft(s *discordgo.Session, i *discordgo.InteractionCreate, userID string) {
	if err := deferEphemeral(s, i); err != nil {
		return
	}
	ctx := context.Background()
	id, err := generateOneDraft(ctx, b.deps.Store, b.deps.Feeds, b.deps.Gen)
	if err != nil {
		_ = followupEphemeral(s, i, err.Error())
		return
	}
	if err := b.SendDraftCard(ctx, userID, id); err != nil {
		_ = followupEphemeral(s, i, "Draft ready but I could not DM it: "+err.Error())
		return
	}
	_ = followupEphemeral(s, i, fmt.Sprintf("Draft #%d is in your DMs.", id))
}

func (b *Bot) cmdHistory(s *discordgo.Session, i *discordgo.InteractionCreate) {
	limit := int(optionInt(i, "limit"))
	if limit <= 0 {
		limit = 5
	}
	if limit > 10 {
		limit = 10
	}
	drafts, err := b.deps.Store.ListDrafts(context.Background(), "", limit)
	if err != nil {
		_ = respondEphemeral(s, i, "Could not read history: "+err.Error())
		return
	}
	if len(drafts) == 0 {
		_ = respondEphemeral(s, i, "Nothing yet. Generate one with /draft.")
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
	_ = respondEphemeral(s, i, strings.TrimSpace(bld.String()))
}

func (b *Bot) cmdAutopost(s *discordgo.Session, i *discordgo.InteractionCreate) {
	enabled := optionBool(i, "enabled")
	ctx := context.Background()
	schedules, err := b.deps.Store.ListSchedules(ctx, false)
	if err != nil {
		_ = respondEphemeral(s, i, "Could not read the schedule: "+err.Error())
		return
	}
	if len(schedules) == 0 {
		_ = respondEphemeral(s, i, "Set a schedule first with /schedule-set.")
		return
	}
	for _, sc := range schedules {
		if err := b.deps.Store.SetScheduleAutopost(ctx, sc.ID, enabled); err != nil {
			_ = respondEphemeral(s, i, "Could not update autopost: "+err.Error())
			return
		}
	}
	if enabled {
		_ = respondEphemeral(s, i, "Autopost on: drafts post themselves at schedule time.")
	} else {
		_ = respondEphemeral(s, i, "Autopost off: every draft needs your approval.")
	}
}

// generateOneDraft refreshes sources, picks the next unused article, asks the
// generator for text, stores the draft and marks the article used.
func generateOneDraft(ctx context.Context, st *store.Store, f *feeds.Service, gen generate.Generator) (int64, error) {
	if _, err := f.Refresh(ctx); err != nil {
		return 0, fmt.Errorf("refresh sources: %w", err)
	}
	art, err := st.NextUnusedArticle(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("no fresh articles left. Add sources with /source-add")
	}
	if err != nil {
		return 0, fmt.Errorf("pick article: %w", err)
	}
	text, err := gen.Generate(ctx, generateInput(art))
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

type clock struct{ hour, minute int }

// parseScheduleTime accepts HH:MM in 24-hour time.
func parseScheduleTime(s string) (clock, error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return clock{}, fmt.Errorf("want HH:MM")
	}
	h, errH := strconv.Atoi(strings.TrimSpace(parts[0]))
	m, errM := strconv.Atoi(strings.TrimSpace(parts[1]))
	if errH != nil || errM != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return clock{}, fmt.Errorf("want HH:MM")
	}
	return clock{h, m}, nil
}

// parseDays accepts weekday names, abbreviations, daily, weekdays and
// weekends, and returns CSV time.Weekday numbers.
func parseDays(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "daily", "everyday", "every day":
		return "0,1,2,3,4,5,6", nil
	case "weekdays":
		return "1,2,3,4,5", nil
	case "weekends":
		return "0,6", nil
	}
	names := map[string]int{
		"sun": 0, "sunday": 0, "mon": 1, "monday": 1, "tue": 2, "tues": 2,
		"tuesday": 2, "wed": 3, "wednesday": 3, "thu": 4, "thur": 4,
		"thurs": 4, "thursday": 4, "fri": 5, "friday": 5, "sat": 6, "saturday": 6,
	}
	seen := map[int]bool{}
	var days []int
	for _, tok := range strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' }) {
		n, ok := names[tok]
		if !ok {
			return "", fmt.Errorf("unknown day %q", tok)
		}
		if !seen[n] {
			seen[n] = true
			days = append(days, n)
		}
	}
	if len(days) == 0 {
		return "", fmt.Errorf("no days given")
	}
	var parts []string
	for _, d := range days {
		parts = append(parts, strconv.Itoa(d))
	}
	return strings.Join(parts, ","), nil
}

// weekdayNames renders CSV weekday numbers back into short names.
func weekdayNames(csv string) string {
	short := map[string]string{
		"0": "Sun", "1": "Mon", "2": "Tue", "3": "Wed",
		"4": "Thu", "5": "Fri", "6": "Sat",
	}
	var out []string
	for _, tok := range strings.Split(csv, ",") {
		if s, ok := short[strings.TrimSpace(tok)]; ok {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return csv
	}
	return strings.Join(out, " ")
}

func randomState() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func optionString(i *discordgo.InteractionCreate, name string) string {
	for _, o := range i.ApplicationCommandData().Options {
		if o.Name == name {
			return o.StringValue()
		}
	}
	return ""
}

func optionInt(i *discordgo.InteractionCreate, name string) int64 {
	for _, o := range i.ApplicationCommandData().Options {
		if o.Name == name {
			return o.IntValue()
		}
	}
	return 0
}

func optionBool(i *discordgo.InteractionCreate, name string) bool {
	for _, o := range i.ApplicationCommandData().Options {
		if o.Name == name {
			return o.BoolValue()
		}
	}
	return false
}
