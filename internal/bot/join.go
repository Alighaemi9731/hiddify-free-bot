package bot

import (
	"log"

	tele "gopkg.in/telebot.v4"
)

// onJoinRequest auto-approves join requests for channels the bot tracks, so
// private advertising channels work frictionlessly: the user taps the invite
// link, the bot approves, they become a member, and the daily-config gate
// passes (counting as a NEW join).
func (b *Bot) onJoinRequest(c tele.Context) error {
	req := c.Update().ChatJoinRequest
	if req == nil || req.Chat == nil || req.Sender == nil {
		return nil
	}
	ch, err := b.store.GetChannelByChat(req.Chat.ID)
	if err != nil || ch == nil {
		return nil // not one of our channels — leave it alone
	}
	if err := b.tb.ApproveJoinRequest(req.Chat, req.Sender); err != nil {
		log.Printf("approve join request chat=%d user=%d: %v", req.Chat.ID, req.Sender.ID, err)
	}
	return nil
}
