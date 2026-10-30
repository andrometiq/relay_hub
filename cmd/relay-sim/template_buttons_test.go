package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func buttonsTemplate(buttons string) string {
	return `{"name":"offer","language":"en_US","category":"MARKETING","components":[{"type":"BODY","text":"Hi"},{"type":"BUTTONS","buttons":[` + buttons + `]}]}`
}

func TestTemplateButtonRulesFollowMeta(t *testing.T) {
	const (
		reply = `{"type":"QUICK_REPLY","text":"Yes"}`
		link  = `{"type":"URL","text":"Open","url":"https://example.com/{{1}}","example":["a"]}`
		phone = `{"type":"PHONE_NUMBER","text":"Call","phone_number":"+919876543210"}`
		code  = `{"type":"COPY_CODE","example":"SAVE20"}`
	)
	cases := map[string]struct {
		buttons, problem string
	}{
		"quick replies before actions": {reply + "," + reply + "," + link + "," + phone + "," + code, ""},
		"quick replies after actions":  {link + "," + link + "," + reply, ""},
		"quick replies split":          {reply + "," + link + "," + reply, "grouped together"},
		"actions split":                {link + "," + reply + "," + link, "grouped together"},
		"third URL":                    {link + "," + link + "," + link, "at most 2 URL"},
		"second phone":                 {phone + "," + phone, "at most 1 PHONE_NUMBER"},
		"eleven buttons":               {strings.Repeat(reply+",", 10) + reply, "at most 10 buttons"},
		"copy code with array":         {`{"type":"COPY_CODE","example":["SAVE20"]}`, "one string example"},
		"copy code with text":          {`{"type":"COPY_CODE","text":"Copy","example":"SAVE20"}`, "no text"},
	}
	for name, test := range cases {
		var row map[string]any
		if err := json.Unmarshal([]byte(buttonsTemplate(test.buttons)), &row); err != nil {
			t.Fatal(name, err)
		}
		got := templateButtonProblem(row["components"])
		if (test.problem == "") != (got == "") || !strings.Contains(got, test.problem) {
			t.Errorf("%s: got %q want %q", name, got, test.problem)
		}
	}
}

func TestMetaRejectsInvalidButtonsOnCreateAndEdit(t *testing.T) {
	meta := &fakeMeta{records: newRecorder()}
	call := func(path, body string, want int) map[string]any {
		t.Helper()
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer sim-provider-token")
		w := httptest.NewRecorder()
		meta.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: status %d want %d: %s", path, w.Code, want, w.Body)
		}
		var result map[string]any
		json.Unmarshal(w.Body.Bytes(), &result)
		return result
	}
	interleaved := `{"type":"QUICK_REPLY","text":"A"},{"type":"URL","text":"Open","url":"https://example.com"},{"type":"QUICK_REPLY","text":"B"}`
	refused := call("/v25.0/220/message_templates", buttonsTemplate(interleaved), 400)
	if refused["error"].(map[string]any)["code"] != float64(100) {
		t.Fatal(refused)
	}

	grouped := `{"type":"QUICK_REPLY","text":"A"},{"type":"QUICK_REPLY","text":"B"},{"type":"URL","text":"Open","url":"https://example.com"}`
	id := call("/v25.0/220/message_templates", buttonsTemplate(grouped), 200)["id"].(string)
	call("/v25.0/"+id, `{"components":[{"type":"BUTTONS","buttons":[`+interleaved+`]}]}`, 400)
	call("/v25.0/"+id, `{"components":[{"type":"BODY","text":"Changed"}]}`, 200)
	if text := meta.templates[0]["components"].([]any)[0].(map[string]any)["text"]; text != "Changed" {
		t.Fatal("edit was not stored", text)
	}
}
