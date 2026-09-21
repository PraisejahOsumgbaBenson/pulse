package discord

import (
	"strings"
	"testing"

	"github.com/PraisejahOsumgbaBenson/pulse/internal/store"
	"github.com/bwmarrin/discordgo"
)

func TestParseCustomID(t *testing.T) {
	action, id, ok := parseCustomID("pulse:approve:12")
	if !ok || action != "approve" || id != 12 {
		t.Errorf("parseCustomID() = %q %d %v, want approve 12 true", action, id, ok)
	}
	for _, bad := range []string{"", "pulse:approve", "other:approve:1", "pulse:approve:0", "pulse:approve:x"} {
		if _, _, ok := parseCustomID(bad); ok {
			t.Errorf("parseCustomID(%q) = true, want false", bad)
		}
	}
}

func TestRenderCardPendingHasFiveButtons(t *testing.T) {
	d := store.Draft{ID: 3, Text: "hello", Status: store.DraftPending}
	embeds, components := renderCard(d, "Some Article")
	if len(embeds) != 1 || !strings.Contains(embeds[0].Title, "#3") {
		t.Errorf("pending embed = %+v, want title with #3", embeds)
	}
	if len(components) != 1 {
		t.Fatalf("pending components rows = %d, want 1", len(components))
	}
	row, ok := components[0].(*discordgo.ActionsRow)
	if !ok || len(row.Components) != 5 {
		t.Fatalf("pending buttons = %+v, want 5 in one row", components)
	}
	wantIDs := []string{"pulse:approve:3", "pulse:edit:3", "pulse:regen:3", "pulse:skip:3", "pulse:snooze:3"}
	for i, c := range row.Components {
		btn, ok := c.(*discordgo.Button)
		if !ok || btn.CustomID != wantIDs[i] {
			t.Errorf("button %d = %+v, want custom id %q", i, c, wantIDs[i])
		}
	}
	if embeds[0].Footer == nil || !strings.Contains(embeds[0].Footer.Text, "Some Article") {
		t.Errorf("pending footer = %+v, want source title", embeds[0].Footer)
	}
}

func TestRenderCardPostedHasLinkButton(t *testing.T) {
	d := store.Draft{ID: 4, Text: "done", Status: store.DraftPosted, LinkedInURN: "urn:li:share:1"}
	embeds, components := renderCard(d, "")
	if len(embeds) != 1 || !strings.Contains(embeds[0].Title, "posted") {
		t.Errorf("posted embed = %+v, want posted title", embeds)
	}
	if len(components) != 1 {
		t.Fatalf("posted components = %d rows, want 1", len(components))
	}
	row := components[0].(*discordgo.ActionsRow)
	btn, ok := row.Components[0].(*discordgo.Button)
	if !ok || btn.Style != discordgo.LinkButton {
		t.Fatalf("posted button = %+v, want link button", row.Components[0])
	}
	want := "https://www.linkedin.com/feed/update/urn:li:share:1/"
	if btn.URL != want {
		t.Errorf("link button url = %q, want %q", btn.URL, want)
	}
}

func TestRenderCardSkippedHasNoButtons(t *testing.T) {
	d := store.Draft{ID: 5, Text: "x", Status: store.DraftSkipped}
	embeds, components := renderCard(d, "")
	if len(embeds) != 1 || !strings.Contains(embeds[0].Title, "skipped") {
		t.Errorf("skipped embed = %+v, want skipped title", embeds)
	}
	if len(components) != 0 {
		t.Errorf("skipped components = %v, want none", components)
	}
}

func TestRenderCardFailedShowsError(t *testing.T) {
	d := store.Draft{ID: 6, Text: "x", Status: store.DraftFailed, Error: "boom"}
	embeds, components := renderCard(d, "")
	if len(embeds) != 1 || !strings.Contains(embeds[0].Title, "failed") {
		t.Errorf("failed embed = %+v, want failed title", embeds)
	}
	if len(embeds[0].Fields) != 1 || embeds[0].Fields[0].Value != "boom" {
		t.Errorf("failed fields = %+v, want the error", embeds[0].Fields)
	}
	if len(components) != 1 {
		t.Errorf("failed components rows = %d, want buttons kept for retry", len(components))
	}
}

func TestParseScheduleTime(t *testing.T) {
	c, err := parseScheduleTime("09:00")
	if err != nil || c.hour != 9 || c.minute != 0 {
		t.Errorf("parseScheduleTime(09:00) = %+v %v, want 9 0 nil", c, err)
	}
	c, err = parseScheduleTime("18:30")
	if err != nil || c.hour != 18 || c.minute != 30 {
		t.Errorf("parseScheduleTime(18:30) = %+v %v, want 18 30 nil", c, err)
	}
	for _, bad := range []string{"", "9", "9am", "25:00", "09:60", "09-00"} {
		if _, err := parseScheduleTime(bad); err == nil {
			t.Errorf("parseScheduleTime(%q) = nil, want error", bad)
		}
	}
}

func TestParseDays(t *testing.T) {
	got, err := parseDays("monday wednesday friday")
	if err != nil || got != "1,3,5" {
		t.Errorf("parseDays() = %q %v, want 1,3,5 nil", got, err)
	}
	got, err = parseDays("Mon, Wed")
	if err != nil || got != "1,3" {
		t.Errorf("parseDays(abbrev) = %q %v, want 1,3 nil", got, err)
	}
	for input, want := range map[string]string{
		"daily": "0,1,2,3,4,5,6", "weekdays": "1,2,3,4,5", "weekends": "0,6",
	} {
		if got, err := parseDays(input); err != nil || got != want {
			t.Errorf("parseDays(%q) = %q %v, want %q nil", input, got, err, want)
		}
	}
	if _, err := parseDays("funday"); err == nil {
		t.Error("parseDays(funday) = nil, want error")
	}
	if _, err := parseDays(""); err == nil {
		t.Error("parseDays(\"\") = nil, want error")
	}
}

func TestWeekdayNames(t *testing.T) {
	if got := weekdayNames("1,3,5"); got != "Mon Wed Fri" {
		t.Errorf("weekdayNames() = %q, want Mon Wed Fri", got)
	}
}

func TestExcerpt(t *testing.T) {
	if got := excerpt("  hello   world  ", 80); got != "hello world" {
		t.Errorf("excerpt() = %q, want collapsed spaces", got)
	}
	long := strings.Repeat("a", 100)
	if got := excerpt(long, 80); len([]rune(got)) != 80 {
		t.Errorf("excerpt runes = %d, want 80", len([]rune(got)))
	}
}

func TestModalText(t *testing.T) {
	data := discordgo.ModalSubmitInteractionData{
		CustomID: "pulse:editmodal:1",
		Components: []discordgo.MessageComponent{
			&discordgo.ActionsRow{Components: []discordgo.MessageComponent{
				&discordgo.TextInput{CustomID: "pulse:edittext", Value: "new text"},
			}},
		},
	}
	if got := modalText(data, "pulse:edittext"); got != "new text" {
		t.Errorf("modalText() = %q, want new text", got)
	}
	if got := modalText(data, "missing"); got != "" {
		t.Errorf("modalText(missing) = %q, want empty", got)
	}
}
