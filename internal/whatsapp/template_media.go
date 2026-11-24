package whatsapp

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Session IDs are provider-issued paths, never arbitrary URLs. Preserve the
// signed query without letting an upstream response choose another endpoint.
var uploadSession = regexp.MustCompile(`^upload:[A-Za-z0-9_+=-]+(?:\?sig=[A-Za-z0-9_-]+)?$`)

func (s *Service) uploadTemplateSample(ctx context.Context, a Account, in Input, validateOnly bool) (map[string]any, error) {
	if !a.Templates || !digits.MatchString(a.AppID) {
		return nil, errors.New("enable templates and configure the Meta app ID before uploading a sample")
	}
	if in.MediaType != "image" && in.MediaType != "video" && in.MediaType != "document" {
		return nil, errors.New("template samples must be an image, video or PDF document")
	}
	raw, mediaType, err := attachmentBytes(in)
	if err != nil {
		return nil, err
	}
	if in.MediaType == "document" && mediaType != "application/pdf" {
		return nil, errors.New("template document samples must be PDF")
	}
	if validateOnly {
		return map[string]any{"valid": true}, nil
	}
	handle, err := s.resumableUpload(ctx, a, raw, mediaType, in.Filename)
	if err != nil {
		return nil, err
	}
	return map[string]any{"handle": handle}, nil
}

// resumableUpload sends one file through Meta's Resumable Upload API and
// returns its handle, for a template sample or a profile photo.
func (s *Service) resumableUpload(ctx context.Context, a Account, raw []byte, mediaType, filename string) (string, error) {
	session, err := s.graph(ctx, a, http.MethodPost, a.AppID+"/uploads", url.Values{
		"file_length": {strconv.Itoa(len(raw))}, "file_type": {mediaType}, "file_name": {filename},
	}, nil)
	if err != nil {
		return "", err
	}
	id, _ := session["id"].(string)
	if len(id) > 4096 || !uploadSession.MatchString(id) {
		return "", errors.New("provider returned an invalid upload session")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+"/"+a.Version+"/"+id, bytes.NewReader(raw))
	if err != nil {
		return "", errors.New("cannot prepare the upload")
	}
	req.Header.Set("Authorization", "OAuth "+a.AccessToken)
	req.Header.Set("Content-Type", mediaType)
	req.Header.Set("file_offset", "0")
	result, err := s.request(req)
	if err != nil {
		return "", err
	}
	handle, _ := result["h"].(string)
	if strings.TrimSpace(handle) == "" || len(handle) > 8192 || strings.ContainsAny(handle, "\r\n") {
		return "", errors.New("provider did not return an upload handle")
	}
	return handle, nil
}
