package telegram

import (
	"context"
	"encoding/json"
	"fmt"
)

// flowKey tracks a multi-step conversation in a chat.
func flowKey(chatID int64) string {
	return fmt.Sprintf("tg:flow:%d", chatID)
}

// flowState is a paused command waiting for the next reply.
type flowState struct {
	Cmd    string `json:"cmd"`
	Step   string `json:"step"`
	Hour   int    `json:"hour"`
	Minute int    `json:"minute"`
	Days   string `json:"days"`
}

func (b *Bot) loadFlow(ctx context.Context, chatID int64) (flowState, bool) {
	raw, ok, err := b.deps.Store.KVGet(ctx, flowKey(chatID))
	if err != nil || !ok || raw == "" {
		return flowState{}, false
	}
	var f flowState
	if err := json.Unmarshal([]byte(raw), &f); err != nil || f.Cmd == "" {
		return flowState{}, false
	}
	return f, true
}

func (b *Bot) saveFlow(ctx context.Context, chatID int64, f flowState) {
	raw, err := json.Marshal(f)
	if err != nil {
		b.logger.Warn("encode flow", "err", err)
		return
	}
	if err := b.deps.Store.KVSet(ctx, flowKey(chatID), string(raw)); err != nil {
		b.logger.Warn("save flow", "err", err)
	}
}

// clearChatState drops every pending interaction in a chat: edits, flows,
// trending picks. Used by /cancel.
func (b *Bot) clearChatState(ctx context.Context, chatID int64) {
	for _, key := range []string{editKey(chatID), flowKey(chatID), pickKey(chatID)} {
		if err := b.deps.Store.KVSet(ctx, key, ""); err != nil {
			b.logger.Warn("clear chat state", "key", key, "err", err)
		}
	}
}
