package whatsapp

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image/jpeg"
	"mime/multipart"
	"net/http"
	"net/textproto"
)

const maxGroupPhoto = 5 * 1024 * 1024

// groupPhoto accepts image bytes, never a server file path or a fetch URL. Meta
// requires a JPEG no larger than 5 MB, square, and at least 192 pixels per side.
// https://developers.facebook.com/documentation/business-messaging/whatsapp/groups/reference#update-group-settings
func (s *Service) groupPhoto(ctx context.Context, a Account, in Input, validateOnly bool) (map[string]any, error) {
	if len(in.PhotoBase64) == 0 || len(in.PhotoBase64) > base64.StdEncoding.EncodedLen(maxGroupPhoto) {
		return nil, errors.New("group photo requires a base64 JPEG up to 5 MB")
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(in.PhotoBase64)
	if err != nil || len(raw) > maxGroupPhoto {
		return nil, errors.New("invalid group photo encoding or size")
	}
	dimensions, err := jpeg.DecodeConfig(bytes.NewReader(raw))
	if err != nil || dimensions.Width != dimensions.Height || dimensions.Width < 192 || dimensions.Width > 2048 {
		return nil, errors.New("group photo must be a square JPEG from 192 to 2048 pixels")
	}
	// Decode the bounded image to reject corrupt JPEG streams before submission.
	if _, err = jpeg.Decode(bytes.NewReader(raw)); err != nil {
		return nil, errors.New("invalid JPEG image")
	}
	if validateOnly {
		return nil, nil
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err = writer.WriteField("messaging_product", "whatsapp"); err != nil {
		return nil, err
	}
	headers := textproto.MIMEHeader{}
	headers.Set("Content-Disposition", `form-data; name="file"; filename="group.jpg"`)
	headers.Set("Content-Type", "image/jpeg")
	part, err := writer.CreatePart(headers)
	if err != nil {
		return nil, err
	}
	if _, err = part.Write(raw); err != nil {
		return nil, err
	}
	if err = writer.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+"/"+a.Version+"/"+in.ID, &body)
	if err != nil {
		return nil, errors.New("cannot build photo request")
	}
	req.Header.Set("Authorization", "Bearer "+a.AccessToken)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return s.request(req)
}
