package telegram

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image/jpeg"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
)

const maxGroupPhotoBytes = 5 * 1024 * 1024

func (c *Client) setPhoto(ctx context.Context, chatID int64, encoded string) (json.RawMessage, error) {
	if len(encoded) == 0 || len(encoded) > base64.StdEncoding.EncodedLen(maxGroupPhotoBytes) {
		return nil, errors.New("Telegram group photo must be a JPEG of at most 5 MiB")
	}
	photo, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(photo) > maxGroupPhotoBytes {
		return nil, errors.New("invalid Telegram group photo encoding")
	}
	// Decode only the JPEG header. Never decompress an untrusted full bitmap in
	// the Hub process; also bound dimensions for eventual provider decoding.
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(photo))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 2048 || cfg.Height > 2048 {
		return nil, errors.New("Telegram group photo must be a valid JPEG no larger than 2048 by 2048 pixels")
	}
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	if err = writer.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
		return nil, errors.New("invalid Telegram photo request")
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="photo"; filename="group.jpg"`)
	header.Set("Content-Type", "image/jpeg")
	part, err := writer.CreatePart(header)
	if err != nil {
		return nil, errors.New("invalid Telegram photo request")
	}
	if _, err = part.Write(photo); err != nil {
		return nil, errors.New("invalid Telegram photo request")
	}
	if err = writer.Close(); err != nil {
		return nil, errors.New("invalid Telegram photo request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+c.cfg.BotToken+"/setChatPhoto", &buffer)
	if err != nil {
		return nil, errors.New("invalid Telegram photo request")
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return c.perform(req)
}
