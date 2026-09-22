package linkedin

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/PraisejahOsumgbaBenson/pulse/internal/store"
)

// ConnectResult carries a finished LinkedIn authorization back to the bot.
type ConnectResult struct {
	TelegramUserID int64
	PersonID       string
	PersonName     string
	Scope          string
	ExpiresAt      time.Time
}

// CallbackServer finishes the OAuth dance LinkedIn redirects back to.
type CallbackServer struct {
	store       *store.Store
	client      *Client
	onConnected func(context.Context, ConnectResult)
	logger      *slog.Logger
	mux         *http.ServeMux
}

// NewCallbackServer builds the tiny HTTP server. onConnected is invoked after
// the token is stored so the bot can confirm the link over Telegram.
func NewCallbackServer(st *store.Store, client *Client, onConnected func(context.Context, ConnectResult), logger *slog.Logger) *CallbackServer {
	if logger == nil {
		logger = slog.Default()
	}
	s := &CallbackServer{store: st, client: client, onConnected: onConnected, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleCallback)
	s.mux = mux
	return s
}

// Handler serves the callback endpoint. Register the same path you put in
// LINKEDIN_REDIRECT_URI.
func (s *CallbackServer) Handler() http.Handler {
	return s.mux
}

func (s *CallbackServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	if oauthErr := q.Get("error"); oauthErr != "" {
		desc := q.Get("error_description")
		s.logger.Warn("linkedin authorization denied", "error", oauthErr, "desc", desc)
		writePage(w, "Connection failed", "LinkedIn refused the authorization ("+oauthErr+"). Send /link in Telegram to try again.")
		return
	}
	code, state := q.Get("code"), q.Get("state")
	if code == "" || state == "" {
		writePage(w, "Connection failed", "The callback is missing its code or state. Send /link in Telegram to try again.")
		return
	}

	pending, err := s.store.ConsumeOAuthState(ctx, state)
	if err != nil {
		s.logger.Warn("unknown oauth state", "err", err)
		writePage(w, "Connection failed", "This link already expired or was used. Send /link in Telegram to get a fresh one.")
		return
	}

	tok, err := s.client.Exchange(ctx, code, pending.Verifier)
	if err != nil {
		s.logger.Error("exchange authorization code", "err", err)
		writePage(w, "Connection failed", "LinkedIn would not exchange the code. Send /link in Telegram to try again.")
		return
	}
	prof, err := s.client.Me(ctx, tok.AccessToken)
	if err != nil {
		s.logger.Error("read linkedin profile", "err", err)
		writePage(w, "Connection failed", "Connected, but LinkedIn would not share your profile. Send /link in Telegram to try again.")
		return
	}

	if err := s.store.SaveLinkedInToken(ctx, store.LinkedInToken{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    tok.ExpiresAt.Unix(),
		Scope:        tok.Scope,
		PersonID:     prof.PersonID,
		PersonName:   prof.Name,
	}); err != nil {
		s.logger.Error("store linkedin token", "err", err)
		writePage(w, "Connection failed", "Connected, but Pulse could not save the token. Send /link in Telegram to try again.")
		return
	}
	_ = s.store.PruneOAuthStates(ctx, time.Hour)

	s.logger.Info("linkedin connected", "user", prof.Name, "person", prof.PersonID)
	if s.onConnected != nil {
		s.onConnected(ctx, ConnectResult{
			TelegramUserID: pending.TelegramUserID,
			PersonID:       prof.PersonID,
			PersonName:     prof.Name,
			Scope:          tok.Scope,
			ExpiresAt:      tok.ExpiresAt,
		})
	}
	name := prof.Name
	if name == "" {
		name = "your LinkedIn profile"
	}
	writePage(w, "LinkedIn connected", "Pulse is now linked to "+name+". You can close this tab and return to Telegram.")
}

func writePage(w http.ResponseWriter, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Pulse - %s</title></head><body style="font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem"><h1>%s</h1><p>%s</p></body></html>`, title, title, message)
}
