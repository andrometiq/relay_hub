package whatsapp

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"github.com/andrometiq/relay_hub/internal/attachment"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
)

func attachmentBytes(in Input) ([]byte, string, error) {
	if len(in.Filename) > 120 || strings.ContainsAny(in.Filename, "/\\\r\n\"") {
		return nil, "", errors.New("invalid attachment filename")
	}
	if in.ContentBase64 == "" || len(in.ContentBase64) > base64.StdEncoding.EncodedLen(attachment.MaxBytes) || in.MediaID != "" || in.MediaURL != "" {
		return nil, "", errors.New("supply one private attachment up to 10 MiB")
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(in.ContentBase64)
	if err != nil || len(raw) == 0 || len(raw) > attachment.MaxBytes {
		return nil, "", errors.New("invalid attachment encoding or size")
	}
	mediaType, _, err := mime.ParseMediaType(in.MIMEType)
	if err != nil {
		return nil, "", errors.New("attachment MIME type required")
	}
	detected, _, _ := mime.ParseMediaType(http.DetectContentType(raw))
	allowed := map[string]map[string]bool{
		"image":   {"image/jpeg": true, "image/png": true},
		"sticker": {"image/webp": true},
		"video":   {"video/mp4": true},
		"audio":   {"audio/mpeg": true, "audio/mp4": true, "audio/ogg": true, "audio/aac": true},
		"document": {"application/pdf": true, "text/plain": true, "application/msword": true, "application/vnd.ms-excel": true, "application/vnd.ms-powerpoint": true,
			"application/vnd.openxmlformats-officedocument.wordprocessingml.document": true, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": true, "application/vnd.openxmlformats-officedocument.presentationml.presentation": true},
	}
	if !allowed[in.MediaType][mediaType] {
		return nil, "", errors.New("unsupported attachment MIME type")
	}
	if in.MediaType != "document" && detected != mediaType && !(mediaType == "audio/ogg" && detected == "application/ogg") && !(mediaType == "audio/mp4" && detected == "video/mp4") && !(mediaType == "audio/aac" && detected == "application/octet-stream") {
		return nil, "", errors.New("attachment content does not match its media type")
	}
	if mediaType == "application/pdf" && detected != "application/pdf" {
		return nil, "", errors.New("invalid PDF attachment")
	}
	return raw, mediaType, nil
}

func (s *Service) uploadAttachment(ctx context.Context, a Account, in Input, raw []byte, mediaType string) (string, error) {
	filename := in.Filename
	if filename == "" {
		filename = "attachment"
	}
	if len(filename) > 120 || strings.ContainsAny(filename, "/\\\r\n\"") {
		return "", errors.New("invalid attachment filename")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("messaging_product", "whatsapp"); err != nil {
		return "", err
	}
	if err := writer.WriteField("type", mediaType); err != nil {
		return "", err
	}
	headers := textproto.MIMEHeader{}
	headers.Set("Content-Disposition", `form-data; name="file"; filename="`+filename+`"`)
	headers.Set("Content-Type", mediaType)
	part, err := writer.CreatePart(headers)
	if err != nil {
		return "", err
	}
	if _, err = part.Write(raw); err != nil {
		return "", err
	}
	if err = writer.Close(); err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+"/"+a.Version+"/"+a.PhoneID+"/media", &body)
	if err != nil {
		return "", errors.New("cannot prepare media upload")
	}
	request.Header.Set("Authorization", "Bearer "+a.AccessToken)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	result, err := s.request(request)
	if err != nil {
		return "", err
	}
	id, _ := result["id"].(string)
	if !digits.MatchString(id) {
		return "", errors.New("provider did not return a media identifier")
	}
	return id, nil
}
