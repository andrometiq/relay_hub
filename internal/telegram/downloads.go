package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
)

var mediaPathRE = regexp.MustCompile(`^[A-Za-z0-9_/-]+\.[A-Za-z0-9]{1,10}$`)

// FetchMedia accepts only IDs checked against this account's event journal by
// the caller. Downloads use the fixed provider host, with redirects disabled.
func (c *Client) FetchMedia(ctx context.Context, fileID string) (StickerFile, error) {
	fail := errors.New("attachment is unavailable or exceeds 4 MiB")
	if !c.cfg.Enabled || !fileIDRE.MatchString(fileID) {
		return StickerFile{}, fail
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	raw, err := c.call(ctx, "getFile", map[string]any{"file_id": fileID})
	if err != nil {
		return StickerFile{}, fail
	}
	var file struct {
		ID   string `json:"file_id"`
		Path string `json:"file_path"`
		Size int64  `json:"file_size"`
	}
	if json.Unmarshal(raw, &file) != nil || file.ID != fileID || len(file.Path) > 512 || !mediaPathRE.MatchString(file.Path) || path.Clean(file.Path) != file.Path || strings.HasPrefix(file.Path, "/") || strings.Contains(file.Path, "//") || file.Size < 0 || file.Size > MaxMediaBytes {
		return StickerFile{}, fail
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.telegram.org/file/bot"+c.cfg.BotToken+"/"+file.Path, nil)
	if err != nil {
		return StickerFile{}, fail
	}
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := c.http.Do(req)
	if err != nil {
		return StickerFile{}, fail
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.ContentLength > MaxMediaBytes || (resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity") {
		return StickerFile{}, fail
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxMediaBytes+1))
	if err != nil || len(data) == 0 || len(data) > MaxMediaBytes || (file.Size > 0 && file.Size != int64(len(data))) {
		return StickerFile{}, fail
	}
	return StickerFile{Data: data, ContentType: http.DetectContentType(data), Filename: path.Base(file.Path)}, nil
}
