package telegram

import (
	"fmt"
	"testing"
)

func TestTypedIncomingMessagesPreserveContentAndQuotes(t *testing.T) {
	c, _ := New(Config{AccountID: "a", BotToken: groupSimulationToken, Enabled: true})
	for _, tc := range []struct{ name, field, payload string }{
		{"static sticker", "sticker", `{"file_id":"CAAC_static","file_unique_id":"unique_1","type":"regular","width":512,"height":512,"is_animated":false,"is_video":false,"emoji":"👍","file_size":123}`},
		{"animated sticker", "sticker", `{"file_id":"CAAC_tgs","file_unique_id":"unique_2","type":"regular","width":512,"height":512,"is_animated":true,"is_video":false}`},
		{"video sticker", "sticker", `{"file_id":"CAAC_webm","file_unique_id":"unique_3","type":"regular","width":512,"height":512,"is_animated":false,"is_video":true}`},
		{"location", "location", `{"latitude":13.0827,"longitude":80.2707,"horizontal_accuracy":20}`},
		{"contact", "contact", `{"phone_number":"+919876543210","first_name":"தமிழ்","last_name":"Support","user_id":42,"vcard":"BEGIN:VCARD\r\nVERSION:3.0\r\nFN:தமிழ்\r\nEND:VCARD"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := fmt.Sprintf(`{"update_id":187,"message":{"message_id":24,"date":1700000000,"chat":{"id":-100123,"type":"supergroup"},"from":{"id":42},"message_thread_id":91,"reply_to_message":{"message_id":17,"text":"Original quote"},%q:%s}}`, tc.field, tc.payload)
			m, err := c.ParseUpdate([]byte(raw))
			if err != nil || m.ContentType != tc.field || m.Kind != "message" || m.ChatID != -100123 || m.SenderID != 42 || m.ThreadID != 91 || m.ReplyTo != 17 || m.Quote == nil || m.Quote.Text != "Original quote" {
				t.Fatalf("message content/routing lost %#v err=%v", m, err)
			}
			switch tc.name {
			case "static sticker":
				if m.Sticker.FileID != "CAAC_static" || m.Sticker.Emoji != "👍" || m.Sticker.IsAnimated || m.Sticker.IsVideo {
					t.Fatal("static sticker metadata lost")
				}
			case "animated sticker":
				if !m.Sticker.IsAnimated || m.Sticker.FileID != "CAAC_tgs" {
					t.Fatal("animated sticker metadata lost")
				}
			case "video sticker":
				if !m.Sticker.IsVideo || m.Sticker.FileID != "CAAC_webm" {
					t.Fatal("video sticker metadata lost")
				}
			case "location":
				if m.Location.Latitude != 13.0827 || m.Location.Longitude != 80.2707 || m.Location.HorizontalAccuracy != 20 {
					t.Fatal("location lost")
				}
			case "contact":
				if m.Contact.FirstName != "தமிழ்" || m.Contact.PhoneNumber != "+919876543210" || m.Contact.UserID != 42 || m.Contact.VCard == "" {
					t.Fatal("contact lost")
				}
			}
		})
	}
}
func TestTypedReactionClearAndEditedLocation(t *testing.T) {
	c, _ := New(Config{AccountID: "a", BotToken: groupSimulationToken, Enabled: true})
	reaction := `{"update_id":188,"message_reaction":{"chat":{"id":-100123,"type":"supergroup"},"message_id":24,"date":1700000000,"user":{"id":42},"old_reaction":[{"type":"emoji","emoji":"👍"}],"new_reaction":[]}}`
	m, err := c.ParseUpdate([]byte(reaction))
	if err != nil || m.ContentType != "reaction" || m.Kind != "message_reaction" || m.Reactions == nil || len(m.Reactions) != 0 || len(m.OldReactions) != 1 || m.OldReactions[0].Emoji != "👍" || m.MessageID != 24 || m.Text != "" {
		t.Fatalf("reaction removal lost %#v %v", m, err)
	}
	counts := `{"update_id":189,"message_reaction_count":{"chat":{"id":-100123,"type":"supergroup"},"message_id":24,"date":1700000000,"reactions":[{"type":{"type":"emoji","emoji":"👍"},"total_count":3}]}}`
	m, err = c.ParseUpdate([]byte(counts))
	if err != nil || len(m.ReactionCounts) != 1 || m.ReactionCounts[0].TotalCount != 3 || m.ReactionCounts[0].Type.Emoji != "👍" {
		t.Fatalf("reaction count lost %#v %v", m, err)
	}
	edit := `{"update_id":190,"edited_message":{"message_id":24,"date":1700000000,"chat":{"id":-100123,"type":"supergroup"},"message_thread_id":91,"location":{"latitude":13,"longitude":80,"live_period":900}}}`
	m, err = c.ParseUpdate([]byte(edit))
	if err != nil || m.Kind != "edited_message" || m.ContentType != "location" || m.Location.LivePeriod != 900 || m.ThreadID != 91 {
		t.Fatalf("edited location incorrectly normalized %#v %v", m, err)
	}
}
func TestMalformedTypedContentRejected(t *testing.T) {
	c, _ := New(Config{AccountID: "a", BotToken: groupSimulationToken, Enabled: true})
	for _, content := range []string{`"sticker":{"file_id":"../file"}`, `"location":{"latitude":91,"longitude":0}`, `"sticker":{"file_id":"valid"},"text":"another type"`, `"sticker":{"file_id":"valid"},"contact":{"first_name":"A","phone_number":"+123456"}`} {
		if _, err := c.ParseUpdate([]byte(fmt.Sprintf(`{"update_id":191,"message":{"message_id":24,"chat":{"id":-100123,"type":"supergroup"},%s}}`, content))); err == nil {
			t.Fatal("invalid typed content accepted")
		}
	}
}
