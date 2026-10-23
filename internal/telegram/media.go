package telegram

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
)

const MaxStickerBytes = 2 * 1024 * 1024

type StickerFile struct {
	Data        []byte
	ContentType string
	Filename    string
}

var telegramFilePathRE = regexp.MustCompile(`^[A-Za-z0-9_/-]+\.(webp|tgs|webm)$`)

// FetchSticker resolves a provider file_id on this account's bot. Callers must
// first authorize the file_id against a message visible to the requesting user.
// Neither bot-bearing URLs nor file paths are returned to clients.
func (c *Client) FetchSticker(ctx context.Context, fileID string) (StickerFile, error) {
	if !c.cfg.Enabled {
		return StickerFile{}, errors.New("Telegram account is disabled")
	}
	if !fileIDRE.MatchString(fileID) {
		return StickerFile{}, errors.New("invalid Telegram sticker file_id")
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	raw, err := c.call(ctx, "getFile", map[string]any{"file_id": fileID})
	if err != nil {
		return StickerFile{}, err
	}
	var file struct {
		ID   string `json:"file_id"`
		Path string `json:"file_path"`
		Size *int64 `json:"file_size"`
	}
	if json.Unmarshal(raw, &file) != nil || file.ID != fileID || len(file.Path) > 512 || !telegramFilePathRE.MatchString(file.Path) || path.Clean(file.Path) != file.Path || strings.HasPrefix(file.Path, "/") || strings.Contains(file.Path, "//") {
		return StickerFile{}, errors.New("invalid Telegram sticker file response")
	}
	if file.Size != nil && (*file.Size < 1 || *file.Size > MaxStickerBytes) {
		return StickerFile{}, errors.New("Telegram sticker exceeds the 2 MiB download limit")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.telegram.org/file/bot"+c.cfg.BotToken+"/"+file.Path, nil)
	if err != nil {
		return StickerFile{}, errors.New("invalid Telegram sticker download request")
	}
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := c.http.Do(req)
	if err != nil {
		return StickerFile{}, errors.New("Telegram sticker download failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength > MaxStickerBytes || (resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity") {
		return StickerFile{}, errors.New("Telegram sticker download rejected")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxStickerBytes+1))
	if err != nil || len(data) == 0 || len(data) > MaxStickerBytes || (file.Size != nil && *file.Size != int64(len(data))) {
		return StickerFile{}, errors.New("invalid or oversized Telegram sticker download")
	}
	extension := path.Ext(file.Path)
	contentType, err := stickerContentType(extension, data)
	if err != nil {
		return StickerFile{}, err
	}
	return StickerFile{Data: data, ContentType: contentType, Filename: "sticker" + extension}, nil
}
func stickerContentType(extension string, data []byte) (string, error) {
	invalid := errors.New("Telegram sticker content does not match its format")
	switch extension {
	case ".webp":
		if len(data) < 20 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" || uint64(binary.LittleEndian.Uint32(data[4:8]))+8 != uint64(len(data)) {
			return "", invalid
		}
		switch string(data[12:16]) {
		case "VP8 ", "VP8L", "VP8X":
		default:
			return "", invalid
		}
		if uint64(binary.LittleEndian.Uint32(data[16:20])) > uint64(len(data)-20) {
			return "", invalid
		}
		return "image/webp", nil
	case ".webm":
		// Check the EBML signature and declared WebM document type; do not decode
		// video on the Hub. Browsers still validate the complete media stream.
		if len(data) < 12 || !bytes.Equal(data[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}) || !bytes.Contains(data[:min(len(data), 256)], []byte{0x42, 0x82, 0x84, 'w', 'e', 'b', 'm'}) {
			return "", invalid
		}
		return "video/webm", nil
	case ".tgs":
		reader, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return "", invalid
		}
		defer reader.Close()
		reader.Multistream(false)
		decoded, err := io.ReadAll(io.LimitReader(reader, MaxStickerBytes+1))
		if err != nil || len(decoded) > MaxStickerBytes {
			return "", invalid
		}
		var animation struct {
			Version string            `json:"v"`
			Width   int               `json:"w"`
			Height  int               `json:"h"`
			Layers  []json.RawMessage `json:"layers"`
		}
		if json.Unmarshal(decoded, &animation) != nil || animation.Version == "" || len(animation.Version) > 32 || animation.Width < 1 || animation.Width > 512 || animation.Height < 1 || animation.Height > 512 || animation.Layers == nil {
			return "", invalid
		}
		return "application/x-tgsticker", nil
	}
	return "", invalid
}
