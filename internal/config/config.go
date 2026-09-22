// Package config loads Pulse settings from the environment.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Config holds every setting Pulse needs to run.
type Config struct {
	// TelegramToken authenticates the bot to the Telegram Bot API.
	// Talk to @BotFather to create a bot and get its token.
	TelegramToken string
	// TelegramOwnerID optionally restricts commands to a single Telegram
	// user id. Leave empty and the first user to run /start becomes the owner.
	TelegramOwnerID string

	// LinkedInClientID and LinkedInClientSecret identify the LinkedIn developer app.
	LinkedInClientID     string
	LinkedInClientSecret string
	// LinkedInRedirectURI is the exact callback URL registered in the LinkedIn app.
	LinkedInRedirectURI string
	// LinkedInAPIVersion is the YYYYMM version header sent to /rest endpoints.
	LinkedInAPIVersion string

	// OAuthAddr is where the tiny LinkedIn callback server listens, e.g. ":8081".
	OAuthAddr string

	// LLM settings drive draft generation. With no LLMAPIKey the built-in
	// fallback generator is used instead of any network call.
	LLMBaseURL string
	LLMModel   string
	LLMAPIKey  string
	LLMStyle   string

	// DBPath is the SQLite file, created on first run.
	DBPath string
	// Timezone names the IANA zone schedules run in, e.g. "Europe/Berlin".
	Timezone string
	// LogLevel is one of debug, info, warn, error.
	LogLevel string
}

// Load reads the environment, applies defaults, and validates required values.
func Load() (Config, error) {
	cfg := Config{
		TelegramToken:        os.Getenv("TELEGRAM_TOKEN"),
		TelegramOwnerID:      os.Getenv("TELEGRAM_OWNER_ID"),
		LinkedInClientID:     os.Getenv("LINKEDIN_CLIENT_ID"),
		LinkedInClientSecret: os.Getenv("LINKEDIN_CLIENT_SECRET"),
		LinkedInRedirectURI:  os.Getenv("LINKEDIN_REDIRECT_URI"),
		LinkedInAPIVersion:   envOr("LINKEDIN_API_VERSION", "202602"),
		OAuthAddr:            envOr("OAUTH_ADDR", ":8081"),
		LLMBaseURL:           envOr("LLM_BASE_URL", "https://api.openai.com/v1"),
		LLMModel:             envOr("LLM_MODEL", "gpt-4o-mini"),
		LLMAPIKey:            os.Getenv("LLM_API_KEY"),
		LLMStyle:             os.Getenv("LLM_STYLE"),
		DBPath:               envOr("DB_PATH", "pulse.db"),
		Timezone:             envOr("TIMEZONE", "Local"),
		LogLevel:             envOr("LOG_LEVEL", "info"),
	}

	// Only the Telegram token is required to start. LinkedIn values can
	// wait until auto posting matters; /link explains itself until then.
	if strings.TrimSpace(cfg.TelegramToken) == "" {
		return Config{}, fmt.Errorf("missing required environment variables: TELEGRAM_TOKEN")
	}

	if _, err := time.LoadLocation(cfg.Timezone); err != nil {
		return Config{}, fmt.Errorf("invalid TIMEZONE %q: %w", cfg.Timezone, err)
	}

	switch strings.ToLower(cfg.LogLevel) {
	case "debug", "info", "warn", "error":
	default:
		return Config{}, fmt.Errorf("invalid LOG_LEVEL %q: want debug, info, warn or error", cfg.LogLevel)
	}

	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// TimeLocation resolves the configured timezone for scheduling.
func (c Config) TimeLocation() (*time.Location, error) {
	return time.LoadLocation(c.Timezone)
}

// HasLLM reports whether an LLM API key is configured.
func (c Config) HasLLM() bool {
	return strings.TrimSpace(c.LLMAPIKey) != ""
}

// HasLinkedIn reports whether the LinkedIn app credentials are configured.
func (c Config) HasLinkedIn() bool {
	return strings.TrimSpace(c.LinkedInClientID) != "" &&
		strings.TrimSpace(c.LinkedInClientSecret) != "" &&
		strings.TrimSpace(c.LinkedInRedirectURI) != ""
}
