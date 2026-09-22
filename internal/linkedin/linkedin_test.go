package linkedin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/PraisejahOsumgbaBenson/pulse/internal/store"
)

func testClient(userInfoURL, postsURL string) *Client {
	c := New("id", "secret", "http://localhost:8081/callback", "202602", slog.Default())
	c.userInfoURL = userInfoURL
	c.postsURL = postsURL
	return c
}

func TestAuthURLIncludesScopesAndPKCE(t *testing.T) {
	c := testClient("", "")
	url, verifier, err := c.AuthURL("state123")
	if err != nil {
		t.Fatalf("AuthURL() returned error: %v", err)
	}
	if verifier == "" {
		t.Fatal("AuthURL() returned empty verifier")
	}
	for _, want := range []string{
		"client_id=id", "redirect_uri=", "state=state123",
		"code_challenge=", "code_challenge_method=S256",
		"scope=", "w_member_social",
	} {
		if !strings.Contains(url, want) {
			t.Errorf("AuthURL() = %q, missing %q", url, want)
		}
	}
}

func TestExchangeAndRefresh(t *testing.T) {
	var gotVerifier string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm() returned error: %v", err)
		}
		gotVerifier = r.Form.Get("code_verifier")
		grant := r.Form.Get("grant_type")
		resp := map[string]any{"access_token": "a-" + grant, "expires_in": 3600, "scope": "openid profile w_member_social"}
		if grant == "authorization_code" {
			resp["refresh_token"] = "r1"
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := testClient("", "")
	c.oauth.Endpoint.TokenURL = srv.URL
	ctx := context.Background()
	tok, err := c.Exchange(ctx, "code1", "verifier1")
	if err != nil {
		t.Fatalf("Exchange() returned error: %v", err)
	}
	if tok.AccessToken != "a-authorization_code" || tok.RefreshToken != "r1" {
		t.Errorf("Exchange() = %+v, want tokens from test server", tok)
	}
	if gotVerifier != "verifier1" {
		t.Errorf("code_verifier sent = %q, want verifier1", gotVerifier)
	}
	if time.Until(tok.ExpiresAt) > time.Hour || time.Until(tok.ExpiresAt) <= 0 {
		t.Errorf("ExpiresAt = %v, want about an hour out", tok.ExpiresAt)
	}

	fresh, err := c.Refresh(ctx, "r1")
	if err != nil {
		t.Fatalf("Refresh() returned error: %v", err)
	}
	if fresh.AccessToken != "a-refresh_token" {
		t.Errorf("Refresh() = %+v, want refreshed token", fresh)
	}
}

func TestMe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("Authorization = %q, want Bearer tok", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"sub": "abc123", "name": "Test User"})
	}))
	defer srv.Close()

	c := testClient(srv.URL, "")
	prof, err := c.Me(context.Background(), "tok")
	if err != nil {
		t.Fatalf("Me() returned error: %v", err)
	}
	if prof.PersonID != "urn:li:person:abc123" || prof.Name != "Test User" {
		t.Errorf("Me() = %+v, want person urn and name", prof)
	}
}

func TestCreatePostSuccessChecksHeadersAndBody(t *testing.T) {
	var gotHeaders http.Header
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("x-restli-id", "urn:li:share:999")
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	c := testClient("", srv.URL)
	urn, err := c.CreatePost(context.Background(), "tok", "urn:li:person:abc", "hello")
	if err != nil {
		t.Fatalf("CreatePost() returned error: %v", err)
	}
	if urn != "urn:li:share:999" {
		t.Errorf("CreatePost() urn = %q, want urn:li:share:999", urn)
	}
	if gotHeaders.Get("LinkedIn-Version") != "202602" {
		t.Errorf("LinkedIn-Version = %q, want 202602", gotHeaders.Get("LinkedIn-Version"))
	}
	if gotHeaders.Get("X-Restli-Protocol-Version") != "2.0.0" {
		t.Errorf("X-Restli-Protocol-Version = %q, want 2.0.0", gotHeaders.Get("X-Restli-Protocol-Version"))
	}
	if gotBody["author"] != "urn:li:person:abc" || gotBody["lifecycleState"] != "PUBLISHED" {
		t.Errorf("CreatePost() body = %v, want author and PUBLISHED", gotBody)
	}
}

func TestCreatePostSurfacesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{
			"message": "Invalid access token", "status": 401, "serviceErrorCode": 65600,
		})
	}))
	defer srv.Close()

	c := testClient("", srv.URL)
	_, err := c.CreatePost(context.Background(), "bad", "urn:li:person:abc", "hi")
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("CreatePost() err type = %T, want *APIError", err)
	}
	if apiErr.Status != 401 || !strings.Contains(apiErr.Message, "Invalid access token") {
		t.Errorf("CreatePost() err = %v, want 401 with message", err)
	}
}

func TestCreatePostTruncatesLongText(t *testing.T) {
	var length int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		length = utf8.RuneCountInString(body["commentary"].(string))
		w.Header().Set("x-restli-id", "urn:li:share:1")
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	c := testClient("", srv.URL)
	long := strings.Repeat("x", 4000)
	if _, err := c.CreatePost(context.Background(), "tok", "urn:li:person:abc", long); err != nil {
		t.Fatalf("CreatePost() returned error: %v", err)
	}
	if length > 3000 {
		t.Errorf("commentary length = %d, want at most 3000", length)
	}
}

func TestTruncateCommentaryKeepsUnicodeIntact(t *testing.T) {
	long := strings.Repeat("é", 4000)
	got := truncateCommentary(long)
	if n := utf8.RuneCountInString(got); n != 3000 {
		t.Errorf("RuneCountInString() = %d, want 3000", n)
	}
	if !utf8.ValidString(got) {
		t.Error("truncateCommentary() produced invalid UTF-8")
	}
	if got == long {
		t.Error("truncateCommentary() returned the input unchanged, want truncation")
	}
	short := "hello"
	if truncateCommentary(short) != short {
		t.Error("truncateCommentary() changed short input, want it untouched")
	}
}

func TestPostURL(t *testing.T) {
	got := PostURL("urn:li:share:999")
	want := "https://www.linkedin.com/feed/update/urn:li:share:999/"
	if got != want {
		t.Errorf("PostURL() = %q, want %q", got, want)
	}
}

func TestCallbackServerEndToEnd(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}
	defer st.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "a1", "refresh_token": "r1", "expires_in": 3600,
		})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"sub": "u1", "name": "Callback User"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := testClient(srv.URL+"/userinfo", "")
	c.oauth.Endpoint.TokenURL = srv.URL + "/token"

	var notified ConnectResult
	cb := NewCallbackServer(st, c, func(_ context.Context, res ConnectResult) {
		notified = res
	}, slog.Default())

	if err := st.SaveOAuthState(ctx, store.OAuthState{State: "st1", TelegramUserID: 444, Verifier: "v1"}); err != nil {
		t.Fatalf("SaveOAuthState() returned error: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/callback?code=c1&state=st1", nil)
	cb.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("callback status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "LinkedIn connected") {
		t.Errorf("callback body = %q, want success page", rec.Body.String())
	}
	tok, err := st.GetLinkedInToken(ctx)
	if err != nil {
		t.Fatalf("GetLinkedInToken() returned error: %v", err)
	}
	if tok.AccessToken != "a1" || tok.PersonID != "urn:li:person:u1" || tok.PersonName != "Callback User" {
		t.Errorf("stored token = %+v, want exchanged values", tok)
	}
	if notified.TelegramUserID != 444 || notified.PersonName != "Callback User" {
		t.Errorf("onConnected = %+v, want du1 and name", notified)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/callback?code=c1&state=stale", nil)
	cb.Handler().ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "already expired") {
		t.Errorf("stale state body = %q, want expiry page", rec.Body.String())
	}
}
