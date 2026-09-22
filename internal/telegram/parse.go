// Package telegram is the Telegram interface for Pulse: plain-text
// commands, draft cards with inline keyboards, and DM reminders over the
// Telegram Bot API using long polling. No gateway intents or privileged
// setup needed: one token from @BotFather is the whole credential.
package telegram

import (
	"fmt"
	"strconv"
	"strings"
)

// Callback actions carried in inline button callback data.
const (
	ActionApprove = "approve"
	ActionEdit    = "edit"
	ActionRegen   = "regen"
	ActionSkip    = "skip"
	ActionSnooze  = "snooze"
)

// ParseCommand splits "/name@bot args..." into its command name and args.
// Non-command text returns empty strings.
func ParseCommand(text string) (name, args string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") || len(text) < 2 {
		return "", ""
	}
	first, rest, _ := strings.Cut(text[1:], " ")
	name, _, _ = strings.Cut(first, "@")
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return "", ""
	}
	return name, strings.TrimSpace(rest)
}

// ParseScheduleTime accepts HH:MM in 24-hour time or 12-hour time with
// am/pm, e.g. 09:00, 3pm, 9:05 AM.
func ParseScheduleTime(s string) (hour, minute int, err error) {
	lowered := strings.ToLower(strings.TrimSpace(s))
	meridiem := ""
	if strings.HasSuffix(lowered, "am") || strings.HasSuffix(lowered, "pm") {
		meridiem = lowered[len(lowered)-2:]
		lowered = strings.TrimSpace(lowered[:len(lowered)-2])
	}
	parts := strings.Split(lowered, ":")
	if len(parts) > 2 {
		return 0, 0, fmt.Errorf("want HH:MM or 3pm")
	}
	h, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("want HH:MM or 3pm")
	}
	m := 0
	if len(parts) == 2 {
		m, err = strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return 0, 0, fmt.Errorf("want HH:MM or 3pm")
		}
	}
	if meridiem != "" {
		if h < 1 || h > 12 || m < 0 || m > 59 {
			return 0, 0, fmt.Errorf("want h[mm]am/pm")
		}
		if h == 12 {
			h = 0
		}
		if meridiem == "pm" {
			h += 12
		}
		return h, m, nil
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("want HH:MM")
	}
	return h, m, nil
}

// ParseDays accepts weekday names, abbreviations, daily, weekdays and
// weekends, and returns CSV time.Weekday numbers.
func ParseDays(s string) (string, error) {
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

// WeekdayNames renders CSV weekday numbers back into short names.
func WeekdayNames(csv string) string {
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

// CallbackData encodes a card button press. It stays well under Telegram's
// 64-byte callback_data limit.
func CallbackData(action string, draftID int64) string {
	return fmt.Sprintf("pulse:%s:%d", action, draftID)
}

// ParseCallbackData splits pulse:<action>:<id> back into its parts.
func ParseCallbackData(s string) (action string, draftID int64, ok bool) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 || parts[0] != "pulse" {
		return "", 0, false
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || id <= 0 {
		return "", 0, false
	}
	return parts[1], id, true
}

// RenderDraftCard builds the message text for a draft. Buttons travel as a
// separate inline keyboard. status is a store.Draft* constant.
func RenderDraftCard(id int64, draftText, articleTitle, status, linkedInURL string) string {
	var b strings.Builder
	switch status {
	case "posted":
		fmt.Fprintf(&b, "Draft #%d posted to LinkedIn", id)
		if linkedInURL != "" {
			fmt.Fprintf(&b, "\n%s", linkedInURL)
		}
		fmt.Fprintf(&b, "\n\n%s", draftText)
		return b.String()
	case "skipped":
		fmt.Fprintf(&b, "Draft #%d skipped. It will not post. Send /draft for another.", id)
		return b.String()
	case "failed":
		fmt.Fprintf(&b, "Draft #%d post failed", id)
	default:
		fmt.Fprintf(&b, "Draft #%d, ready to review", id)
	}
	fmt.Fprintf(&b, "\n\n%s", draftText)
	if articleTitle != "" {
		fmt.Fprintf(&b, "\n\nSource: %s", articleTitle)
	}
	return b.String()
}
