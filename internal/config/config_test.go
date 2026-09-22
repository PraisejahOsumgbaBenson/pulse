package config_test

import (
	"testing"

	"github.com/PraisejahOsumgbaBenson/pulse/internal/config"
)

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("TELEGRAM_TOKEN", "telegram-token")
	t.Setenv("LINKEDIN_CLIENT_ID", "client-id")
	t.Setenv("LINKEDIN_CLIENT_SECRET", "client-secret")
	t.Setenv("LINKEDIN_REDIRECT_URI", "http://localhost:8081/oauth/linkedin/callback")
}

func TestLoadAppliesDefaults(t *testing.T) {
	setRequiredEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.TelegramToken != "telegram-token" {
		t.Errorf("TelegramToken = %q, want telegram-token", cfg.TelegramToken)
	}
	if cfg.LinkedInAPIVersion != "202602" {
		t.Errorf("LinkedInAPIVersion = %q, want 202602", cfg.LinkedInAPIVersion)
	}
	if cfg.OAuthAddr != ":8081" {
		t.Errorf("OAuthAddr = %q, want :8081", cfg.OAuthAddr)
	}
	if cfg.LLMBaseURL != "https://api.openai.com/v1" {
		t.Errorf("LLMBaseURL = %q, want OpenAI default", cfg.LLMBaseURL)
	}
	if cfg.DBPath != "pulse.db" {
		t.Errorf("DBPath = %q, want pulse.db", cfg.DBPath)
	}
	if cfg.HasLLM() {
		t.Error("HasLLM() = true, want false with no key")
	}
	t.Setenv("LLM_API_KEY", "sk-test")
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if !cfg.HasLLM() {
		t.Error("HasLLM() = false, want true with key set")
	}
}

func TestLoadRejectsMissingRequired(t *testing.T) {
	t.Setenv("TELEGRAM_TOKEN", "")
	t.Setenv("LINKEDIN_CLIENT_ID", "")
	t.Setenv("LINKEDIN_CLIENT_SECRET", "")
	t.Setenv("LINKEDIN_REDIRECT_URI", "")
	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() = nil error, want missing-variable error")
	}
}

func TestLoadAllowsMissingLinkedIn(t *testing.T) {
	t.Setenv("TELEGRAM_TOKEN", "telegram-token")
	t.Setenv("LINKEDIN_CLIENT_ID", "")
	t.Setenv("LINKEDIN_CLIENT_SECRET", "")
	t.Setenv("LINKEDIN_REDIRECT_URI", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() without LinkedIn returned error: %v", err)
	}
	if cfg.HasLinkedIn() {
		t.Error("HasLinkedIn() = true, want false with no credentials")
	}
	setRequiredEnv(t)
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if !cfg.HasLinkedIn() {
		t.Error("HasLinkedIn() = false, want true with credentials set")
	}
}

func TestLoadRejectsBadTimezoneAndLogLevel(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("TIMEZONE", "Not/AZone")
	if _, err := config.Load(); err == nil {
		t.Error("Load() accepted bad TIMEZONE, want error")
	}
	t.Setenv("TIMEZONE", "UTC")
	t.Setenv("LOG_LEVEL", "chatty")
	if _, err := config.Load(); err == nil {
		t.Error("Load() accepted bad LOG_LEVEL, want error")
	}
}
