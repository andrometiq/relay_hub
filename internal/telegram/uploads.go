package telegram

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/andrometiq/relay_hub/internal/attachment"
	"mime/multipart"
	"net/http"
	"path"
	"strings"
)

func mediaOperation(operation string) bool {
	return operation == "photo" || operation == "video" || operation == "audio" || operation == "voice" || operation == "document"
}
func (in MessageInput) uploadBytes() ([]byte, error) {
	if in.FileID != "" {
		if !fileIDRE.MatchString(in.FileID) || in.ContentBase64 != "" || in.Filename != "" {
			return nil, errors.New("use one media source")
		}
		return nil, nil
	}
	if !cleanSingleLine(in.Filename, 120) || in.Filename == "" || path.Base(in.Filename) != in.Filename || strings.ContainsAny(in.Filename, "\\\"") {
		return nil, errors.New("invalid attachment filename")
	}
	if len(in.ContentBase64) > base64.StdEncoding.EncodedLen(attachment.MaxBytes) {
		return nil, errors.New("attachment exceeds 10 MiB")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(in.ContentBase64)
	if err != nil || len(data) == 0 || len(data) > attachment.MaxBytes {
		return nil, errors.New("invalid attachment content")
	}
	mime := http.DetectContentType(data)
	allowed := in.Operation == "document" || in.Operation == "photo" && (mime == "image/jpeg" || mime == "image/png" || mime == "image/webp") || in.Operation == "video" && mime == "video/mp4" || in.Operation == "audio" && (mime == "audio/mpeg" || mime == "audio/mp4" || mime == "video/mp4") || in.Operation == "voice" && (mime == "application/ogg" || mime == "audio/ogg")
	if !allowed {
		return nil, errors.New("attachment format does not match the selected media type")
	}
	return data, nil
}
func (c *Client) upload(ctx context.Context, method string, body map[string]any, in MessageInput) (json.RawMessage, error) {
	data, err := in.uploadBytes()
	if err != nil {
		return nil, err
	}
	var encoded bytes.Buffer
	writer := multipart.NewWriter(&encoded)
	for key, value := range body {
		var field string
		if _, ok := value.(map[string]any); ok {
			raw, e := json.Marshal(value)
			if e != nil {
				return nil, e
			}
			field = string(raw)
		} else {
			field = fmt.Sprint(value)
		}
		if err = writer.WriteField(key, field); err != nil {
			return nil, err
		}
	}
	part, err := writer.CreateFormFile(in.Operation, in.Filename)
	if err != nil {
		return nil, err
	}
	if _, err = part.Write(data); err != nil {
		return nil, err
	}
	if err = writer.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+c.cfg.BotToken+"/"+method, &encoded)
	if err != nil {
		return nil, errors.New("invalid media request")
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return c.perform(req)
}
