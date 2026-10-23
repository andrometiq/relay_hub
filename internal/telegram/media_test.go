package telegram

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func stickerFixture(t *testing.T, extension string) []byte {
	t.Helper()
	switch extension {
	case ".webp":
		data, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
		if err != nil {
			t.Fatal(err)
		}
		return data
	case ".webm":
		return []byte{0x1a, 0x45, 0xdf, 0xa3, 0x87, 0x42, 0x82, 0x84, 'w', 'e', 'b', 'm'}
	case ".tgs":
		var b bytes.Buffer
		zw := gzip.NewWriter(&b)
		_, _ = zw.Write([]byte(`{"v":"5.5.2","w":512,"h":512,"layers":[]}`))
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	t.Fatal("unknown fixture format")
	return nil
}
func stickerSimulationClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := NewWithHTTPClient(Config{AccountID: "sticker-account", BotToken: groupSimulationToken, Enabled: true}, &http.Client{Transport: simulationTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "api.telegram.org" {
			t.Errorf("request left fixed Telegram origin %s", r.URL.Host)
		}
		if !strings.HasPrefix(r.URL.Path, "/bot"+groupSimulationToken+"/") && !strings.HasPrefix(r.URL.Path, "/file/bot"+groupSimulationToken+"/") {
			t.Error("incorrect bot account path")
		}
		clone := r.Clone(r.Context())
		u := *r.URL
		clone.URL = &u
		clone.URL.Scheme = "http"
		clone.URL.Host = strings.TrimPrefix(server.URL, "http://")
		return http.DefaultTransport.RoundTrip(clone)
	})})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func TestStickerDownloadContractAndBytes(t *testing.T) {
	for ext, mime := range map[string]string{".webp": "image/webp", ".webm": "video/webm", ".tgs": "application/x-tgsticker"} {
		t.Run(ext, func(t *testing.T) {
			data := stickerFixture(t, ext)
			calls := 0
			c := stickerSimulationClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if strings.HasSuffix(r.URL.Path, "/getFile") {
					if r.Method != "POST" {
						t.Error("getFile not POST")
					}
					var body map[string]string
					_ = json.NewDecoder(r.Body).Decode(&body)
					if !reflect.DeepEqual(body, map[string]string{"file_id": "CAAC_simulation"}) {
						t.Error("invalid file lookup payload")
					}
					fmt.Fprintf(w, `{"ok":true,"result":{"file_id":"CAAC_simulation","file_size":%d,"file_path":%q}}`, len(data), "stickers/file_17"+ext)
					return
				}
				if r.URL.Path != "/file/bot"+groupSimulationToken+"/stickers/file_17"+ext || r.Method != "GET" || r.Header.Get("Accept-Encoding") != "identity" {
					t.Error("invalid download request")
				}
				w.Header().Set("Content-Type", "application/octet-stream") // not trusted for rendering
				_, _ = w.Write(data)
			})
			file, err := c.FetchSticker(t.Context(), "CAAC_simulation")
			if err != nil || calls != 2 || file.ContentType != mime || file.Filename != "sticker"+ext || !bytes.Equal(file.Data, data) {
				t.Fatalf("wrong media result mime=%s filename=%s calls=%d err=%v", file.ContentType, file.Filename, calls, err)
			}
		})
	}
}
func TestStickerRejectsUnsafeMetadataBeforeDownload(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		size       int
		id         string
	}{
		{"traversal", "stickers/../private/file.webp", 100, "CAAC_simulation"}, {"absolute URL", "https://attacker.example/file.webp", 100, "CAAC_simulation"}, {"absolute path", "/stickers/file.webp", 100, "CAAC_simulation"}, {"encoded traversal", "stickers/%2e%2e/file.webp", 100, "CAAC_simulation"}, {"query", "stickers/file.webp?token=secret", 100, "CAAC_simulation"}, {"fragment", "stickers/file.webp#x", 100, "CAAC_simulation"}, {"double slash", "stickers//file.webp", 100, "CAAC_simulation"}, {"html", "stickers/file.html", 100, "CAAC_simulation"}, {"oversized", "stickers/file.webp", MaxStickerBytes + 1, "CAAC_simulation"}, {"wrong file ID", "stickers/file.webp", 100, "other_account_file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := stickerSimulationClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				fmt.Fprintf(w, `{"ok":true,"result":{"file_id":%q,"file_size":%d,"file_path":%q}}`, tc.id, tc.size, tc.path)
			})
			if _, err := c.FetchSticker(t.Context(), "CAAC_simulation"); err == nil || calls != 1 {
				t.Fatalf("unsafe metadata downloaded calls=%d error=%v", calls, err)
			}
		})
	}
}
func TestStickerRejectsDownloadFailuresAndContent(t *testing.T) {
	for _, tc := range []struct {
		name, ext, encoding string
		body                []byte
		status              int
	}{
		{"HTML response", ".webp", "", []byte("<script>alert(1)</script>"), 200},
		{"oversized body", ".webp", "", bytes.Repeat([]byte{'x'}, MaxStickerBytes+1), 200},
		{"encoding rewrite", ".webp", "gzip", stickerFixture(t, ".webp"), 200},
		{"wrong format", ".tgs", "", stickerFixture(t, ".webp"), 200},
		{"provider denied", ".webp", "", []byte(groupSimulationToken), 403},
		{"provider throttle", ".webp", "", []byte(groupSimulationToken), 429},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := stickerSimulationClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if strings.HasSuffix(r.URL.Path, "/getFile") {
					fmt.Fprintf(w, `{"ok":true,"result":{"file_id":"CAAC_simulation","file_path":%q}}`, "stickers/file_17"+tc.ext)
					return
				}
				if tc.encoding != "" {
					w.Header().Set("Content-Encoding", tc.encoding)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write(tc.body)
			})
			_, err := c.FetchSticker(t.Context(), "CAAC_simulation")
			if err == nil || strings.Contains(err.Error(), groupSimulationToken) || calls != 2 {
				t.Fatalf("unsafe media handling: %v calls=%d", err, calls)
			}
		})
	}
}
func TestStickerRejectsGzipExpansionAndSizeMismatch(t *testing.T) {
	var bomb bytes.Buffer
	zw := gzip.NewWriter(&bomb)
	_, _ = zw.Write(bytes.Repeat([]byte{' '}, MaxStickerBytes+1))
	_ = zw.Close()
	if _, err := stickerContentType(".tgs", bomb.Bytes()); err == nil {
		t.Fatal("unbounded TGS expansion accepted")
	}
	c := stickerSimulationClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getFile") {
			fmt.Fprint(w, `{"ok":true,"result":{"file_id":"CAAC_simulation","file_size":100,"file_path":"stickers/file.webp"}}`)
			return
		}
		_, _ = w.Write(stickerFixture(t, ".webp"))
	})
	if _, err := c.FetchSticker(t.Context(), "CAAC_simulation"); err == nil {
		t.Fatal("truncated media accepted")
	}
}
func TestStickerRedirectCannotExposeBotToken(t *testing.T) {
	escaped := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { escaped = true }))
	defer target.Close()
	c := stickerSimulationClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getFile") {
			fmt.Fprint(w, `{"ok":true,"result":{"file_id":"CAAC_simulation","file_path":"stickers/file.webp"}}`)
			return
		}
		w.Header().Set("Location", target.URL)
		w.WriteHeader(302)
	})
	if _, err := c.FetchSticker(t.Context(), "CAAC_simulation"); err == nil || escaped {
		t.Fatal("redirected media request")
	}
}
func TestStickerInvalidIDDisabledAndCancelled(t *testing.T) {
	c, _ := NewWithHTTPClient(Config{AccountID: "a", BotToken: groupSimulationToken, Enabled: true}, &http.Client{Transport: simulationTransport(func(r *http.Request) (*http.Response, error) {
		t.Fatal("invalid media input reached provider")
		return nil, nil
	})})
	for _, id := range []string{"", "https://example.org/file.webp", "../private", strings.Repeat("a", 257)} {
		if _, err := c.FetchSticker(t.Context(), id); err == nil {
			t.Fatal("invalid file ID accepted")
		}
	}
	c.cfg.Enabled = false
	if _, err := c.FetchSticker(t.Context(), "fileID"); err == nil {
		t.Fatal("disabled bot fetched media")
	}
	// The real HTTP transport enforces context cancellation before connecting.
	c, _ = New(Config{AccountID: "a", BotToken: groupSimulationToken, Enabled: true})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.FetchSticker(ctx, "fileID"); err == nil || strings.Contains(err.Error(), groupSimulationToken) {
		t.Fatal("cancelled file operation returned success or a secret")
	}
}

func TestAttachmentDownloadBoundaries(t *testing.T) {
	for _, path := range []string{"documents/file_1.pdf", "../private.pdf", "documents/../../private.pdf", "https://other.invalid/file.pdf"} {
		t.Run(path, func(t *testing.T) {
			downloads := 0
			c := stickerSimulationClient(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/getFile") {
					_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"file_id": "known", "file_path": path, "file_size": 9}})
					return
				}
				downloads++
				fmt.Fprint(w, "%PDF-1.7\n")
			})
			file, err := c.FetchMedia(t.Context(), "known")
			if path == "documents/file_1.pdf" {
				if err != nil || file.ContentType != "application/pdf" || downloads != 1 {
					t.Fatal("valid media rejected", err)
				}
			} else if err == nil || downloads != 0 {
				t.Fatal("unsafe download path accepted")
			}
		})
	}
}
