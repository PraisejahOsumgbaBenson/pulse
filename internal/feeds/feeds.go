// Package feeds turns RSS/Atom feeds and single links into articles Pulse
// can draft LinkedIn posts from.
package feeds

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/PraisejahOsumgbaBenson/pulse/internal/store"
	"github.com/mmcdole/gofeed"
)

const (
	// maxItemsPerSource caps how many entries one refresh stores per feed.
	maxItemsPerSource = 20
	// maxSummaryChars and maxBodyChars keep stored text compact for the LLM.
	maxSummaryChars = 2000
	maxBodyChars    = 4000
	// maxPageBytes bounds single-page downloads.
	maxPageBytes = 1 << 20
)

var (
	titleRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	descRe  = regexp.MustCompile(`(?is)<meta[^>]+name=["']description["'][^>]+content=["'](.*?)["']`)
	descRe2 = regexp.MustCompile(`(?is)<meta[^>]+content=["'](.*?)["'][^>]+name=["']description["']`)
	tagRe   = regexp.MustCompile(`(?s)<[^>]+>`)
	spaceRe = regexp.MustCompile(`\s+`)
)

// Service fetches sources and stores new articles.
type Service struct {
	store  *store.Store
	http   *http.Client
	parser *gofeed.Parser
	logger *slog.Logger
}

// New builds a Service backed by the given store.
func New(st *store.Store, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:  st,
		http:   &http.Client{Timeout: 20 * time.Second},
		parser: gofeed.NewParser(),
		logger: logger,
	}
}

// Add registers a URL as a source. If it parses as a feed the entries are
// ingested, otherwise the page itself becomes one article. It returns the
// source and how many new articles were stored.
func (s *Service) Add(ctx context.Context, rawURL string) (store.Source, int, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return store.Source{}, 0, fmt.Errorf("empty source url")
	}
	if feed, err := s.parser.ParseURLWithContext(rawURL, ctx); err == nil && len(feed.Items) > 0 {
		title := strings.TrimSpace(feed.Title)
		if title == "" {
			title = rawURL
		}
		src, err := s.store.AddSource(ctx, store.SourceRSS, rawURL, title)
		if err != nil {
			return store.Source{}, 0, err
		}
		n, err := s.ingestItems(ctx, src.ID, feed.Items)
		if err != nil {
			return store.Source{}, 0, err
		}
		if err := s.store.TouchSourceFetched(ctx, src.ID); err != nil {
			return store.Source{}, 0, err
		}
		s.logger.Info("feed source added", "url", rawURL, "new", n)
		return src, n, nil
	}
	return s.addPage(ctx, rawURL)
}

func (s *Service) addPage(ctx context.Context, rawURL string) (store.Source, int, error) {
	title, summary, err := fetchPageMeta(ctx, s.http, rawURL)
	if err != nil {
		return store.Source{}, 0, fmt.Errorf("fetch page %s: %w", rawURL, err)
	}
	src, err := s.store.AddSource(ctx, store.SourceLink, rawURL, title)
	if err != nil {
		return store.Source{}, 0, err
	}
	inserted, err := s.store.AddArticle(ctx, store.Article{
		SourceID: src.ID,
		URL:      rawURL,
		Title:    title,
		Summary:  summary,
	})
	if err != nil {
		return store.Source{}, 0, err
	}
	if err := s.store.TouchSourceFetched(ctx, src.ID); err != nil {
		return store.Source{}, 0, err
	}
	n := 0
	if inserted {
		n = 1
	}
	s.logger.Info("link source added", "url", rawURL)
	return src, n, nil
}

// Refresh re-fetches every RSS source and returns new articles stored.
func (s *Service) Refresh(ctx context.Context) (int, error) {
	sources, err := s.store.ListSources(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, src := range sources {
		if src.Kind != store.SourceRSS {
			continue
		}
		feed, err := s.parser.ParseURLWithContext(src.URL, ctx)
		if err != nil {
			s.logger.Warn("refresh feed failed", "url", src.URL, "err", err)
			continue
		}
		n, err := s.ingestItems(ctx, src.ID, feed.Items)
		if err != nil {
			s.logger.Warn("ingest feed failed", "url", src.URL, "err", err)
			continue
		}
		if err := s.store.TouchSourceFetched(ctx, src.ID); err != nil {
			s.logger.Warn("touch source failed", "url", src.URL, "err", err)
		}
		total += n
	}
	return total, nil
}

func (s *Service) ingestItems(ctx context.Context, sourceID int64, items []*gofeed.Item) (int, error) {
	stored := 0
	for i, item := range items {
		if i >= maxItemsPerSource {
			break
		}
		link := strings.TrimSpace(item.Link)
		if link == "" {
			link = strings.TrimSpace(item.GUID)
		}
		if link == "" {
			continue
		}
		published := time.Now()
		if item.PublishedParsed != nil {
			published = *item.PublishedParsed
		} else if item.UpdatedParsed != nil {
			published = *item.UpdatedParsed
		}
		summary := strings.TrimSpace(item.Description)
		if summary == "" {
			summary = strings.TrimSpace(item.Content)
		}
		inserted, err := s.store.AddArticle(ctx, store.Article{
			SourceID:    sourceID,
			URL:         link,
			Title:       truncateRunes(strings.TrimSpace(item.Title), 500),
			Summary:     truncateRunes(cleanText(summary), maxSummaryChars),
			Body:        truncateRunes(cleanText(item.Content), maxBodyChars),
			PublishedAt: published.Unix(),
		})
		if err != nil {
			return stored, err
		}
		if inserted {
			stored++
		}
	}
	return stored, nil
}

// fetchPageMeta downloads a page and extracts its title and description.
func fetchPageMeta(ctx context.Context, client *http.Client, rawURL string) (title, summary string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "PulseBot/1.0 (+https://github.com/PraisejahOsumgbaBenson/pulse)")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
	if err != nil {
		return "", "", err
	}
	html := string(body)
	title = cleanText(firstMatch(titleRe, html))
	if title == "" {
		title = rawURL
	}
	summary = cleanText(firstMatch(descRe, html))
	if summary == "" {
		summary = cleanText(firstMatch(descRe2, html))
	}
	return title, summary, nil
}

func firstMatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func cleanText(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	// Decode a few common entities without pulling in a full HTML parser.
	s = strings.NewReplacer(
		"&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'",
		"&nbsp;", " ", "\n", " ", "\r", " ", "\t", " ",
	).Replace(s)
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
