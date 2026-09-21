// Package store persists Pulse state in SQLite: settings, the LinkedIn
// token, OAuth states, sources, articles, drafts, schedules and snoozes.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Draft statuses.
const (
	DraftPending = "pending"
	DraftPosted  = "posted"
	DraftSkipped = "skipped"
	DraftFailed  = "failed"
)

// Source kinds.
const (
	SourceRSS  = "rss"
	SourceLink = "link"
)

const schema = `
CREATE TABLE IF NOT EXISTS kv (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS linkedin_token (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	access_token TEXT NOT NULL,
	refresh_token TEXT NOT NULL DEFAULT '',
	expires_at INTEGER NOT NULL DEFAULT 0,
	scope TEXT NOT NULL DEFAULT '',
	person_id TEXT NOT NULL DEFAULT '',
	person_name TEXT NOT NULL DEFAULT '',
	updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS oauth_states (
	state TEXT PRIMARY KEY,
	discord_user_id TEXT NOT NULL,
	verifier TEXT NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sources (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	kind TEXT NOT NULL,
	url TEXT NOT NULL UNIQUE,
	title TEXT NOT NULL DEFAULT '',
	last_fetched_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS articles (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	source_id INTEGER NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
	url TEXT NOT NULL UNIQUE,
	title TEXT NOT NULL DEFAULT '',
	summary TEXT NOT NULL DEFAULT '',
	body TEXT NOT NULL DEFAULT '',
	published_at INTEGER NOT NULL DEFAULT 0,
	used INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS drafts (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	article_id INTEGER NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
	text TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'pending',
	linkedin_urn TEXT NOT NULL DEFAULT '',
	error TEXT NOT NULL DEFAULT '',
	discord_channel_id TEXT NOT NULL DEFAULT '',
	discord_message_id TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS schedules (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	days TEXT NOT NULL,
	hour INTEGER NOT NULL,
	minute INTEGER NOT NULL,
	autopost INTEGER NOT NULL DEFAULT 0,
	timezone TEXT NOT NULL DEFAULT '',
	enabled INTEGER NOT NULL DEFAULT 1,
	last_fired_slot TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS snoozes (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	draft_id INTEGER NOT NULL REFERENCES drafts(id) ON DELETE CASCADE,
	fire_at INTEGER NOT NULL,
	fired INTEGER NOT NULL DEFAULT 0
);
`

// Store wraps a SQLite database. It keeps a single open connection because
// Pulse is a single-user bot and this keeps PRAGMA behavior predictable.
type Store struct {
	db *sql.DB
}

// Open creates the database file if needed, applies the schema, and returns
// a ready Store.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the database.
func (s *Store) Close() error {
	return s.db.Close()
}

func now() int64 {
	return time.Now().Unix()
}

// KVSet stores a small string value.
func (s *Store) KVSet(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO kv(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("kv set %q: %w", key, err)
	}
	return nil
}

// KVGet returns a stored value and whether it exists.
func (s *Store) KVGet(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM kv WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("kv get %q: %w", key, err)
	}
	return value, true, nil
}

// LinkedInToken is the stored OAuth credential plus the member identity.
type LinkedInToken struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64
	Scope        string
	PersonID     string
	PersonName   string
	UpdatedAt    int64
}

// Expired reports whether the access token is past its lifetime.
func (t LinkedInToken) Expired() bool {
	return t.ExpiresAt > 0 && time.Now().Unix() >= t.ExpiresAt
}

// ExpiringSoon reports whether the access token dies within the given duration.
func (t LinkedInToken) ExpiringSoon(within time.Duration) bool {
	return t.ExpiresAt > 0 && time.Now().Add(within).Unix() >= t.ExpiresAt
}

// SaveLinkedInToken upserts the single stored LinkedIn credential.
func (s *Store) SaveLinkedInToken(ctx context.Context, tok LinkedInToken) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO linkedin_token
		(id, access_token, refresh_token, expires_at, scope, person_id, person_name, updated_at)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			access_token = excluded.access_token,
			refresh_token = excluded.refresh_token,
			expires_at = excluded.expires_at,
			scope = excluded.scope,
			person_id = excluded.person_id,
			person_name = excluded.person_name,
			updated_at = excluded.updated_at`,
		tok.AccessToken, tok.RefreshToken, tok.ExpiresAt, tok.Scope,
		tok.PersonID, tok.PersonName, now())
	if err != nil {
		return fmt.Errorf("save linkedin token: %w", err)
	}
	return nil
}

// GetLinkedInToken returns the stored credential, or sql.ErrNoRows.
func (s *Store) GetLinkedInToken(ctx context.Context) (LinkedInToken, error) {
	var tok LinkedInToken
	err := s.db.QueryRowContext(ctx, `SELECT access_token, refresh_token, expires_at,
		scope, person_id, person_name, updated_at FROM linkedin_token WHERE id = 1`).Scan(
		&tok.AccessToken, &tok.RefreshToken, &tok.ExpiresAt,
		&tok.Scope, &tok.PersonID, &tok.PersonName, &tok.UpdatedAt)
	if err != nil {
		return LinkedInToken{}, fmt.Errorf("get linkedin token: %w", err)
	}
	return tok, nil
}

// ClearLinkedInToken removes the stored credential.
func (s *Store) ClearLinkedInToken(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM linkedin_token WHERE id = 1`); err != nil {
		return fmt.Errorf("clear linkedin token: %w", err)
	}
	return nil
}

// OAuthState tracks an in-flight LinkedIn authorization for one Discord user.
type OAuthState struct {
	State         string
	DiscordUserID string
	Verifier      string
	CreatedAt     int64
}

// SaveOAuthState records a fresh authorization request.
func (s *Store) SaveOAuthState(ctx context.Context, st OAuthState) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO oauth_states(state, discord_user_id, verifier, created_at)
		VALUES (?, ?, ?, ?)`, st.State, st.DiscordUserID, st.Verifier, now())
	if err != nil {
		return fmt.Errorf("save oauth state: %w", err)
	}
	return nil
}

// ConsumeOAuthState returns and deletes a pending state, or sql.ErrNoRows.
func (s *Store) ConsumeOAuthState(ctx context.Context, state string) (OAuthState, error) {
	var st OAuthState
	err := s.db.QueryRowContext(ctx, `SELECT state, discord_user_id, verifier, created_at
		FROM oauth_states WHERE state = ?`, state).Scan(
		&st.State, &st.DiscordUserID, &st.Verifier, &st.CreatedAt)
	if err != nil {
		return OAuthState{}, fmt.Errorf("consume oauth state: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM oauth_states WHERE state = ?`, state); err != nil {
		return OAuthState{}, fmt.Errorf("delete oauth state: %w", err)
	}
	return st, nil
}

// PruneOAuthStates drops states older than the given age.
func (s *Store) PruneOAuthStates(ctx context.Context, olderThan time.Duration) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM oauth_states WHERE created_at < ?`,
		time.Now().Add(-olderThan).Unix())
	if err != nil {
		return fmt.Errorf("prune oauth states: %w", err)
	}
	return nil
}

// Source is an RSS/Atom feed or a single page Pulse drafts from.
type Source struct {
	ID            int64
	Kind          string
	URL           string
	Title         string
	LastFetchedAt int64
}

// AddSource inserts a source, or returns the existing row on URL conflict.
func (s *Store) AddSource(ctx context.Context, kind, url, title string) (Source, error) {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sources(kind, url, title) VALUES (?, ?, ?)
		ON CONFLICT(url) DO UPDATE SET kind = excluded.kind, title = excluded.title`,
		kind, url, title)
	if err != nil {
		return Source{}, fmt.Errorf("add source %s: %w", url, err)
	}
	var src Source
	err = s.db.QueryRowContext(ctx, `SELECT id, kind, url, title, last_fetched_at
		FROM sources WHERE url = ?`, url).Scan(
		&src.ID, &src.Kind, &src.URL, &src.Title, &src.LastFetchedAt)
	if err != nil {
		return Source{}, fmt.Errorf("read back source %s: %w", url, err)
	}
	return src, nil
}

// ListSources returns all sources ordered by id.
func (s *Store) ListSources(ctx context.Context) ([]Source, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, url, title, last_fetched_at
		FROM sources ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list sources: %w", err)
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		var src Source
		if err := rows.Scan(&src.ID, &src.Kind, &src.URL, &src.Title, &src.LastFetchedAt); err != nil {
			return nil, fmt.Errorf("scan source: %w", err)
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// RemoveSource deletes a source and its articles.
func (s *Store) RemoveSource(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sources WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("remove source %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("remove source %d rows: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("remove source %d: %w", id, sql.ErrNoRows)
	}
	return nil
}

// TouchSourceFetched records a successful fetch of a source.
func (s *Store) TouchSourceFetched(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sources SET last_fetched_at = ? WHERE id = ?`, now(), id)
	if err != nil {
		return fmt.Errorf("touch source %d: %w", id, err)
	}
	return nil
}

// Article is one fetched item that can seed a draft.
type Article struct {
	ID          int64
	SourceID    int64
	URL         string
	Title       string
	Summary     string
	Body        string
	PublishedAt int64
	Used        bool
	CreatedAt   int64
}

// AddArticle inserts an article, reporting whether it was new.
// Duplicates by URL are silently skipped.
func (s *Store) AddArticle(ctx context.Context, a Article) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO articles
		(source_id, url, title, summary, body, published_at, used, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, ?)
		ON CONFLICT(url) DO NOTHING`,
		a.SourceID, a.URL, a.Title, a.Summary, a.Body, a.PublishedAt, now())
	if err != nil {
		return false, fmt.Errorf("add article %s: %w", a.URL, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("add article %s rows: %w", a.URL, err)
	}
	return n > 0, nil
}

// CountUnusedArticles returns how many articles await a draft.
func (s *Store) CountUnusedArticles(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM articles WHERE used = 0`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count unused articles: %w", err)
	}
	return n, nil
}

// NextUnusedArticle returns the oldest article not yet turned into a draft.
func (s *Store) NextUnusedArticle(ctx context.Context) (Article, error) {
	var a Article
	var used int
	err := s.db.QueryRowContext(ctx, `SELECT id, source_id, url, title, summary, body,
		published_at, used, created_at FROM articles WHERE used = 0
		ORDER BY published_at ASC, id ASC LIMIT 1`).Scan(
		&a.ID, &a.SourceID, &a.URL, &a.Title, &a.Summary, &a.Body,
		&a.PublishedAt, &used, &a.CreatedAt)
	if err != nil {
		return Article{}, fmt.Errorf("next unused article: %w", err)
	}
	a.Used = used != 0
	return a, nil
}

// MarkArticleUsed flags an article as consumed by a draft.
func (s *Store) MarkArticleUsed(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE articles SET used = 1 WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("mark article %d used: %w", id, err)
	}
	return nil
}

// GetArticle returns one article by id.
func (s *Store) GetArticle(ctx context.Context, id int64) (Article, error) {
	var a Article
	var used int
	err := s.db.QueryRowContext(ctx, `SELECT id, source_id, url, title, summary, body,
		published_at, used, created_at FROM articles WHERE id = ?`, id).Scan(
		&a.ID, &a.SourceID, &a.URL, &a.Title, &a.Summary, &a.Body,
		&a.PublishedAt, &used, &a.CreatedAt)
	if err != nil {
		return Article{}, fmt.Errorf("get article %d: %w", id, err)
	}
	a.Used = used != 0
	return a, nil
}

// Draft is a generated post moving through review toward LinkedIn.
type Draft struct {
	ID               int64
	ArticleID        int64
	Text             string
	Status           string
	LinkedInURN      string
	Error            string
	DiscordChannelID string
	DiscordMessageID string
	CreatedAt        int64
	UpdatedAt        int64
}

// CreateDraft stores a fresh pending draft.
func (s *Store) CreateDraft(ctx context.Context, articleID int64, text string) (Draft, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO drafts(article_id, text, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)`, articleID, text, DraftPending, now(), now())
	if err != nil {
		return Draft{}, fmt.Errorf("create draft: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Draft{}, fmt.Errorf("create draft id: %w", err)
	}
	return s.GetDraft(ctx, id)
}

// GetDraft returns one draft by id.
func (s *Store) GetDraft(ctx context.Context, id int64) (Draft, error) {
	var d Draft
	err := s.db.QueryRowContext(ctx, `SELECT id, article_id, text, status, linkedin_urn,
		error, discord_channel_id, discord_message_id, created_at, updated_at
		FROM drafts WHERE id = ?`, id).Scan(
		&d.ID, &d.ArticleID, &d.Text, &d.Status, &d.LinkedInURN,
		&d.Error, &d.DiscordChannelID, &d.DiscordMessageID, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return Draft{}, fmt.Errorf("get draft %d: %w", id, err)
	}
	return d, nil
}

// UpdateDraftText replaces a draft's text and bumps its timestamp.
func (s *Store) UpdateDraftText(ctx context.Context, id int64, text string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE drafts SET text = ?, updated_at = ? WHERE id = ?`,
		text, now(), id)
	if err != nil {
		return fmt.Errorf("update draft %d text: %w", id, err)
	}
	return nil
}

// UpdateDraftArticle re-points a draft at a new article with fresh text,
// used by Regenerate to draw from another source item.
func (s *Store) UpdateDraftArticle(ctx context.Context, id, articleID int64, text string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE drafts SET article_id = ?, text = ?,
		status = ?, error = ?, updated_at = ? WHERE id = ?`,
		articleID, text, DraftPending, "", now(), id)
	if err != nil {
		return fmt.Errorf("update draft %d article: %w", id, err)
	}
	return nil
}

// UpdateDraftStatus moves a draft, optionally recording the LinkedIn URN or error.
func (s *Store) UpdateDraftStatus(ctx context.Context, id int64, status, urn, postErr string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE drafts SET status = ?, linkedin_urn = ?,
		error = ?, updated_at = ? WHERE id = ?`, status, urn, postErr, now(), id)
	if err != nil {
		return fmt.Errorf("update draft %d status: %w", id, err)
	}
	return nil
}

// SetDraftMessage records where a draft was announced on Discord.
func (s *Store) SetDraftMessage(ctx context.Context, id int64, channelID, messageID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE drafts SET discord_channel_id = ?,
		discord_message_id = ?, updated_at = ? WHERE id = ?`, channelID, messageID, now(), id)
	if err != nil {
		return fmt.Errorf("set draft %d message: %w", id, err)
	}
	return nil
}

// ListDrafts returns recent drafts, newest first, optionally filtered by status.
func (s *Store) ListDrafts(ctx context.Context, status string, limit int) ([]Draft, error) {
	query := `SELECT id, article_id, text, status, linkedin_urn, error,
		discord_channel_id, discord_message_id, created_at, updated_at
		FROM drafts`
	var args []any
	if status != "" {
		query += ` WHERE status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list drafts: %w", err)
	}
	defer rows.Close()
	var out []Draft
	for rows.Next() {
		var d Draft
		if err := rows.Scan(&d.ID, &d.ArticleID, &d.Text, &d.Status, &d.LinkedInURN,
			&d.Error, &d.DiscordChannelID, &d.DiscordMessageID, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan draft: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Schedule is one weekly reminder slot.
type Schedule struct {
	ID           int64
	Days         string // CSV of time.Weekday numbers, e.g. "1,3,5"
	Hour         int
	Minute       int
	Autopost     bool
	Timezone     string
	Enabled      bool
	LastFiredSlot string
}

// ReplaceSchedule clears all schedules and stores one new slot.
func (s *Store) ReplaceSchedule(ctx context.Context, days string, hour, minute int, autopost bool, timezone string) (Schedule, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Schedule{}, fmt.Errorf("replace schedule tx: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM schedules`); err != nil {
		return Schedule{}, fmt.Errorf("replace schedule clear: %w", err)
	}
	var autopostInt int
	if autopost {
		autopostInt = 1
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO schedules(days, hour, minute, autopost, timezone, enabled)
		VALUES (?, ?, ?, ?, ?, 1)`, days, hour, minute, autopostInt, timezone)
	if err != nil {
		return Schedule{}, fmt.Errorf("replace schedule insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Schedule{}, fmt.Errorf("replace schedule id: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Schedule{}, fmt.Errorf("replace schedule commit: %w", err)
	}
	return s.GetSchedule(ctx, id)
}

// GetSchedule returns one schedule by id.
func (s *Store) GetSchedule(ctx context.Context, id int64) (Schedule, error) {
	var sc Schedule
	var autopost, enabled int
	err := s.db.QueryRowContext(ctx, `SELECT id, days, hour, minute, autopost, timezone,
		enabled, last_fired_slot FROM schedules WHERE id = ?`, id).Scan(
		&sc.ID, &sc.Days, &sc.Hour, &sc.Minute, &autopost, &sc.Timezone,
		&enabled, &sc.LastFiredSlot)
	if err != nil {
		return Schedule{}, fmt.Errorf("get schedule %d: %w", id, err)
	}
	sc.Autopost = autopost != 0
	sc.Enabled = enabled != 0
	return sc, nil
}

// ListSchedules returns all schedules, optionally only enabled ones.
func (s *Store) ListSchedules(ctx context.Context, enabledOnly bool) ([]Schedule, error) {
	query := `SELECT id, days, hour, minute, autopost, timezone, enabled, last_fired_slot
		FROM schedules`
	if enabledOnly {
		query += ` WHERE enabled = 1`
	}
	query += ` ORDER BY id`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list schedules: %w", err)
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		var sc Schedule
		var autopost, enabled int
		if err := rows.Scan(&sc.ID, &sc.Days, &sc.Hour, &sc.Minute, &autopost,
			&sc.Timezone, &enabled, &sc.LastFiredSlot); err != nil {
			return nil, fmt.Errorf("scan schedule: %w", err)
		}
		sc.Autopost = autopost != 0
		sc.Enabled = enabled != 0
		out = append(out, sc)
	}
	return out, rows.Err()
}

// SetScheduleAutopost toggles auto-publishing for a schedule.
func (s *Store) SetScheduleAutopost(ctx context.Context, id int64, autopost bool) error {
	v := 0
	if autopost {
		v = 1
	}
	_, err := s.db.ExecContext(ctx, `UPDATE schedules SET autopost = ? WHERE id = ?`, v, id)
	if err != nil {
		return fmt.Errorf("set schedule %d autopost: %w", id, err)
	}
	return nil
}

// MarkScheduleFired records the slot a schedule just served.
func (s *Store) MarkScheduleFired(ctx context.Context, id int64, slot string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE schedules SET last_fired_slot = ? WHERE id = ?`, slot, id)
	if err != nil {
		return fmt.Errorf("mark schedule %d fired: %w", id, err)
	}
	return nil
}

// DisableSchedules turns every schedule off.
func (s *Store) DisableSchedules(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE schedules SET enabled = 0`)
	if err != nil {
		return fmt.Errorf("disable schedules: %w", err)
	}
	return nil
}

// Snooze holds a draft for re-announcement at a later time.
type Snooze struct {
	ID      int64
	DraftID int64
	FireAt  int64
	Fired   bool
}

// AddSnooze queues a draft to be re-announced at fireAt.
func (s *Store) AddSnooze(ctx context.Context, draftID int64, fireAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO snoozes(draft_id, fire_at, fired)
		VALUES (?, ?, 0)`, draftID, fireAt.Unix())
	if err != nil {
		return fmt.Errorf("add snooze for draft %d: %w", draftID, err)
	}
	return nil
}

// DueSnoozes returns snoozes whose time has come and are not yet fired.
func (s *Store) DueSnoozes(ctx context.Context, at time.Time) ([]Snooze, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, draft_id, fire_at, fired FROM snoozes
		WHERE fired = 0 AND fire_at <= ? ORDER BY fire_at`, at.Unix())
	if err != nil {
		return nil, fmt.Errorf("due snoozes: %w", err)
	}
	defer rows.Close()
	var out []Snooze
	for rows.Next() {
		var sn Snooze
		var fired int
		if err := rows.Scan(&sn.ID, &sn.DraftID, &sn.FireAt, &fired); err != nil {
			return nil, fmt.Errorf("scan snooze: %w", err)
		}
		sn.Fired = fired != 0
		out = append(out, sn)
	}
	return out, rows.Err()
}

// MarkSnoozeFired flags a snooze as delivered.
func (s *Store) MarkSnoozeFired(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE snoozes SET fired = 1 WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("mark snooze %d fired: %w", id, err)
	}
	return nil
}

// ClearSnoozesForDraft drops pending snoozes for a draft that moved on.
func (s *Store) ClearSnoozesForDraft(ctx context.Context, draftID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM snoozes WHERE draft_id = ? AND fired = 0`, draftID)
	if err != nil {
		return fmt.Errorf("clear snoozes for draft %d: %w", draftID, err)
	}
	return nil
}
