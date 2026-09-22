package telegram_test

import (
	"testing"

	"github.com/PraisejahOsumgbaBenson/pulse/internal/telegram"
)

func TestParseCommand(t *testing.T) {
	cases := []struct {
		in   string
		name string
		args string
	}{
		{"/start", "start", ""},
		{"/draft", "draft", ""},
		{"/source_add https://example.com/feed", "source_add", "https://example.com/feed"},
		{"/schedule_set 09:00 monday wednesday", "schedule_set", "09:00 monday wednesday"},
		{"/draft@pulsebot", "draft", ""},
		{"/history 5", "history", "5"},
		{"hello world", "", ""},
		{"", "", ""},
		{"/", "", ""},
	}
	for _, tc := range cases {
		name, args := telegram.ParseCommand(tc.in)
		if name != tc.name || args != tc.args {
			t.Errorf("ParseCommand(%q) = (%q, %q), want (%q, %q)",
				tc.in, name, args, tc.name, tc.args)
		}
	}
}

func TestParseScheduleTime(t *testing.T) {
	cases := []struct {
		in   string
		h, m int
	}{
		{"09:00", 9, 0},
		{"3pm", 15, 0},
		{"9am", 9, 0},
		{"12:30pm", 12, 30},
		{"12am", 0, 0},
		{"12pm", 12, 0},
		{"9:05 AM", 9, 5},
		{"6:45 pm", 18, 45},
		{"9", 9, 0},
	}
	for _, tc := range cases {
		h, m, err := telegram.ParseScheduleTime(tc.in)
		if err != nil || h != tc.h || m != tc.m {
			t.Errorf("ParseScheduleTime(%q) = %d, %d, %v; want %d, %d, nil", tc.in, h, m, err, tc.h, tc.m)
		}
	}
	for _, bad := range []string{"25:00", "09:60", "morning", "", "13pm", "9:61am", "9:00:00"} {
		if _, _, err := telegram.ParseScheduleTime(bad); err == nil {
			t.Errorf("ParseScheduleTime(%q) = nil error, want error", bad)
		}
	}
}

func TestParseDays(t *testing.T) {
	got, err := telegram.ParseDays("monday wednesday friday")
	if err != nil || got != "1,3,5" {
		t.Errorf("ParseDays() = %q, %v; want 1,3,5, nil", got, err)
	}
	for in, want := range map[string]string{
		"daily":    "0,1,2,3,4,5,6",
		"weekdays": "1,2,3,4,5",
		"weekends": "0,6",
	} {
		got, err := telegram.ParseDays(in)
		if err != nil || got != want {
			t.Errorf("ParseDays(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := telegram.ParseDays("funday"); err == nil {
		t.Error("ParseDays(funday) = nil error, want error")
	}
}

func TestCallbackDataRoundTrip(t *testing.T) {
	id := telegram.CallbackData("approve", 42)
	action, draftID, ok := telegram.ParseCallbackData(id)
	if !ok || action != "approve" || draftID != 42 {
		t.Errorf("round trip = (%q, %d, %v), want (approve, 42, true)", action, draftID, ok)
	}
	for _, bad := range []string{"", "pulse:approve", "other:approve:1", "pulse:approve:abc", "pulse:approve:0"} {
		if _, _, ok := telegram.ParseCallbackData(bad); ok {
			t.Errorf("ParseCallbackData(%q) = ok, want not-ok", bad)
		}
	}
	if len(id) > 64 {
		t.Errorf("callback data %q exceeds Telegram 64-byte limit", id)
	}
}

func TestRenderDraftCard(t *testing.T) {
	text := telegram.RenderDraftCard(7, "Hello\n\nWorld", "Example Feed", "pending", "")
	if text == "" {
		t.Fatal("RenderDraftCard() = empty, want card text")
	}
	for _, want := range []string{"#7", "Hello", "Example Feed"} {
		if !contains(text, want) {
			t.Errorf("card text missing %q:\n%s", want, text)
		}
	}
	posted := telegram.RenderDraftCard(7, "Hello", "", "posted", "https://www.linkedin.com/feed/update/urn:li:share:1/")
	if !contains(posted, "posted") {
		t.Errorf("posted card missing status:\n%s", posted)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
