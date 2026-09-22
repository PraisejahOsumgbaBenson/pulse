package telegram

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testServer(t *testing.T, handler http.HandlerFunc) (*Client, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	c := NewClient("test-token")
	c.base = srv.URL + "/bot" + "test-token"
	return c, srv.Close
}

func envelope(t *testing.T, result any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"ok": true, "result": result})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSendMessageShape(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	c, done := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write(envelope(t, map[string]any{"message_id": 11, "chat": map[string]any{"id": 5}, "text": "hi"}))
	})
	defer done()

	msg, err := c.SendMessage(t.Context(), 5, "hi", CardKeyboard(7))
	if err != nil {
		t.Fatalf("SendMessage() returned error: %v", err)
	}
	if msg.MessageID != 11 {
		t.Errorf("MessageID = %d, want 11", msg.MessageID)
	}
	if gotPath != "/bottest-token/sendMessage" {
		t.Errorf("path = %q, want sendMessage method", gotPath)
	}
	if gotBody["chat_id"] != float64(5) || gotBody["text"] != "hi" {
		t.Errorf("body = %v, want chat_id 5 and text", gotBody)
	}
	if _, ok := gotBody["reply_markup"]; !ok {
		t.Error("body missing reply_markup keyboard")
	}
}

func TestSendChatActionShape(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	c, done := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write(envelope(t, true))
	})
	defer done()

	if err := c.SendChatAction(t.Context(), 5, "typing"); err != nil {
		t.Fatalf("SendChatAction() returned error: %v", err)
	}
	if gotPath != "/bottest-token/sendChatAction" {
		t.Errorf("path = %q, want sendChatAction method", gotPath)
	}
	if gotBody["chat_id"] != float64(5) || gotBody["action"] != "typing" {
		t.Errorf("body = %v, want chat_id 5 and typing action", gotBody)
	}
}

func TestGetUpdatesParsesCallback(t *testing.T) {
	c, done := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(envelope(t, []map[string]any{{
			"update_id": 99,
			"callback_query": map[string]any{
				"id":   "cb1",
				"from": map[string]any{"id": 5},
				"message": map[string]any{
					"message_id": 11,
					"chat":       map[string]any{"id": 5},
					"text":       "card",
				},
				"data": "pulse:approve:7",
			},
		}}))
	})
	defer done()

	updates, err := c.GetUpdates(t.Context(), 0, 0)
	if err != nil {
		t.Fatalf("GetUpdates() returned error: %v", err)
	}
	if len(updates) != 1 || updates[0].CallbackQuery == nil {
		t.Fatalf("updates = %+v, want 1 callback query", updates)
	}
	if updates[0].CallbackQuery.Data != "pulse:approve:7" {
		t.Errorf("data = %q, want pulse:approve:7", updates[0].CallbackQuery.Data)
	}
}

func TestAPIError(t *testing.T) {
	c, done := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":401,"description":"Unauthorized: bot token invalid"}`))
	})
	defer done()

	_, err := c.GetMe(t.Context())
	if err == nil {
		t.Fatal("GetMe() = nil error, want 401")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("err type = %T, want *APIError", err)
	}
	if apiErr.Code != 401 {
		t.Errorf("code = %d, want 401", apiErr.Code)
	}
}
