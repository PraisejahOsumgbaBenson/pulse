// Command pulse runs the Discord bot: slash commands, the LinkedIn OAuth
// callback server, and the reminder scheduler in one process.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/PraisejahOsumgbaBenson/pulse/internal/config"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/discord"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/feeds"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/generate"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/linkedin"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/publisher"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/scheduler"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/store"
)

func main() {
	if err := run(); err != nil {
		_, _ = os.Stderr.WriteString("pulse: " + err.Error() + "\n")
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := newLogger(cfg.LogLevel)
	loc, err := cfg.TimeLocation()
	if err != nil {
		return err
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}

	li := linkedin.New(cfg.LinkedInClientID, cfg.LinkedInClientSecret, cfg.LinkedInRedirectURI, cfg.LinkedInAPIVersion, logger)
	feedSvc := feeds.New(st, logger)
	gen := generate.New(cfg.LLMBaseURL, cfg.LLMModel, cfg.LLMAPIKey, cfg.LLMStyle, logger)
	pub := publisher.New(st, li, logger)

	bot, err := discord.New(cfg.DiscordToken, discord.Deps{
		Store:        st,
		Feeds:        feedSvc,
		Gen:          gen,
		LinkedIn:     li,
		Pub:          pub,
		Location:     loc,
		TimezoneName: cfg.Timezone,
		OwnerID:      cfg.DiscordOwnerID,
		DevGuildID:   cfg.DevGuildID,
		Logger:       logger,
	})
	if err != nil {
		st.Close()
		return err
	}

	mux := http.NewServeMux()
	cb := linkedin.NewCallbackServer(st, li, func(ctx context.Context, res linkedin.ConnectResult) {
		name := res.PersonName
		if name == "" {
			name = "your LinkedIn profile"
		}
		if err := bot.SendText(ctx, res.DiscordUserID, "LinkedIn connected as "+name+". Reminders will post on your approval."); err != nil {
			logger.Warn("confirm link over dm", "err", err)
		}
	}, logger)
	mux.Handle(callbackPath(cfg.LinkedInRedirectURI), cb.Handler())
	httpSrv := &http.Server{Addr: cfg.OAuthAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		logger.Info("oauth callback listening", "addr", cfg.OAuthAddr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("oauth callback server failed", "err", err)
		}
	}()

	sched := scheduler.New(st, feedSvc, gen, bot, pub, loc, cfg.Timezone, 30*time.Second, logger)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go sched.Run(ctx)

	if err := bot.Start(); err != nil {
		stop()
		st.Close()
		return err
	}
	logger.Info("pulse running", "db", cfg.DBPath, "generator", gen.Name(), "timezone", cfg.Timezone)

	<-ctx.Done()
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	_ = bot.Close()
	_ = st.Close()
	return nil
}

// callbackPath extracts the mount path from the registered redirect URI.
func callbackPath(redirectURI string) string {
	u, err := url.Parse(redirectURI)
	if err != nil || u.Path == "" {
		return "/"
	}
	return u.Path
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}
