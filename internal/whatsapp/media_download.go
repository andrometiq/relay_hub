package whatsapp

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

type MediaDownload struct {
	Data     []byte
	MIME     string
	Filename string
}

// MediaKnown restricts the sticker proxy to a media ID actually received by
// this account. A provider token may span multiple phones, so possession of an
// arbitrary numeric media ID is never enough to authorize the download.
func (p PostgresEvents) MediaKnown(ctx context.Context, account, id string) (bool, error) {
	c, err := pgx.Connect(ctx, p.URL)
	if err != nil {
		return false, err
	}
	defer c.Close(context.Background())
	var known bool
	err = c.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM relay_whatsapp_incoming WHERE account=$1 AND payload->>'type'='sticker' AND payload->'sticker'->>'id'=$2)`, account, id).Scan(&known)
	return known, err
}

func (p PostgresEvents) AttachmentKnown(ctx context.Context, account, id string) (bool, error) {
	c, err := pgx.Connect(ctx, p.URL)
	if err != nil {
		return false, err
	}
	defer c.Close(context.Background())
	var known bool
	err = c.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM relay_whatsapp_incoming WHERE account=$1 AND payload->>'type' IN ('image','audio','video','document','sticker') AND payload->(payload->>'type')->>'id'=$2)`, account, id).Scan(&known)
	return known, err
}

// DownloadMedia is the bounded incoming-sticker proxy. It never returns a
// provider URL/token and never accepts a URL supplied by a browser/client.
// https://developers.facebook.com/documentation/business-messaging/whatsapp/business-phone-numbers/media
// https://developers.facebook.com/documentation/business-messaging/whatsapp/reference/media/media-api
func (s *Service) DownloadMedia(ctx context.Context, a Account, id string, maxBytes int64) (MediaDownload, error) {
	if maxBytes > 2<<20 {
		return MediaDownload{}, errors.New("sticker exceeds the 2 MiB limit")
	}
	return s.downloadMedia(ctx, a, id, maxBytes, true)
}

func (s *Service) DownloadAttachment(ctx context.Context, a Account, id string) (MediaDownload, error) {
	return s.downloadMedia(ctx, a, id, 4<<20, false)
}

func (s *Service) downloadMedia(ctx context.Context, a Account, id string, maxBytes int64, stickerOnly bool) (MediaDownload, error) {
	var empty MediaDownload
	if !digits.MatchString(id) || maxBytes < 1 || maxBytes > 4<<20 {
		return empty, errors.New("valid media ID and a limit up to 4 MiB are required")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		return empty, ErrBusy
	}
	var known bool
	var err error
	if stickerOnly {
		store, ok := s.Events.(interface {
			MediaKnown(context.Context, string, string) (bool, error)
		})
		if !ok {
			return empty, errors.New("media ownership verification is unavailable")
		}
		known, err = store.MediaKnown(ctx, a.ID, id)
	} else {
		store, ok := s.Events.(interface {
			AttachmentKnown(context.Context, string, string) (bool, error)
		})
		if !ok {
			return empty, errors.New("media ownership verification is unavailable")
		}
		known, err = store.AttachmentKnown(ctx, a.ID, id)
	}
	if err != nil || !known {
		return empty, errors.New("media does not belong to this account")
	}
	metadata, err := s.graph(ctx, a, http.MethodGet, id, url.Values{"phone_number_id": {a.PhoneID}}, nil)
	if err != nil {
		return empty, err
	}
	rawURL, _ := metadata["url"].(string)
	endpoint, err := url.Parse(rawURL)
	// Keep the provider bearer credential on the specific Meta media host only.
	// Redirects are also refused by the service HTTP client.
	if err != nil || endpoint.Scheme != "https" || endpoint.Host != "lookaside.fbsbx.com" || endpoint.User != nil || endpoint.Fragment != "" || endpoint.Path != "/whatsapp_business/attachments/" {
		return empty, errors.New("untrusted media download destination")
	}
	metadataID, _ := metadata["id"].(string)
	if metadataID != id {
		return empty, errors.New("media identifier mismatch")
	}
	mimeType, _ := metadata["mime_type"].(string)
	if stickerOnly && mimeType != "image/webp" {
		return empty, errors.New("this proxy supports WhatsApp stickers only")
	}
	if _, _, err := mime.ParseMediaType(mimeType); err != nil {
		return empty, errors.New("invalid media content type")
	}
	expectedSize := int64(-1)
	if value, exists := metadata["file_size"]; exists {
		switch v := value.(type) {
		case string:
			expectedSize, err = strconv.ParseInt(v, 10, 64)
		case float64:
			expectedSize = int64(v)
			if float64(expectedSize) != v {
				err = errors.New("invalid size")
			}
		default:
			err = errors.New("invalid size")
		}
		if err != nil || expectedSize < 1 || expectedSize > maxBytes {
			return empty, errors.New("media size exceeds the proxy limit or is invalid")
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return empty, errors.New("cannot build media request")
	}
	req.Header.Set("Authorization", "Bearer "+a.AccessToken)
	res, err := s.client.Do(req)
	if err != nil {
		return empty, errors.New("media download unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return empty, &ProviderError{Status: res.StatusCode}
	}
	if res.ContentLength > maxBytes {
		return empty, errors.New("media size exceeds the proxy limit")
	}
	responseMIME, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil || responseMIME != mimeType {
		return empty, errors.New("media content type mismatch")
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxBytes+1))
	if err != nil || len(data) == 0 || int64(len(data)) > maxBytes || (expectedSize >= 0 && int64(len(data)) != expectedSize) {
		return empty, errors.New("invalid media response size")
	}
	if stickerOnly && (len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP") {
		return empty, errors.New("invalid WebP sticker")
	}
	if value, exists := metadata["sha256"]; exists {
		digest, ok := value.(string)
		if !ok {
			return empty, errors.New("invalid media digest")
		}
		expected, e := hex.DecodeString(digest)
		if e != nil || len(expected) != sha256.Size {
			expected, e = base64.StdEncoding.DecodeString(digest)
		}
		actual := sha256.Sum256(data)
		if e != nil || len(expected) != sha256.Size || subtle.ConstantTimeCompare(expected, actual[:]) != 1 {
			return empty, errors.New("media integrity verification failed")
		}
	}
	filename := "attachment.bin"
	if stickerOnly {
		filename = "sticker.webp"
	} else if extensions, _ := mime.ExtensionsByType(mimeType); len(extensions) > 0 {
		filename = "attachment" + extensions[0]
	}
	return MediaDownload{Data: data, MIME: mimeType, Filename: filename}, nil
}
