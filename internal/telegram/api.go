package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// maxMessageRunes stays under Telegram's 4096-character message cap.
const maxMessageRunes = 3900

// User is a Telegram user or bot.
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

// Chat is where a message lives. Pulse only talks in private chats.
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

// Message is a Telegram message.
type Message struct {
	MessageID int    `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Date      int64  `json:"date"`
	Text      string `json:"text"`
}

// CallbackQuery is a tap on an inline keyboard button.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

// Update is one event from long polling.
type Update struct {
	UpdateID      int            `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

// InlineKeyboardButton is one button under a message.
type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

// InlineKeyboardMarkup is rows of inline buttons.
type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

// BotCommand describes one command for the Telegram command menu.
type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// CardKeyboard returns the review buttons for a draft card.
func CardKeyboard(draftID int64) *InlineKeyboardMarkup {
	btn := func(text, action string) InlineKeyboardButton {
		return InlineKeyboardButton{Text: text, CallbackData: CallbackData(action, draftID)}
	}
	return &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
		{btn("Approve", ActionApprove), btn("Edit", ActionEdit)},
		{btn("Regenerate", ActionRegen), btn("Skip", ActionSkip)},
		{btn("Snooze 1h", ActionSnooze)},
	}}
}

// APIError is a Bot API failure with its code and description.
type APIError struct {
	Code        int
	Description string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram api %d: %s", e.Code, e.Description)
}

// API is the Bot API surface Pulse uses. *Client implements it; tests fake it.
type API interface {
	GetMe(ctx context.Context) (User, error)
	GetUpdates(ctx context.Context, offset, timeoutSecs int) ([]Update, error)
	SendMessage(ctx context.Context, chatID int64, text string, markup *InlineKeyboardMarkup) (Message, error)
	EditMessageText(ctx context.Context, chatID int64, messageID int, text string, markup *InlineKeyboardMarkup) error
	AnswerCallbackQuery(ctx context.Context, id, text string) error
	SetMyCommands(ctx context.Context, cmds []BotCommand) error
	SendChatAction(ctx context.Context, chatID int64, action string) error
}

// Client speaks the Telegram Bot API over HTTPS with plain JSON.
type Client struct {
	base string
	http *http.Client
}

// NewClient builds a Client for one bot token.
func NewClient(token string) *Client {
	return &Client{
		base: "https://api.telegram.org/bot" + token,
		http: &http.Client{Timeout: 70 * time.Second},
	}
}

type apiEnvelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
}

func (c *Client) call(ctx context.Context, method string, payload any, result any) error {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode %s: %w", method, err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/"+method, body)
	if err != nil {
		return fmt.Errorf("build %s request: %w", method, err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call %s: %w", method, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("read %s response: %w", method, err)
	}
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("decode %s response: %w", method, err)
	}
	if !env.OK {
		code := env.ErrorCode
		if code == 0 {
			code = resp.StatusCode
		}
		return &APIError{Code: code, Description: env.Description}
	}
	if result != nil {
		if err := json.Unmarshal(env.Result, result); err != nil {
			return fmt.Errorf("decode %s result: %w", method, err)
		}
	}
	return nil
}

// clip keeps message text under Telegram's length cap.
func clip(s string) string {
	r := []rune(s)
	if len(r) <= maxMessageRunes {
		return s
	}
	return string(r[:maxMessageRunes-1]) + "…"
}

// GetMe verifies the token and returns the bot identity.
func (c *Client) GetMe(ctx context.Context) (User, error) {
	var u User
	if err := c.call(ctx, "getMe", nil, &u); err != nil {
		return User{}, err
	}
	return u, nil
}

// updatesRequest asks for events after offset, waiting up to timeoutSecs.
func (c *Client) GetUpdates(ctx context.Context, offset, timeoutSecs int) ([]Update, error) {
	var updates []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         timeoutSecs,
		"allowed_updates": []string{"message", "callback_query"},
	}, &updates)
	if err != nil {
		return nil, err
	}
	return updates, nil
}

// SendMessage DMs text with an optional inline keyboard.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string, markup *InlineKeyboardMarkup) (Message, error) {
	payload := map[string]any{"chat_id": chatID, "text": clip(text)}
	if markup != nil {
		payload["reply_markup"] = markup
	}
	var msg Message
	if err := c.call(ctx, "sendMessage", payload, &msg); err != nil {
		return Message{}, err
	}
	return msg, nil
}

// EditMessageText rewrites a sent message and its keyboard in place.
func (c *Client) EditMessageText(ctx context.Context, chatID int64, messageID int, text string, markup *InlineKeyboardMarkup) error {
	payload := map[string]any{"chat_id": chatID, "message_id": messageID, "text": clip(text)}
	if markup != nil {
		payload["reply_markup"] = markup
	}
	return c.call(ctx, "editMessageText", payload, nil)
}

// AnswerCallbackQuery dismisses the button spinner, optionally with a toast.
func (c *Client) AnswerCallbackQuery(ctx context.Context, id, text string) error {
	return c.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": text}, nil)
}

// SetMyCommands publishes the command menu Telegram shows on the phone.
func (c *Client) SetMyCommands(ctx context.Context, cmds []BotCommand) error {
	return c.call(ctx, "setMyCommands", map[string]any{"commands": cmds}, nil)
}

// SendChatAction shows activity like "typing..." in the chat. It expires
// after a few seconds, so resend it during long work.
func (c *Client) SendChatAction(ctx context.Context, chatID int64, action string) error {
	return c.call(ctx, "sendChatAction", map[string]any{"chat_id": chatID, "action": action}, nil)
}
