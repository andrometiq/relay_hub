package whatsapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type mediaTestTransport func(*http.Request) (*http.Response, error)

func (f mediaTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type mediaTestStore struct {
	allow bool
	fail  bool
}

func (f mediaTestStore) Save(context.Context, string, []byte) error           { return nil }
func (f mediaTestStore) List(context.Context, string, int64) ([]Event, error) { return nil, nil }
func (f mediaTestStore) MediaKnown(_ context.Context, account, id string) (bool, error) {
	if f.fail {
		return false, errors.New("db failure")
	}
	return f.allow && account == "site-a" && id == "123", nil
}
func TestStickerDownloadSimulation(t *testing.T) {
	sticker, _ := base64.StdEncoding.DecodeString("UklGRhwAAABXRUJQVlA4TBAAAAAvD8ADAAdQwOh//wMR0f8A")
	sum := sha256.Sum256(sticker)
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
		body   []byte
		mime   string
		status int
		denied bool
	}{
		{name: "valid-hex-digest"},
		{name: "valid-base64-digest", mutate: func(m map[string]any) { m["sha256"] = base64.StdEncoding.EncodeToString(sum[:]) }},
		{name: "foreign-url", mutate: func(m map[string]any) { m["url"] = "https://evil.invalid/whatsapp_business/attachments/" }, denied: true},
		{name: "host-suffix", mutate: func(m map[string]any) {
			m["url"] = "https://lookaside.fbsbx.com.evil.invalid/whatsapp_business/attachments/"
		}, denied: true},
		{name: "embedded-user", mutate: func(m map[string]any) { m["url"] = "https://secret@lookaside.fbsbx.com/whatsapp_business/attachments/" }, denied: true},
		{name: "port", mutate: func(m map[string]any) { m["url"] = "https://lookaside.fbsbx.com:8443/whatsapp_business/attachments/" }, denied: true},
		{name: "path-traversal", mutate: func(m map[string]any) {
			m["url"] = "https://lookaside.fbsbx.com/whatsapp_business/attachments/../other"
		}, denied: true},
		{name: "plain-http", mutate: func(m map[string]any) { m["url"] = "http://lookaside.fbsbx.com/whatsapp_business/attachments/" }, denied: true},
		{name: "metadata-id-mismatch", mutate: func(m map[string]any) { m["id"] = "456" }, denied: true},
		{name: "metadata-type-mismatch", mutate: func(m map[string]any) { m["mime_type"] = "image/svg+xml" }, denied: true},
		{name: "oversized-metadata", mutate: func(m map[string]any) { m["file_size"] = float64(3 << 20) }, denied: true},
		{name: "download-size-mismatch", mutate: func(m map[string]any) { m["file_size"] = fmt.Sprint(len(sticker) + 1) }, denied: true},
		{name: "metadata-hash-mismatch", mutate: func(m map[string]any) { m["sha256"] = strings.Repeat("0", 64) }, denied: true},
		{name: "missing-size-bounded", mutate: func(m map[string]any) { delete(m, "file_size") }},
		{name: "chunked-overflow", mutate: func(m map[string]any) { delete(m, "file_size") }, body: bytes.Repeat([]byte("x"), (2<<20)+1), denied: true},
		{name: "response-type-mismatch", mime: "text/html", denied: true},
		{name: "fake-webp", body: bytes.Repeat([]byte("x"), len(sticker)), denied: true},
		{name: "redirect-refused", status: 302, denied: true},
		{name: "expired-url", status: 404, denied: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metadata := map[string]any{"id": "123", "url": "https://lookaside.fbsbx.com/whatsapp_business/attachments/?mid=123", "mime_type": "image/webp", "file_size": fmt.Sprint(len(sticker)), "sha256": hex.EncodeToString(sum[:])}
			if tc.mutate != nil {
				tc.mutate(metadata)
			}
			metadataCalls, downloads := 0, 0
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer provider-secret" {
					t.Error("provider auth missing")
				}
				if r.URL.Path == "/v25.0/123" {
					metadataCalls++
					if r.URL.Query().Get("phone_number_id") != "111" {
						t.Error("unscoped metadata request")
					}
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(metadata)
					return
				}
				if r.URL.Path != "/whatsapp_business/attachments/" {
					t.Error("unexpected download path")
					w.WriteHeader(400)
					return
				}
				downloads++
				if tc.status == 302 {
					w.Header().Set("Location", "https://evil.invalid/steal")
					w.WriteHeader(302)
					return
				}
				if tc.status > 0 {
					w.WriteHeader(tc.status)
					fmt.Fprint(w, "private-token-provider-error")
					return
				}
				kind := tc.mime
				if kind == "" {
					kind = "image/webp"
				}
				w.Header().Set("Content-Type", kind)
				w.(http.Flusher).Flush()
				body := tc.body
				if body == nil {
					body = sticker
				}
				w.Write(body)
			}))
			defer provider.Close()
			target, _ := url.Parse(provider.URL)
			transport := http.DefaultTransport.(*http.Transport).Clone()
			defer transport.CloseIdleConnections()
			s, err := NewWithHTTPClient([]Account{testAccount()}, &http.Client{Transport: mediaTestTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "graph.facebook.com" && r.URL.Host != "lookaside.fbsbx.com" {
					t.Error("unexpected destination attempted")
					return nil, errors.New("forbidden destination")
				}
				clone := r.Clone(r.Context())
				clone.URL.Scheme, clone.URL.Host = target.Scheme, target.Host
				clone.Host = target.Host
				return transport.RoundTrip(clone)
			})})
			if err != nil {
				t.Fatal(err)
			}
			s.Events = mediaTestStore{allow: true}
			got, err := s.DownloadMedia(context.Background(), testAccount(), "123", 2<<20)
			if (err != nil) != tc.denied {
				t.Fatalf("error=%v denied=%v", err, tc.denied)
			}
			if err != nil {
				if strings.Contains(err.Error(), "provider-secret") || strings.Contains(err.Error(), "private-token") {
					t.Fatal("secret in error")
				}
			} else if !bytes.Equal(got.Data, sticker) || got.MIME != "image/webp" || got.Filename != "sticker.webp" {
				t.Fatal("sticker bytes or metadata changed")
			}
			if metadataCalls != 1 || downloads > 1 {
				t.Fatal("media request repeated")
			}
		})
	}
}
func TestStickerDownloadRequiresAccountEvidence(t *testing.T) {
	calls := 0
	s, _ := NewWithHTTPClient([]Account{testAccount()}, &http.Client{Transport: mediaTestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader(`{}`)), Header: http.Header{}}, nil
	})})
	for _, store := range []EventStore{nil, mediaTestStore{}, mediaTestStore{allow: true, fail: true}, mediaTestStore{allow: true}} {
		s.Events = store
		a := testAccount()
		if store == (mediaTestStore{allow: true}) {
			a.ID = "foreign"
		}
		if _, err := s.DownloadMedia(context.Background(), a, "123", 2<<20); err == nil {
			t.Fatal("unowned media accepted")
		}
	}
	s.Events = mediaTestStore{allow: true}
	for _, id := range []string{"../123", "https://evil.invalid/file", ""} {
		if _, err := s.DownloadMedia(context.Background(), testAccount(), id, 2<<20); err == nil {
			t.Fatal("invalid media accepted")
		}
	}
	if calls != 0 {
		t.Fatal("ownership failure still contacted provider")
	}
}

func (f mediaTestStore) AttachmentKnown(ctx context.Context, account, id string) (bool, error) {
	return f.MediaKnown(ctx, account, id)
}

func TestAttachmentDownloadSimulation(t *testing.T) {
	for _, tc := range []struct {
		name, mime string
		size       int
		allowed    bool
		denied     bool
	}{
		{"image", "image/png", 1024, true, false}, {"voice", "audio/ogg", 1024, true, false},
		{"document", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", 7 << 20, true, false}, {"too-large", "application/pdf", MaxDownloadBytes + 1, true, true},
		{"unknown-account", "image/png", 1024, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := bytes.Repeat([]byte("x"), tc.size)
			sum := sha256.Sum256(raw)
			s, err := NewWithHTTPClient([]Account{testAccount()}, &http.Client{Transport: mediaTestTransport(func(r *http.Request) (*http.Response, error) {
				var body []byte
				mimeType := tc.mime
				if r.URL.Host == "graph.facebook.com" {
					body, _ = json.Marshal(map[string]any{"id": "123", "url": "https://lookaside.fbsbx.com/whatsapp_business/attachments/?mid=123", "mime_type": tc.mime, "file_size": tc.size, "sha256": hex.EncodeToString(sum[:])})
					mimeType = "application/json"
				} else if r.URL.Host == "lookaside.fbsbx.com" {
					body = raw
				} else {
					t.Fatal("unexpected host")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {mimeType}}, Body: io.NopCloser(bytes.NewReader(body))}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			s.Events = mediaTestStore{allow: tc.allowed}
			file, err := s.DownloadAttachment(context.Background(), testAccount(), "123")
			if tc.denied {
				if err == nil {
					t.Fatal("invalid attachment accepted")
				}
				return
			}
			if err != nil || !bytes.Equal(file.Data, raw) || file.MIME != tc.mime {
				t.Fatalf("attachment: %v", err)
			}
		})
	}
}

func TestMediaFailureChecks(t *testing.T) {
	for _, tc := range []struct {
		name, check string
		store       EventStore
		busy        bool
		status      int
	}{
		{name: "ownership-store-missing", check: "ownership_verification"},
		{name: "ownership-store-error", check: "ownership_verification", store: mediaTestStore{fail: true}},
		{name: "not-owned", check: "ownership", store: mediaTestStore{}},
		{name: "capacity", check: "capacity", store: mediaTestStore{allow: true}, busy: true},
		{name: "provider-permission", check: "provider_metadata", store: mediaTestStore{allow: true}, status: 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			s, _ := NewWithHTTPClient([]Account{testAccount()}, &http.Client{Transport: mediaTestTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"private-provider-error"}}`))}, nil
			})})
			s.Events = tc.store
			if tc.busy {
				for i := 0; i < cap(s.slots); i++ {
					s.slots <- struct{}{}
				}
			}
			_, err := s.DownloadAttachment(context.Background(), testAccount(), "123")
			var rejected *MediaError
			if !errors.As(err, &rejected) || rejected.Check != tc.check {
				t.Fatalf("wrong rejection: %v", err)
			}
			if strings.Contains(err.Error(), "private-provider-error") {
				t.Fatal("provider body leaked")
			}
			if tc.status == 0 && calls != 0 {
				t.Fatal("local denial contacted provider")
			}
		})
	}
}
