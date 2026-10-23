package telegram

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"strings"
	"testing"
)

func jpegFixture(t *testing.T, width, height int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewGray(image.Rect(0, 0, width, height)), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestGroupPhotoMultipartMatchesUpload(t *testing.T) {
	photo := jpegFixture(t, 10, 10)
	c := simulatedGroupClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/setChatPhoto") {
			t.Error("incorrect photo method")
		}
		reader, err := r.MultipartReader()
		if err != nil {
			t.Fatal(err)
		}
		parts := 0
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(part)
			if err != nil {
				t.Fatal(err)
			}
			parts++
			switch part.FormName() {
			case "chat_id":
				if string(raw) != "-1001234567890" {
					t.Error("wrong chat")
				}
			case "photo":
				if part.FileName() != "group.jpg" || part.Header.Get("Content-Type") != "image/jpeg" || !bytes.Equal(raw, photo) {
					t.Error("photo bytes altered")
				}
			default:
				t.Error("unexpected multipart field")
			}
		}
		if parts != 2 {
			t.Errorf("unexpected parts: %d", parts)
		}
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	})
	if _, err := c.ExecuteGroup(t.Context(), GroupInput{Operation: "photo.set", ChatID: -1001234567890, PhotoBase64: base64.StdEncoding.EncodeToString(photo)}); err != nil {
		t.Fatal(err)
	}
}
func TestGroupPhotoBoundsBeforeTransport(t *testing.T) {
	c, _ := NewWithHTTPClient(Config{AccountID: "simulation", BotToken: groupSimulationToken, Enabled: true}, &http.Client{Transport: simulationTransport(func(r *http.Request) (*http.Response, error) {
		t.Error("invalid image reached provider")
		return nil, nil
	})})
	for name, encoded := range map[string]string{
		"empty": "", "invalid base64": "###", "not jpeg": base64.StdEncoding.EncodeToString([]byte("not an image")), "above byte limit": strings.Repeat("A", base64.StdEncoding.EncodedLen(maxGroupPhotoBytes)+4), "above dimension limit": base64.StdEncoding.EncodeToString(jpegFixture(t, 2049, 1)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := c.ExecuteGroup(t.Context(), GroupInput{Operation: "photo.set", ChatID: -1001234567890, PhotoBase64: encoded}); err == nil {
				t.Fatal("invalid image accepted")
			}
		})
	}
}
