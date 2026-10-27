package whatsapp

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func jpegFixture(t *testing.T, width, height int) []byte {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			im.SetRGBA(x, y, color.RGBA{uint8(x), uint8(y), uint8(x + y), 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, im, &jpeg.Options{Quality: 70}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestGroupPhotoSimulation(t *testing.T) {
	raw := jpegFixture(t, 256, 256)
	uploads := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v25.0/111/groups" {
			fmt.Fprint(w, `{"data":{"groups":[{"id":"group-1"}]}}`)
			return
		}
		uploads++
		if r.Method != "POST" || r.URL.Path != "/v25.0/group-1" || r.Header.Get("Authorization") != "Bearer provider-secret" {
			t.Error("invalid photo route/auth")
		}
		reader, err := r.MultipartReader()
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		fields := map[string][]byte{}
		for {
			part, e := reader.NextPart()
			if e == io.EOF {
				break
			}
			if e != nil {
				t.Error(e)
				break
			}
			data, _ := io.ReadAll(part)
			fields[part.FormName()] = data
			if part.FormName() == "file" && (part.FileName() != "group.jpg" || part.Header.Get("Content-Type") != "image/jpeg") {
				t.Error("photo MIME/filename invalid")
			}
		}
		if len(fields) != 2 || string(fields["messaging_product"]) != "whatsapp" || !bytes.Equal(fields["file"], raw) {
			t.Error("uploaded image changed")
		}
		fmt.Fprint(w, `{"request_id":"photo-request"}`)
	}))
	defer provider.Close()
	s, _ := New([]Account{testAccount()})
	s.base = provider.URL
	in := Input{Operation: "groups.photo", ID: "group-1", PhotoBase64: base64.StdEncoding.EncodeToString(raw)}
	if _, err := s.Execute(context.Background(), testAccount(), in); err != nil {
		t.Fatal(err)
	}
	in.ID = "foreign-group"
	if _, err := s.Execute(context.Background(), testAccount(), in); err == nil {
		t.Fatal("foreign photo update accepted")
	}
	for _, tc := range []struct{ name, encoded string }{{"empty", ""}, {"malformed-base64", "not base64!"}, {"non-jpeg", base64.StdEncoding.EncodeToString([]byte("<svg>no</svg>"))}, {"truncated", base64.StdEncoding.EncodeToString(raw[:len(raw)/2])}, {"small", base64.StdEncoding.EncodeToString(jpegFixture(t, 191, 191))}, {"rectangular", base64.StdEncoding.EncodeToString(jpegFixture(t, 256, 192))}, {"excess-dimensions", base64.StdEncoding.EncodeToString(jpegFixture(t, 2049, 2049))}, {"oversize", strings.Repeat("A", base64.StdEncoding.EncodedLen(maxGroupPhoto)+4)}} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.execute(context.Background(), testAccount(), Input{Operation: "groups.photo", ID: "group-1", PhotoBase64: tc.encoded}, true); err == nil {
				t.Error("invalid photo accepted")
			}
		})
	}
	if uploads != 1 {
		t.Fatal("invalid image or foreign group reached provider")
	}
}
