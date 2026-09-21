package discord

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/PraisejahOsumgbaBenson/pulse/internal/linkedin"
	"github.com/PraisejahOsumgbaBenson/pulse/internal/store"
	"github.com/bwmarrin/discordgo"
)

// Card actions carried in button and modal custom IDs.
const (
	actionApprove = "approve"
	actionEdit    = "edit"
	actionRegen   = "regen"
	actionSkip    = "skip"
	actionSnooze  = "snooze"
	actionEditModal = "editmodal"
)

// snoozeFor is how long Snooze holds a draft before re-announcing it.
const snoozeFor = time.Hour

const (
	colorPending = 0x5865F2
	colorPosted  = 0x57F287
	colorFailed  = 0xED4245
	colorSkipped = 0x95A5A6
)

func customID(action string, draftID int64) string {
	return fmt.Sprintf("pulse:%s:%d", action, draftID)
}

// parseCustomID splits pulse:<action>:<id> back into its parts.
func parseCustomID(s string) (action string, draftID int64, ok bool) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 || parts[0] != "pulse" {
		return "", 0, false
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || id <= 0 {
		return "", 0, false
	}
	return parts[1], id, true
}

// renderCard builds the embed and buttons for a draft's current state.
func renderCard(d store.Draft, articleTitle string) (embeds []*discordgo.MessageEmbed, components []discordgo.MessageComponent) {
	var footer *discordgo.MessageEmbedFooter
	if articleTitle != "" {
		footer = &discordgo.MessageEmbedFooter{Text: "Source: " + articleTitle}
	}

	switch d.Status {
	case store.DraftPosted:
		text := d.Text
		if utf8.RuneCountInString(text) > 500 {
			text = string([]rune(text)[:497]) + "..."
		}
		embeds = []*discordgo.MessageEmbed{{
			Title:       fmt.Sprintf("Draft #%d, posted to LinkedIn", d.ID),
			Description: text,
			Color:       colorPosted,
		}}
		if d.LinkedInURN != "" {
			components = []discordgo.MessageComponent{
				&discordgo.ActionsRow{Components: []discordgo.MessageComponent{
					&discordgo.Button{
						Label: "View on LinkedIn",
						Style: discordgo.LinkButton,
						URL:   linkedin.PostURL(d.LinkedInURN),
					},
				}},
			}
		}
		return embeds, components
	case store.DraftSkipped:
		embeds = []*discordgo.MessageEmbed{{
			Title:       fmt.Sprintf("Draft #%d, skipped", d.ID),
			Description: "This one will not post. Generate another with /draft.",
			Color:       colorSkipped,
			Footer:      footer,
		}}
		return embeds, nil
	}

	embed := &discordgo.MessageEmbed{
		Description: d.Text,
		Footer:      footer,
	}
	if d.Status == store.DraftFailed {
		embed.Title = fmt.Sprintf("Draft #%d, post failed", d.ID)
		embed.Color = colorFailed
		if d.Error != "" {
			embed.Fields = []*discordgo.MessageEmbedField{{
				Name:   "What went wrong",
				Value:  d.Error,
				Inline: false,
			}}
		}
	} else {
		embed.Title = fmt.Sprintf("Draft #%d, ready to review", d.ID)
		embed.Color = colorPending
	}
	components = []discordgo.MessageComponent{
		&discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			&discordgo.Button{Label: "Approve", Style: discordgo.PrimaryButton, CustomID: customID(actionApprove, d.ID), Emoji: &discordgo.ComponentEmoji{Name: "✅"}},
			&discordgo.Button{Label: "Edit", Style: discordgo.SecondaryButton, CustomID: customID(actionEdit, d.ID), Emoji: &discordgo.ComponentEmoji{Name: "✏️"}},
			&discordgo.Button{Label: "Regenerate", Style: discordgo.SecondaryButton, CustomID: customID(actionRegen, d.ID), Emoji: &discordgo.ComponentEmoji{Name: "🔄"}},
			&discordgo.Button{Label: "Skip", Style: discordgo.DangerButton, CustomID: customID(actionSkip, d.ID), Emoji: &discordgo.ComponentEmoji{Name: "⏭️"}},
			&discordgo.Button{Label: "Snooze 1h", Style: discordgo.SecondaryButton, CustomID: customID(actionSnooze, d.ID), Emoji: &discordgo.ComponentEmoji{Name: "⏰"}},
		}},
	}
	return []*discordgo.MessageEmbed{embed}, components
}

func (b *Bot) onComponent(s *discordgo.Session, i *discordgo.InteractionCreate) {
	userID := invokerID(i)
	if !b.allowed(userID) {
		b.deny(s, i)
		return
	}
	action, draftID, ok := parseCustomID(i.MessageComponentData().CustomID)
	if !ok {
		_ = respondEphemeral(s, i, "I do not recognize that button.")
		return
	}
	ctx := context.Background()
	d, err := b.deps.Store.GetDraft(ctx, draftID)
	if err != nil {
		_ = respondEphemeral(s, i, fmt.Sprintf("Draft #%d is gone.", draftID))
		return
	}

	if action == actionEdit {
		b.openEditModal(s, i, d)
		return
	}

	if err := deferComponentUpdate(s, i); err != nil {
		b.logger.Warn("defer component", "err", err)
		return
	}

	switch action {
	case actionApprove:
		b.handleApprove(s, i, d)
	case actionRegen:
		b.handleRegen(s, i, userID, d)
	case actionSkip:
		b.handleSkip(s, i, d)
	case actionSnooze:
		b.handleSnooze(s, i, d)
	default:
		_ = followupEphemeral(s, i, "I do not recognize that button.")
	}
}

func (b *Bot) handleApprove(s *discordgo.Session, i *discordgo.InteractionCreate, d store.Draft) {
	ctx := context.Background()
	urn, err := b.deps.Pub.Publish(ctx, d.ID)
	if err != nil {
		b.logger.Warn("publish failed", "draft", d.ID, "err", err)
		_ = b.editCard(ctx, d)
		_ = followupEphemeral(s, i, "Could not post: "+err.Error())
		return
	}
	_ = b.editCard(ctx, d)
	_ = followupEphemeral(s, i, "Posted to LinkedIn: "+linkedin.PostURL(urn))
}

func (b *Bot) handleRegen(s *discordgo.Session, i *discordgo.InteractionCreate, userID string, d store.Draft) {
	ctx := context.Background()
	art, err := b.deps.Store.NextUnusedArticle(ctx)
	if err != nil {
		_ = followupEphemeral(s, i, "No fresh articles left. Add sources with /source-add.")
		return
	}
	text, err := b.deps.Gen.Generate(ctx, generateInput(art))
	if err != nil {
		b.logger.Warn("regenerate failed", "draft", d.ID, "err", err)
		_ = followupEphemeral(s, i, "Generation failed: "+err.Error())
		return
	}
	if err := b.deps.Store.UpdateDraftArticle(ctx, d.ID, art.ID, text); err != nil {
		_ = followupEphemeral(s, i, "Could not save the new draft: "+err.Error())
		return
	}
	if err := b.deps.Store.MarkArticleUsed(ctx, art.ID); err != nil {
		b.logger.Warn("mark article used", "article", art.ID, "err", err)
	}
	_ = b.editCard(ctx, d)
}

func (b *Bot) handleSkip(s *discordgo.Session, i *discordgo.InteractionCreate, d store.Draft) {
	ctx := context.Background()
	if err := b.deps.Store.UpdateDraftStatus(ctx, d.ID, store.DraftSkipped, "", ""); err != nil {
		_ = followupEphemeral(s, i, "Could not skip: "+err.Error())
		return
	}
	_ = b.deps.Store.ClearSnoozesForDraft(ctx, d.ID)
	_ = b.editCard(ctx, d)
}

func (b *Bot) handleSnooze(s *discordgo.Session, i *discordgo.InteractionCreate, d store.Draft) {
	ctx := context.Background()
	if d.Status != store.DraftPending && d.Status != store.DraftFailed {
		_ = followupEphemeral(s, i, fmt.Sprintf("Draft #%d already moved on.", d.ID))
		return
	}
	if err := b.deps.Store.AddSnooze(ctx, d.ID, time.Now().Add(snoozeFor)); err != nil {
		_ = followupEphemeral(s, i, "Could not snooze: "+err.Error())
		return
	}
	_ = followupEphemeral(s, i, fmt.Sprintf("Snoozed draft #%d for an hour.", d.ID))
}

func (b *Bot) openEditModal(s *discordgo.Session, i *discordgo.InteractionCreate, d store.Draft) {
	value := d.Text
	if utf8.RuneCountInString(value) > 3000 {
		value = string([]rune(value)[:3000])
	}
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{
			CustomID: customID(actionEditModal, d.ID),
			Title:    fmt.Sprintf("Edit draft #%d", d.ID),
			Components: []discordgo.MessageComponent{
				&discordgo.ActionsRow{Components: []discordgo.MessageComponent{
					&discordgo.TextInput{
						CustomID:    "pulse:edittext",
						Label:       "Post text, LinkedIn allows 3000 characters",
						Style:       discordgo.TextInputParagraph,
						Placeholder: "Write the post...",
						Required:    true,
						MinLength:   1,
						MaxLength:   3000,
						Value:       value,
					},
				}},
			},
		},
	})
	if err != nil {
		b.logger.Warn("open edit modal", "draft", d.ID, "err", err)
	}
}

func (b *Bot) onModalSubmit(s *discordgo.Session, i *discordgo.InteractionCreate) {
	userID := invokerID(i)
	if !b.allowed(userID) {
		b.deny(s, i)
		return
	}
	action, draftID, ok := parseCustomID(i.ModalSubmitData().CustomID)
	if !ok || action != actionEditModal {
		_ = respondEphemeral(s, i, "I do not recognize that form.")
		return
	}
	ctx := context.Background()
	d, err := b.deps.Store.GetDraft(ctx, draftID)
	if err != nil {
		_ = respondEphemeral(s, i, fmt.Sprintf("Draft #%d is gone.", draftID))
		return
	}
	text := strings.TrimSpace(modalText(i.ModalSubmitData(), "pulse:edittext"))
	if text == "" {
		_ = respondEphemeral(s, i, "The post text cannot be empty.")
		return
	}
	if utf8.RuneCountInString(text) > 3000 {
		_ = respondEphemeral(s, i, "That is over LinkedIn's 3000-character limit. Trim it and try again.")
		return
	}
	if err := b.deps.Store.UpdateDraftText(ctx, draftID, text); err != nil {
		_ = respondEphemeral(s, i, "Could not save: "+err.Error())
		return
	}
	if err := b.editCard(ctx, d); err != nil {
		b.logger.Warn("edit card after modal", "draft", draftID, "err", err)
	}
	_ = respondEphemeral(s, i, fmt.Sprintf("Draft #%d updated.", draftID))
}

// modalText pulls one text input out of a modal submission.
func modalText(data discordgo.ModalSubmitInteractionData, customID string) string {
	for _, c := range data.Components {
		row, ok := c.(*discordgo.ActionsRow)
		if !ok {
			continue
		}
		for _, inner := range row.Components {
			if ti, ok := inner.(*discordgo.TextInput); ok && ti.CustomID == customID {
				return ti.Value
			}
		}
	}
	return ""
}

func respondEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate, content string) error {
	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

func deferComponentUpdate(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredMessageUpdate,
	})
}

func deferEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate) error {
	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags: discordgo.MessageFlagsEphemeral,
		},
	})
}

func followupEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate, content string) error {
	_, err := s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Content: content,
		Flags:   discordgo.MessageFlagsEphemeral,
	})
	return err
}
