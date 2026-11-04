package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
)

// esMedia is one media file of a signup number: an outgoing upload or an
// incoming file a test control seeded. Media lives in memory only.
type esMedia struct {
	Phone, MIME string
	Data        []byte
}

const esMediaURL = "https://lookaside.fbsbx.com/whatsapp_business/attachments/?mid="

// mediaRoute serves the Graph calls of signup media and the Resumable Upload
// API: a media ID, an upload session of the app, and the upload itself.
// Callers hold s.mu.
func (s *signupState) mediaRoute(r *http.Request, node, edge string, body []byte) (int, any, bool) {
	token := s.Tokens[bearer(r)]
	switch {
	case s.media[node] != nil && r.Method == "GET" && edge == "":
		file := s.media[node]
		if token == nil || token.Phone != file.Phone {
			return 400, permissionError(), true
		}
		sum := sha256.Sum256(file.Data)
		return 200, map[string]any{"messaging_product": "whatsapp", "id": node, "url": esMediaURL + node, "mime_type": file.MIME,
			"sha256": hex.EncodeToString(sum[:]), "file_size": len(file.Data)}, true
	case node == esAppID && edge == "uploads" && r.Method == "POST":
		if token == nil || r.URL.Query().Get("file_length") == "" || r.URL.Query().Get("file_type") == "" {
			return 400, metaError(100, 0, "OAuthException", "(#100) Invalid parameter", false), true
		}
		return 200, map[string]any{"id": fmt.Sprintf("upload:sim%d", s.sequence("upload"))}, true
	case strings.HasPrefix(node, "upload:") && r.Method == "POST":
		if token == nil || len(body) == 0 || r.Header.Get("file_offset") != "0" {
			return 400, metaError(100, 0, "OAuthException", "(#100) Invalid parameter", false), true
		}
		return 200, map[string]any{"h": fmt.Sprintf("sim-handle-%d", s.sequence("handle"))}, true
	}
	return 0, nil, false
}

// upload is the media upload of a number; Meta answers with the media ID.
func (s *signupState) upload(r *http.Request, phone *esPhone, body []byte) (int, any) {
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || params["boundary"] == "" {
		return 400, metaError(100, 0, "OAuthException", "(#100) Invalid parameter", false)
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		part, err := reader.NextPart()
		if err != nil {
			return 400, metaError(100, 0, "OAuthException", "(#100) The parameter file is required", false)
		}
		if part.FormName() != "file" {
			continue
		}
		data, err := io.ReadAll(part)
		if err != nil || len(data) == 0 {
			return 400, metaError(100, 0, "OAuthException", "(#100) Invalid parameter", false)
		}
		return 200, map[string]any{"id": s.addMedia(phone.ID, part.Header.Get("Content-Type"), data)}
	}
}

// addMedia stores one file under a new numeric media ID; callers hold s.mu.
func (s *signupState) addMedia(phone, mimeType string, data []byte) string {
	id := fmt.Sprint(900000000000 + s.sequence("media"))
	s.media[id] = &esMedia{Phone: phone, MIME: mimeType, Data: data}
	return id
}

// attachment serves a media download from the fake lookaside host.
func (s *signupState) attachment(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	file := s.media[r.URL.Query().Get("mid")]
	token := s.Tokens[bearer(r)]
	s.mu.Unlock()
	if file == nil || token == nil || token.Phone != file.Phone {
		http.Error(w, "not found", 404)
		return
	}
	w.Header().Set("Content-Type", file.MIME)
	w.Write(file.Data)
}

// mediaControl seeds an incoming media file on a signup number and returns
// its media ID, for a test webhook to name.
func (s *signupState) mediaControl(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PhoneID       string `json:"phone_id"`
		MIMEType      string `json:"mime_type"`
		ContentBase64 string `json:"content_base64"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&in) != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	data, err := base64.StdEncoding.DecodeString(in.ContentBase64)
	if _, _, typeErr := mime.ParseMediaType(in.MIMEType); err != nil || typeErr != nil || len(data) == 0 {
		http.Error(w, "media needs a MIME type and base64 content", 400)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Phones[in.PhoneID] == nil {
		http.Error(w, "unknown phone_id", 404)
		return
	}
	writeJSON(w, map[string]any{"id": s.addMedia(in.PhoneID, in.MIMEType, data)})
}

// templateStatus sets the review result of a signup WABA template and returns
// the message_template_status_update webhook; callers hold s.mu.
func (s *signupState) templateStatus(id, status string) ([]byte, bool) {
	for _, waba := range s.WABAs {
		for _, row := range waba.Templates {
			if row["id"] == id {
				row["status"] = status
				return envelope(waba.ID, "message_template_status_update", map[string]any{"event": status, "message_template_id": id,
					"message_template_name": row["name"], "message_template_language": row["language"]}), true
			}
		}
	}
	return nil, false
}
