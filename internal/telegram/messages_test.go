package telegram

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/andrometiq/relay_hub/internal/attachment"
	"io"
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func floatPointer(v float64) *float64 { return &v }
func TestTypedMessageContracts(t *testing.T) {
	const chat int64 = -1001234567890
	vcard := "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:தமிழ் Support\r\nTEL:+919876543210\r\nEND:VCARD"
	for _, tc := range []struct {
		name, method string
		in           MessageInput
		fields       map[string]any
		result       string
	}{
		{"quoted unicode text", "sendMessage", MessageInput{Operation: "text", Text: "வணக்கம் 🌟"}, map[string]any{"text": "வணக்கம் 🌟"}, `{"message_id":91}`},
		{"file sticker", "sendSticker", MessageInput{Operation: "sticker", Sticker: "CAACAgIAAxkBAAE_123"}, map[string]any{"sticker": "CAACAgIAAxkBAAE_123"}, `{"message_id":91}`},
		{"https static sticker", "sendSticker", MessageInput{Operation: "sticker", Sticker: "https://cdn.example.org/sticker.webp?v=2"}, map[string]any{"sticker": "https://cdn.example.org/sticker.webp?v=2"}, `{"message_id":91}`},
		{"reaction emoji", "setMessageReaction", MessageInput{Operation: "reaction", MessageID: 29, Reaction: []Reaction{{Type: "emoji", Emoji: "👍"}}, IsBig: true}, map[string]any{"message_id": float64(29), "reaction": []any{map[string]any{"type": "emoji", "emoji": "👍"}}, "is_big": true}, `true`},
		{"custom reaction", "setMessageReaction", MessageInput{Operation: "reaction", MessageID: 29, Reaction: []Reaction{{Type: "custom_emoji", CustomEmojiID: "5368324170671202286"}}}, map[string]any{"message_id": float64(29), "reaction": []any{map[string]any{"type": "custom_emoji", "custom_emoji_id": "5368324170671202286"}}}, `true`},
		{"clear reaction", "setMessageReaction", MessageInput{Operation: "reaction", MessageID: 29, Reaction: []Reaction{}}, map[string]any{"message_id": float64(29), "reaction": []any{}}, `true`},
		{"zero coordinates", "sendLocation", MessageInput{Operation: "location", Latitude: floatPointer(0), Longitude: floatPointer(0)}, map[string]any{"latitude": float64(0), "longitude": float64(0)}, `{"message_id":91}`},
		{"bounded coordinates", "sendLocation", MessageInput{Operation: "location", Latitude: floatPointer(-90), Longitude: floatPointer(180)}, map[string]any{"latitude": float64(-90), "longitude": float64(180)}, `{"message_id":91}`},
		{"contact vcard", "sendContact", MessageInput{Operation: "contact", PhoneNumber: "+919876543210", FirstName: "தமிழ்", LastName: "Support", VCard: vcard}, map[string]any{"phone_number": "+919876543210", "first_name": "தமிழ்", "last_name": "Support", "vcard": vcard}, `{"message_id":91}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.in
			in.ChatID = chat
			expected := map[string]any{"chat_id": float64(chat)}
			for k, v := range tc.fields {
				expected[k] = v
			}
			if in.Operation != "reaction" {
				in.ThreadID = 87
				in.ReplyTo = 29
				expected["message_thread_id"] = float64(87)
				expected["reply_parameters"] = map[string]any{"message_id": float64(29)}
			}
			calls := 0
			c := simulatedGroupClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if !strings.HasSuffix(r.URL.Path, "/"+tc.method) {
					t.Error("wrong provider method")
				}
				var got map[string]any
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Error(err)
				}
				if !reflect.DeepEqual(got, expected) {
					t.Errorf("payload mismatch got=%v want=%v", got, expected)
				}
				fmt.Fprintf(w, `{"ok":true,"result":%s}`, tc.result)
			})
			result, err := c.ExecuteMessage(t.Context(), in)
			expectedID := int64(91)
			if in.Operation == "reaction" {
				expectedID = 29
			}
			if err != nil || calls != 1 || result["chat_id"] != chat || result["message_id"] != expectedID || result["operation"] != in.Operation {
				t.Fatalf("invalid message receipt %#v %v", result, err)
			}
		})
	}
}
func TestInvalidMessageTypesNeverReachProvider(t *testing.T) {
	c, _ := NewWithHTTPClient(Config{AccountID: "a", BotToken: groupSimulationToken, Enabled: true}, &http.Client{Transport: simulationTransport(func(r *http.Request) (*http.Response, error) {
		t.Fatal("invalid message reached provider")
		return nil, nil
	})})
	for _, tc := range []struct {
		name string
		in   MessageInput
	}{
		{"unsupported", MessageInput{Operation: "sendMoney"}}, {"http sticker", MessageInput{Operation: "sticker", Sticker: "http://example.org/s.webp"}},
		{"local sticker", MessageInput{Operation: "sticker", Sticker: "https://127.0.0.1/s.webp"}}, {"private sticker", MessageInput{Operation: "sticker", Sticker: "https://192.168.1.2/s.webp"}}, {"url credentials", MessageInput{Operation: "sticker", Sticker: "https://token:secret@example.org/s.webp"}},
		{"animated URL", MessageInput{Operation: "sticker", Sticker: "https://example.org/s.tgs"}}, {"video URL", MessageInput{Operation: "sticker", Sticker: "https://example.org/s.webm"}}, {"local file", MessageInput{Operation: "sticker", Sticker: "/tmp/s.webp"}}, {"attach unsupported", MessageInput{Operation: "sticker", Sticker: "attach://photo"}},
		{"reaction target absent", MessageInput{Operation: "reaction", Reaction: []Reaction{}}}, {"reaction absent", MessageInput{Operation: "reaction", MessageID: 12}}, {"too many reactions", MessageInput{Operation: "reaction", MessageID: 12, Reaction: []Reaction{{Type: "emoji", Emoji: "👍"}, {Type: "emoji", Emoji: "❤️"}}}},
		{"paid reaction", MessageInput{Operation: "reaction", MessageID: 12, Reaction: []Reaction{{Type: "paid"}}}}, {"text reaction", MessageInput{Operation: "reaction", MessageID: 12, Reaction: []Reaction{{Type: "emoji", Emoji: "hello"}}}}, {"mixed reaction", MessageInput{Operation: "reaction", MessageID: 12, Reaction: []Reaction{{Type: "emoji", Emoji: "👍", CustomEmojiID: "123"}}}},
		{"thread reaction", MessageInput{Operation: "reaction", MessageID: 12, ThreadID: 7, Reaction: []Reaction{}}}, {"quote reaction", MessageInput{Operation: "reaction", MessageID: 12, ReplyTo: 7, Reaction: []Reaction{}}},
		{"missing latitude", MessageInput{Operation: "location", Longitude: floatPointer(0)}}, {"latitude bound", MessageInput{Operation: "location", Latitude: floatPointer(90.1), Longitude: floatPointer(0)}}, {"longitude bound", MessageInput{Operation: "location", Latitude: floatPointer(0), Longitude: floatPointer(-180.1)}}, {"infinity", MessageInput{Operation: "location", Latitude: floatPointer(math.Inf(1)), Longitude: floatPointer(0)}}, {"NaN", MessageInput{Operation: "location", Latitude: floatPointer(0), Longitude: floatPointer(math.NaN())}},
		{"contact phone injection", MessageInput{Operation: "contact", PhoneNumber: "+123\r\nFN:evil", FirstName: "A"}}, {"contact name injection", MessageInput{Operation: "contact", PhoneNumber: "+123456", FirstName: "A\nEND:VCARD"}}, {"contact name absent", MessageInput{Operation: "contact", PhoneNumber: "+123456"}},
		{"contact multiple cards", MessageInput{Operation: "contact", PhoneNumber: "+123456", FirstName: "A", VCard: "BEGIN:VCARD\nVERSION:3.0\nEND:VCARD\nBEGIN:VCARD\nVERSION:3.0\nEND:VCARD"}},
		{"contact vcard bound", MessageInput{Operation: "contact", PhoneNumber: "+123456", FirstName: "A", VCard: strings.Repeat("x", 2049)}},
		{"operation mismatch", MessageInput{Operation: "location", Text: "hidden", Latitude: floatPointer(0), Longitude: floatPointer(0)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.in.ChatID = -100123
			if _, err := c.ExecuteMessage(t.Context(), tc.in); err == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
}
func TestTypedMessageFailuresNeverRetry(t *testing.T) {
	for _, op := range []string{"sticker", "reaction", "location", "contact"} {
		for _, status := range []int{400, 403, 429} {
			t.Run(fmt.Sprintf("%s/%d", op, status), func(t *testing.T) {
				calls := 0
				c := simulatedGroupClient(t, func(w http.ResponseWriter, r *http.Request) {
					calls++
					w.WriteHeader(status)
					fmt.Fprintf(w, `{"ok":false,"error_code":%d,"description":%q,"parameters":{"retry_after":4}}`, status, groupSimulationToken)
				})
				in := MessageInput{Operation: op, ChatID: -100123}
				switch op {
				case "sticker":
					in.Sticker = "file_123"
				case "reaction":
					in.MessageID = 17
					in.Reaction = []Reaction{}
				case "location":
					in.Latitude = floatPointer(10)
					in.Longitude = floatPointer(20)
				case "contact":
					in.PhoneNumber = "+1234567"
					in.FirstName = "A"
				}
				_, err := c.ExecuteMessage(t.Context(), in)
				var pe *APIError
				if !errors.As(err, &pe) || pe.StatusCode != status || pe.RetryAfter != 4 || calls != 1 || strings.Contains(err.Error(), groupSimulationToken) {
					t.Fatalf("unsafe failure handling: %v calls=%d", err, calls)
				}
			})
		}
	}
}
func TestMessageReceiptMustConfirmCorrectChat(t *testing.T) {
	for _, body := range []string{`{"ok":true,"result":false}`, `{"ok":true,"result":{"message_id":0}}`, `{"ok":true,"result":{"message_id":17,"chat":{"id":12345}}}`} {
		t.Run(body, func(t *testing.T) {
			c := simulatedGroupClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
			if _, err := c.ExecuteMessage(t.Context(), MessageInput{Operation: "sticker", ChatID: -100123, Sticker: "file_123"}); err == nil {
				t.Fatal("incorrect receipt accepted")
			}
		})
	}
	c := simulatedGroupClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"ok":true,"result":false}`) })
	if _, err := c.ExecuteMessage(t.Context(), MessageInput{Operation: "reaction", ChatID: -100123, MessageID: 17, Reaction: []Reaction{}}); err == nil {
		t.Fatal("unconfirmed reaction accepted")
	}
}

func TestMultipartMediaBoundaries(t *testing.T) {
	payload := []byte("Synthetic private document")
	for _, operation := range []string{"photo", "video", "audio", "voice", "document"} {
		t.Run(operation, func(t *testing.T) {
			calls := 0
			c := simulatedGroupClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if operation == "document" {
					if err := r.ParseMultipartForm(5 << 20); err != nil {
						t.Fatal(err)
					}
					file, header, err := r.FormFile("document")
					if err != nil {
						t.Fatal(err)
					}
					defer file.Close()
					raw, _ := io.ReadAll(file)
					if string(raw) != string(payload) || header.Filename != "evidence.txt" || r.FormValue("message_thread_id") != "7" || r.FormValue("reply_parameters") != "{\"message_id\":9}" {
						t.Fatal("multipart lost content or routing")
					}
				}
				fmt.Fprint(w, `{"ok":true,"result":{"message_id":99}}`)
			})
			in := MessageInput{Operation: operation, ChatID: 123, ThreadID: 7, ReplyTo: 9, Filename: "evidence.txt", ContentBase64: base64.StdEncoding.EncodeToString(payload), Caption: "Evidence"}
			_, err := c.ExecuteMessage(t.Context(), in)
			if operation == "document" {
				if err != nil || calls != 1 {
					t.Fatal(err)
				}
			} else if err == nil || calls != 0 {
				t.Fatal("format mismatch reached provider")
			}
			in.Filename = "../evidence.txt"
			if in.Validate() == nil {
				t.Fatal("path filename accepted")
			}
			in.Filename = "evidence.txt"
			in.FileID = "known_id"
			if in.Validate() == nil {
				t.Fatal("two media sources accepted")
			}
		})
	}
	in := MessageInput{Operation: "document", ChatID: 123, Filename: "large.bin", ContentBase64: strings.Repeat("A", base64.StdEncoding.EncodedLen(attachment.MaxBytes)+4)}
	if in.Validate() == nil {
		t.Fatal("oversized media accepted")
	}
}
