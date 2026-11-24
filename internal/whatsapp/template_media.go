package whatsapp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
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
	// Meta may take part of the file and answer with the file_offset to resume from instead of a handle.
	offset := 0
	for attempt := 0; attempt < 8; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+"/"+a.Version+"/"+id, bytes.NewReader(raw[offset:]))
		if err != nil {
			return "", errors.New("cannot prepare the upload")
		}
		req.Header.Set("Authorization", "OAuth "+a.AccessToken)
		req.Header.Set("Content-Type", mediaType)
		req.Header.Set("file_offset", strconv.Itoa(offset))
		result, err := s.request(req)
		if err != nil {
			return "", err
		}
		if handle, _ := result["h"].(string); strings.TrimSpace(handle) != "" {
			if len(handle) > 8192 || strings.ContainsAny(handle, "\r\n") {
				return "", errors.New("provider returned an invalid upload handle")
			}
			return handle, nil
		}
		next, ok := uploadOffset(result["file_offset"])
		if !ok || next <= offset || next >= len(raw) {
			keys := make([]string, 0, len(result))
			for key := range result {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			return "", fmt.Errorf("provider did not return an upload handle (fields %v, file_offset %v of %d bytes)", keys, result["file_offset"], len(raw))
		}
		offset = next
	}
	return "", errors.New("provider did not finish the upload")
}

// uploadOffset reads Meta's file_offset, which arrives as a JSON number or a numeric string.
func uploadOffset(value any) (int, bool) {
	switch v := value.(type) {
	case float64:
		return int(v), v >= 0
	case string:
		n, err := strconv.Atoi(v)
		return n, err == nil && n >= 0
	}
	return 0, false
}
