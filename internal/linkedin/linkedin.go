// Package linkedin connects Pulse to LinkedIn: OAuth sign-in with PKCE,
// member identity, and publishing text posts through the Posts API.
package linkedin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

const (
	// authURL and tokenURL are LinkedIn's OAuth 2.0 endpoints.
	authURL  = "https://www.linkedin.com/oauth/v2/authorization"
	tokenURL = "https://www.linkedin.com/oauth/v2/accessToken"
	// defaultUserInfoURL returns the OpenID Connect profile of the member.
	defaultUserInfoURL = "https://api.linkedin.com/v2/userinfo"
	// defaultPostsURL is the versioned Posts API endpoint.
	defaultPostsURL = "https://api.linkedin.com/rest/posts"
	// restliProtocolVersion is required on every /rest call.
	restliProtocolVersion = "2.0.0"
	// maxCommentary is LinkedIn's hard limit for post text.
	maxCommentary = 3000
)

// Scopes requests the member profile plus the right to post on the
// member's own behalf. Both are self-serve products, no review needed.
var Scopes = []string{"openid", "profile", "w_member_social"}

// APIError is a failed LinkedIn API call with the status LinkedIn returned.
type APIError struct {
	Status  int
	Code    int
	Message string
}

func (e *APIError) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("linkedin: status %d code %d: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("linkedin: status %d: %s", e.Status, e.Message)
}

// Client talks to LinkedIn's OAuth and Posts APIs.
type Client struct {
	oauth      *oauth2.Config
	http       *http.Client
	userInfoURL string
	postsURL    string
	version     string
	logger      *slog.Logger
}

// New builds a Client for a LinkedIn developer app. version is the YYYYMM
// API version sent in the LinkedIn-Version header.
func New(clientID, clientSecret, redirectURI, version string, logger *slog.Logger) *Client {
	return newClient(clientID, clientSecret, redirectURI, version,
		defaultUserInfoURL, defaultPostsURL, logger)
}

func newClient(clientID, clientSecret, redirectURI, version, userInfoURL, postsURL string, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		oauth: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  redirectURI,
			Scopes:       Scopes,
			Endpoint: oauth2.Endpoint{
				AuthURL:  authURL,
				TokenURL: tokenURL,
			},
		},
		http:        &http.Client{Timeout: 20 * time.Second},
		userInfoURL: userInfoURL,
		postsURL:    postsURL,
		version:     version,
		logger:      logger,
	}
}

// AuthURL starts a sign-in: it returns the LinkedIn URL to open and the PKCE
// verifier the callback must present when exchanging the code.
func (c *Client) AuthURL(state string) (url, verifier string, err error) {
	verifier = oauth2.GenerateVerifier()
	url = c.oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))
	return url, verifier, nil
}

// Token is an exchanged OAuth credential.
type Token struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	Scope        string
}

// Exchange swaps an authorization code plus the PKCE verifier for a Token.
func (c *Client) Exchange(ctx context.Context, code, verifier string) (Token, error) {
	t, err := c.oauth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return Token{}, fmt.Errorf("exchange authorization code: %w", err)
	}
	scope, _ := t.Extra("scope").(string)
	if scope == "" {
		scope = strings.Join(Scopes, " ")
	}
	return Token{
		AccessToken:  t.AccessToken,
		RefreshToken: t.RefreshToken,
		ExpiresAt:    t.Expiry,
		Scope:        scope,
	}, nil
}

// Refresh mints a fresh access token from a stored refresh token. Consumer
// apps only receive refresh tokens when LinkedIn issues one; otherwise
// re-authorization through AuthURL is the only path.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (Token, error) {
	src := c.oauth.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken})
	t, err := src.Token()
	if err != nil {
		return Token{}, fmt.Errorf("refresh access token: %w", err)
	}
	scope, _ := t.Extra("scope").(string)
	return Token{
		AccessToken:  t.AccessToken,
		RefreshToken: t.RefreshToken,
		ExpiresAt:    t.Expiry,
		Scope:        scope,
	}, nil
}

// Profile identifies the authenticated member.
type Profile struct {
	// PersonID is the author URN, e.g. urn:li:person:abc123.
	PersonID string
	Name     string
}

// Me reads the OpenID Connect userinfo to learn who authorized the app.
func (c *Client) Me(ctx context.Context, accessToken string) (Profile, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.userInfoURL, nil)
	if err != nil {
		return Profile{}, fmt.Errorf("build userinfo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return Profile{}, fmt.Errorf("call userinfo: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Profile{}, fmt.Errorf("read userinfo: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Profile{}, parseAPIError(resp.StatusCode, body)
	}
	var info struct {
		Sub  string `json:"sub"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return Profile{}, fmt.Errorf("decode userinfo: %w", err)
	}
	if info.Sub == "" {
		return Profile{}, fmt.Errorf("userinfo missing subject claim")
	}
	return Profile{PersonID: "urn:li:person:" + info.Sub, Name: info.Name}, nil
}

// CreatePost publishes a text post to the member's own feed and returns the
// share URN from the x-restli-id response header.
func (c *Client) CreatePost(ctx context.Context, accessToken, authorURN, commentary string) (string, error) {
	commentary = truncateCommentary(commentary)
	payload := map[string]any{
		"author":     authorURN,
		"commentary": commentary,
		"visibility": "PUBLIC",
		"distribution": map[string]any{
			"feedDistribution":               "MAIN_FEED",
			"targetEntities":                 []any{},
			"thirdPartyDistributionChannels": []any{},
		},
		"lifecycleState": "PUBLISHED",
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode post: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.postsURL, bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("build post request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("LinkedIn-Version", c.version)
	req.Header.Set("X-Restli-Protocol-Version", restliProtocolVersion)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("call posts api: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return "", parseAPIError(resp.StatusCode, body)
	}
	urn := resp.Header.Get("x-restli-id")
	if urn == "" {
		return "", fmt.Errorf("posts api returned 201 without x-restli-id")
	}
	c.logger.Info("linkedin post published", "urn", urn)
	return urn, nil
}

// PostURL turns a share URN into the public feed URL.
func PostURL(urn string) string {
	return "https://www.linkedin.com/feed/update/" + urn + "/"
}

func truncateCommentary(s string) string {
	r := []rune(s)
	if len(r) <= maxCommentary {
		return s
	}
	return string(r[:maxCommentary-1]) + "…"
}

func parseAPIError(status int, body []byte) *APIError {
	var doc struct {
		Message          string `json:"message"`
		Status           int    `json:"status"`
		ServiceErrorCode int    `json:"serviceErrorCode"`
	}
	if err := json.Unmarshal(body, &doc); err != nil || doc.Message == "" {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = http.StatusText(status)
		}
		return &APIError{Status: status, Message: msg}
	}
	return &APIError{Status: status, Code: doc.ServiceErrorCode, Message: doc.Message}
}
