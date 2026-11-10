// Command pulse runs the Telegram bot: commands, the LinkedIn OAuth
// callback server, and the reminder scheduler in one process.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/PraisejahOsumgbaBenson/pulse/internal/config"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/feeds"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/generate"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/linkedin"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/publisher"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/scheduler"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/store"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/telegram"
)

func main() {
	if err := run(); err != nil {
		_, _ = os.Stderr.WriteString("pulse: " + err.Error() + "\n")
		os.Exit(1)
	}
}

func run() error {
	if err := loadDotEnv(); err != nil {
		return err
	}
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

	bot, err := telegram.New(cfg.TelegramToken, telegram.Deps{
		Store:              st,
		Feeds:              feedSvc,
		Gen:                gen,
		LinkedIn:           li,
		LinkedInConfigured: cfg.HasLinkedIn(),
		Pub:                pub,
		Location:           loc,
		TimezoneName:       cfg.Timezone,
		OwnerID:            cfg.TelegramOwnerID,
		Logger:             logger,
	})
	if err != nil {
		st.Close()
		return err
	}

	cb := linkedin.NewCallbackServer(st, li, func(ctx context.Context, res linkedin.ConnectResult) {
		name := res.PersonName
		if name == "" {
			name = "your LinkedIn profile"
		}
		if err := bot.SendText(ctx, res.TelegramUserID, "LinkedIn connected as "+name+". Reminders will post on your approval."); err != nil {
			logger.Warn("confirm link over telegram", "err", err)
		}
	}, logger)
	handler := newHTTPHandler(cb, cfg.HasLinkedIn(), callbackPath(cfg.LinkedInRedirectURI))
	httpSrv := &http.Server{Addr: cfg.OAuthAddr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		logger.Info("oauth callback listening", "addr", cfg.OAuthAddr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("oauth callback server failed", "err", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sched := scheduler.New(st, feedSvc, gen, bot, pub, loc, cfg.Timezone, 30*time.Second, logger)
	go sched.Run(ctx)

	if err := bot.Start(ctx); err != nil {
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
	bot.Stop()
	_ = st.Close()
	return nil
}

// loadDotEnv reads .env when present. Real environment variables win, so a
// container or process manager can override the file.
func loadDotEnv() error {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("load .env: %w", err)
	}
	return nil
}

// newHTTPHandler serves the OAuth callback, a health check, and a small
// landing page so a self-hosted instance can be checked from a browser.
func newHTTPHandler(cb *linkedin.CallbackServer, linkedInConfigured bool, callbackPath string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthHandler)
	if linkedInConfigured {
		mux.Handle(callbackPath, cb.Handler())
		// A redirect URI with no path owns "/", so the landing page would
		// shadow the callback. Keep the callback and skip the page.
		if callbackPath == "/" {
			return mux
		}
	}
	mux.HandleFunc("/", landingHandler)
	return mux
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, "ok\n")
}

func landingHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, landingPage)
}

const landingPage = `<!doctype html>
<html lang="en">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Pulse</title>
<style>
  :root { color-scheme: light dark; }
  body { font-family: system-ui, sans-serif; max-width: 36rem; margin: 4rem auto; padding: 0 1rem; line-height: 1.6; }
  code { background: rgba(127, 127, 127, .15); padding: .1rem .3rem; border-radius: .25rem; }
</style>
<h1>Pulse is running</h1>
<p>This host receives LinkedIn OAuth callbacks for the Pulse Telegram bot.</p>
<p>Message your bot on Telegram to claim it and set your schedule.</p>
<p>Health check: <code>/healthz</code></p>
</html>
`

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
