// Package publisher publishes approved drafts to LinkedIn: it loads the
// stored token, refreshes it when possible, posts, and records the outcome.
package publisher

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/PraisejahOsumgbaBenson/pulse/internal/linkedin"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/store"
)

// ErrNotConnected means no LinkedIn token is stored yet.
var ErrNotConnected = errors.New("linkedin is not connected, run /link first")

// ErrTokenExpired means the token died and cannot be refreshed.
var ErrTokenExpired = errors.New("linkedin token expired, run /link to reconnect")

// Publisher posts drafts.
type Publisher struct {
	store    *store.Store
	linkedin *linkedin.Client
	logger   *slog.Logger
}

// New builds a Publisher.
func New(st *store.Store, li *linkedin.Client, logger *slog.Logger) *Publisher {
	if logger == nil {
		logger = slog.Default()
	}
	return &Publisher{store: st, linkedin: li, logger: logger}
}

// Publish posts one pending or failed draft and returns the share URN.
func (p *Publisher) Publish(ctx context.Context, draftID int64) (string, error) {
	d, err := p.store.GetDraft(ctx, draftID)
	if err != nil {
		return "", fmt.Errorf("load draft %d: %w", draftID, err)
	}
	if d.Status != store.DraftPending && d.Status != store.DraftFailed {
		return "", fmt.Errorf("draft %d is %s, only pending drafts can post", draftID, d.Status)
	}

	tok, err := p.store.GetLinkedInToken(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotConnected
	}
	if err != nil {
		return "", fmt.Errorf("load linkedin token: %w", err)
	}
	if tok.Expired() {
		if tok.RefreshToken == "" {
			return "", ErrTokenExpired
		}
		fresh, err := p.linkedin.Refresh(ctx, tok.RefreshToken)
		if err != nil {
			p.logger.Warn("linkedin refresh failed", "err", err)
			return "", ErrTokenExpired
		}
		tok.AccessToken = fresh.AccessToken
		tok.RefreshToken = fresh.RefreshToken
		tok.ExpiresAt = fresh.ExpiresAt.Unix()
		if fresh.Scope != "" {
			tok.Scope = fresh.Scope
		}
		if err := p.store.SaveLinkedInToken(ctx, tok); err != nil {
			return "", fmt.Errorf("save refreshed token: %w", err)
		}
		p.logger.Info("linkedin token refreshed")
	}

	urn, err := p.linkedin.CreatePost(ctx, tok.AccessToken, tok.PersonID, d.Text)
	if err != nil {
		var apiErr *linkedin.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 401 {
			p.logger.Warn("linkedin rejected token", "err", err)
			if uErr := p.store.UpdateDraftStatus(ctx, draftID, store.DraftFailed, "", "LinkedIn said unauthorized. Run /link to reconnect."); uErr != nil {
				return "", uErr
			}
			return "", ErrTokenExpired
		}
		if uErr := p.store.UpdateDraftStatus(ctx, draftID, store.DraftFailed, "", err.Error()); uErr != nil {
			return "", uErr
		}
		return "", fmt.Errorf("publish draft %d: %w", draftID, err)
	}

	if err := p.store.UpdateDraftStatus(ctx, draftID, store.DraftPosted, urn, ""); err != nil {
		return "", err
	}
	if err := p.store.ClearSnoozesForDraft(ctx, draftID); err != nil {
		p.logger.Warn("clear snoozes after post", "draft", draftID, "err", err)
	}
	p.logger.Info("draft published", "draft", draftID, "urn", urn)
	return urn, nil
}
