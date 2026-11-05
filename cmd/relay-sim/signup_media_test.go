package main

import (
	"bytes"
	"encoding/base64"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"strings"
	"testing"
)

var pngPixel, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGNgYGBgAAAABQABpfZFQAAAAABJRU5ErkJggg==")

// uploadMedia posts one file as the Hub's media upload does.
func (h *esHarness) uploadMedia(phone, token string, want int) string {
	h.t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	form.WriteField("messaging_product", "whatsapp")
	part, _ := form.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="file"; filename="swatch.png"`}, "Content-Type": {"image/png"}})
	part.Write(pngPixel)
	form.Close()
	r := httptest.NewRequest("POST", "/v25.0/"+phone+"/media", &body)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", form.FormDataContentType())
	w := httptest.NewRecorder()
	h.meta.ServeHTTP(w, r)
	if w.Code != want {
		h.t.Fatalf("upload: status %d want %d: %s", w.Code, want, w.Body.String())
	}
	return w.Body.String()
}

func TestSignupMediaUploadDownloadAndReads(t *testing.T) {
	h := newESHarness(t)
	token, data := h.signupNumber(esDialogInput{Scenario: "new", Number: "919876500001", OTP: esOTP, DisplayName: "Acme"})
	other, _ := h.signupNumber(esDialogInput{Scenario: "new", Number: "919876500002", OTP: esOTP, DisplayName: "Other"})
	phone := data["phone_number_id"].(string)
	if body := h.uploadMedia(phone, token, 200); !strings.Contains(body, `"id":"900000000000`) {
		t.Fatal(body)
	}
	id := ""
	for key := range h.signup.media {
		id = key
	}
	info := h.graph("GET", "/v25.0/"+id+"?phone_number_id="+phone, token, "", 200)
	if info["mime_type"] != "image/png" || info["file_size"] != float64(len(pngPixel)) || info["url"] != esMediaURL+id {
		t.Fatal(info)
	}
	h.graph("GET", "/v25.0/"+id, other, "", 400)

	link, _ := url.Parse(info["url"].(string))
	for _, c := range []struct {
		token string
		want  int
	}{{token, 200}, {other, 404}} {
		r := httptest.NewRequest("GET", link.RequestURI(), nil)
		r.Header.Set("Authorization", "Bearer "+c.token)
		w := httptest.NewRecorder()
		h.signup.attachment(w, r)
		if w.Code != c.want || c.want == 200 && (!bytes.Equal(w.Body.Bytes(), pngPixel) || w.Header().Get("Content-Type") != "image/png") {
			t.Fatal("attachment download", c.want, w.Code)
		}
	}

	read := h.graph("POST", "/v25.0/"+phone+"/messages", token, `{"messaging_product":"whatsapp","status":"read","message_id":"wamid.in.1"}`, 200)
	if read["success"] != true {
		t.Fatal(read)
	}
	h.graph("POST", "/v25.0/"+phone+"/messages", token, `{"messaging_product":"whatsapp","status":"read"}`, 400)
}

func TestSignupResumableUpload(t *testing.T) {
	h := newESHarness(t)
	token, _ := h.signupNumber(esDialogInput{Scenario: "new", Number: "919876500001", OTP: esOTP, DisplayName: "Acme"})
	session := h.graph("POST", "/v25.0/"+esAppID+"/uploads?file_length=70&file_type=image/png&file_name=profile", token, "", 200)["id"].(string)
	h.graph("POST", "/v25.0/"+esAppID+"/uploads?file_type=image/png", token, "", 400)
	r := httptest.NewRequest("POST", "/v25.0/"+session, bytes.NewReader(pngPixel))
	r.Header.Set("Authorization", "OAuth "+token)
	r.Header.Set("file_offset", "0")
	w := httptest.NewRecorder()
	h.meta.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"h":"sim-handle-`) {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestSignupTemplateReviewControl(t *testing.T) {
	s, h, hub := newSignupSimulation(t)
	token, data := h.signupNumber(esDialogInput{Scenario: "new", Number: "919876500001", OTP: esOTP, DisplayName: "Acme"})
	waba := data["waba_id"].(string)
	created := h.graph("POST", "/v25.0/"+waba+"/message_templates", token, `{"name":"acme_update","language":"en","category":"UTILITY","components":[{"type":"BODY","text":"Hi"}]}`, 200)
	id := created["id"].(string)
	controlCall(t, s, "POST", "/_sim/es/template", `{"id":"`+id+`","status":"PENDING"}`, 400)
	controlCall(t, s, "POST", "/_sim/es/template", `{"id":"404","status":"APPROVED"}`, 404)
	controlCall(t, s, "POST", "/_sim/es/template", `{"id":"`+id+`","status":"APPROVED"}`, 200)
	rows := h.graph("GET", "/v25.0/"+waba+"/message_templates?name=acme_update", token, "", 200)["data"].([]any)
	if rows[0].(map[string]any)["status"] != "APPROVED" {
		t.Fatal(rows)
	}
	received := hub.received()
	if len(received) != 1 || !strings.Contains(received[0], `"message_template_status_update"`) || !strings.Contains(received[0], `"id":"`+waba+`"`) {
		t.Fatal(received)
	}
	if out := controlCall(t, s, "POST", "/_sim/es/media", `{"phone_id":"`+data["phone_number_id"].(string)+`","mime_type":"image/png","content_base64":"`+base64.StdEncoding.EncodeToString(pngPixel)+`"}`, 200); !strings.Contains(out, `"id":"900000000000`) {
		t.Fatal(out)
	}
	controlCall(t, s, "POST", "/_sim/es/media", `{"phone_id":"999","mime_type":"image/png","content_base64":"aGk="}`, 404)
}
