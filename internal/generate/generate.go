// Package generate turns source articles into LinkedIn-ready draft text.
// The Generator interface keeps the LLM provider swappable; New picks the
// OpenAI-compatible client when an API key is configured and an offline
// fallback otherwise.
package generate

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
	"unicode"
)

const (
	// maxDraftRunes stays comfortably under LinkedIn's 3000-char limit.
	maxDraftRunes = 2900
)

// Input is the raw material for one draft.
type Input struct {
	Title   string
	Summary string
	URL     string
	Style   string
}

// Generator produces draft post text from an Input.
type Generator interface {
	Name() string
	Generate(ctx context.Context, in Input) (string, error)
}

// New selects the OpenAI-compatible client when apiKey is set, else the
// offline fallback that needs no network or key.
func New(baseURL, model, apiKey, style string, logger *slog.Logger) Generator {
	if logger == nil {
		logger = slog.Default()
	}
	if strings.TrimSpace(apiKey) == "" {
		return &Fallback{style: style, logger: logger}
	}
	return &OpenAICompat{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		apiKey:  apiKey,
		style:   style,
		http:    &http.Client{Timeout: 60 * time.Second},
		logger:  logger,
	}
}

// OpenAICompat calls any OpenAI-compatible chat completions endpoint.
type OpenAICompat struct {
	baseURL string
	model   string
	apiKey  string
	style   string
	http    *http.Client
	logger  *slog.Logger
}

// Name identifies the generator.
func (g *OpenAICompat) Name() string { return "openai-compatible:" + g.model }

func systemPrompt(style string) string {
	voice := "direct, concrete, no hype, no emojis"
	if strings.TrimSpace(style) != "" {
		voice = strings.TrimSpace(style)
	}
	return "You write short LinkedIn posts for a builder sharing what they read and build. " +
		"Voice: " + voice + ". Rules: open with a one-line hook; 60 to 160 words; " +
		"short paragraphs with blank lines between them; plain text only, no markdown; " +
		"never invent facts that are not in the source; end with 2 to 4 relevant hashtags."
}

// Generate asks the model for one post.
func (g *OpenAICompat) Generate(ctx context.Context, in Input) (string, error) {
	user := fmt.Sprintf("Write a LinkedIn post about this. Keep it under 180 words.\n\nTitle: %s\nSummary: %s\nLink: %s",
		strings.TrimSpace(in.Title), strings.TrimSpace(in.Summary), strings.TrimSpace(in.URL))
	body, err := json.Marshal(map[string]any{
		"model":       g.model,
		"temperature": 0.7,
		"max_tokens":  600,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt(g.style)},
			{"role": "user", "content": user},
		},
	})
	if err != nil {
		return "", fmt.Errorf("encode chat request: %w", err)
	}
	// Overloaded free-tier models answer 429 or 5xx under spikes, so retry
	// those a few times with backoff instead of failing the draft.
	const maxAttempts = 3
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return "", fmt.Errorf("build chat request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+g.apiKey)
		resp, err := g.http.Do(req)
		if err != nil {
			return "", fmt.Errorf("call chat completions: %w", err)
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil {
			return "", fmt.Errorf("read chat response: %w", err)
		}
		if resp.StatusCode == http.StatusOK {
			text, err := decodeChoices(raw)
			if err != nil {
				return "", err
			}
			g.logger.Info("draft generated", "generator", g.Name(), "chars", len(text))
			return capRunes(text, maxDraftRunes), nil
		}
		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		if !retryable || attempt >= maxAttempts {
			return "", fmt.Errorf("chat completions status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
		}
		g.logger.Warn("chat completions overloaded, retrying", "status", resp.StatusCode, "attempt", attempt)
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("chat completions status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
		case <-time.After(time.Duration(1<<uint(attempt-1)) * time.Second):
		}
	}
}

func decodeChoices(raw []byte) (string, error) {
	var doc struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("decode chat response: %w", err)
	}
	if len(doc.Choices) == 0 {
		return "", fmt.Errorf("chat response held no choices")
	}
	text := strings.TrimSpace(doc.Choices[0].Message.Content)
	if text == "" {
		return "", fmt.Errorf("model returned empty draft")
	}
	return text, nil
}

// Fallback builds a readable post with no network and no key.
type Fallback struct {
	style  string
	logger *slog.Logger
}

// Name identifies the generator.
func (g *Fallback) Name() string { return "fallback" }

// Generate assembles title, summary, link and derived hashtags.
func (g *Fallback) Generate(_ context.Context, in Input) (string, error) {
	title := strings.TrimSpace(in.Title)
	summary := strings.TrimSpace(in.Summary)
	url := strings.TrimSpace(in.URL)
	if title == "" && summary == "" {
		return "", fmt.Errorf("fallback needs a title or summary")
	}
	if url == "" {
		return g.thoughtPost(title, summary), nil
	}
	var b strings.Builder
	if title != "" && !strings.HasPrefix(summary, title) {
		b.WriteString(title)
	}
	body := firstSentences(summary, 2)
	if body != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(body)
	}
	if url != "" {
		b.WriteString("\n\nRead more: " + url)
	}
	if tags := deriveTags(title + " " + summary); len(tags) > 0 {
		b.WriteString("\n\n" + strings.Join(tags, " "))
	}
	text := capRunes(strings.TrimSpace(b.String()), maxDraftRunes)
	g.logger.Info("draft generated", "generator", g.Name(), "chars", len(text))
	return text, nil
}

// thoughtClosers ends plain-writer posts with a light question. The pick is
// deterministic per text so the same thought always drafts the same way.
var thoughtClosers = []string{
	"What's your take?",
	"Curious how others see this.",
	"Would love to hear your experience.",
}

// thoughtPost shapes a /topic thought into a short post without inventing
// anything beyond the thought itself: the thought, a closer, hashtags.
func (g *Fallback) thoughtPost(title, summary string) string {
	body := capRunes(strings.TrimSpace(summary), 1200)
	var b strings.Builder
	b.WriteString(body)
	b.WriteString("\n\n" + thoughtClosers[len([]rune(body))%len(thoughtClosers)])
	if tags := deriveTags(title + " " + summary); len(tags) > 0 {
		b.WriteString("\n\n" + strings.Join(tags, " "))
	}
	text := capRunes(strings.TrimSpace(b.String()), maxDraftRunes)
	g.logger.Info("draft generated", "generator", g.Name(), "chars", len(text))
	return text
}

func capRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// firstSentences keeps at most n sentences of plain text.
func firstSentences(s string, n int) string {
	s = strings.TrimSpace(s)
	if s == "" || n <= 0 {
		return ""
	}
	var out []string
	start := 0
	for i, r := range s {
		if r == '.' || r == '!' || r == '?' {
			sent := strings.TrimSpace(s[start : i+1])
			if sent != "" {
				out = append(out, sent)
				if len(out) == n {
					break
				}
			}
			start = i + 1
		}
	}
	if len(out) == 0 {
		return capRunes(s, 500)
	}
	joined := strings.Join(out, " ")
	return capRunes(joined, 500)
}

var stopwords = map[string]bool{
	"how": true, "what": true, "why": true, "when": true, "with": true,
	"from": true, "that": true, "this": true, "your": true, "about": true,
	"into": true, "over": true, "under": true, "more": true, "than": true,
	"will": true, "they": true, "their": true, "them": true, "have": true,
	"guide": true,
}

// deriveTags picks up to 3 hashtag candidates from long, distinctive words.
func deriveTags(s string) []string {
	seen := map[string]bool{}
	var tags []string
	for _, word := range strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		w := strings.ToLower(word)
		if len([]rune(w)) < 5 || stopwords[w] || seen[w] {
			continue
		}
		seen[w] = true
		tags = append(tags, "#"+w)
		if len(tags) == 3 {
			break
		}
	}
	if len(tags) == 0 {
		return []string{"#building"}
	}
	return tags
}
