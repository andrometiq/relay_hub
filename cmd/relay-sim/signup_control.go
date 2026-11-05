package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

var esNameStatuses = map[string]bool{"APPROVED": true, "DECLINED": true, "PENDING_REVIEW": true, "EXPIRED": true}

// signupControl serves the /_sim/es/ test control routes; the caller checked
// the control token.
func (s *simulation) signupControl(w http.ResponseWriter, r *http.Request) {
	es := s.meta.signup
	if r.Method == "GET" && r.URL.Path == "/_sim/es/state" {
		writeJSON(w, es.snapshot())
		return
	}
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	if r.URL.Path == "/_sim/es/media" {
		es.mediaControl(w, r)
		return
	}
	var in struct {
		ID      string `json:"id"`
		WABAID  string `json:"waba_id"`
		PhoneID string `json:"phone_id"`
		Ready   bool   `json:"ready"`
		To      string `json:"to"`
		Text    string `json:"text"`
		Status  string `json:"status"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in) != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	if r.URL.Path == "/_sim/es/template" {
		s.templateControl(w, r, in.ID, in.Status)
		return
	}
	payload, status, problem := es.control(r.URL.Path, in.WABAID, in.PhoneID, in.Ready, in.To, in.Text, in.Status)
	if problem != "" {
		http.Error(w, problem, status)
		return
	}
	if payload == nil {
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	s.emit(w, r, payload, false)
}

// control changes the fake Meta state for one control route and returns the
// webhook that Meta would send for the change.
func (es *signupState) control(path, waba, phone string, ready bool, to, text, status string) ([]byte, int, string) {
	es.mu.Lock()
	defer es.mu.Unlock()
	var payload []byte
	switch path {
	case "/_sim/es/payment":
		account := es.WABAs[waba]
		if account == nil {
			return nil, 404, "unknown WABA"
		}
		account.Funding = ready
	case "/_sim/es/partner-removed":
		account := es.WABAs[waba]
		if account == nil {
			return nil, 404, "unknown WABA"
		}
		account.Subscribed = false
		payload = accountUpdate(account, "PARTNER_REMOVED")
	case "/_sim/es/echo":
		number := es.Phones[phone]
		if number == nil || !numberPattern.MatchString(to) || text == "" {
			return nil, 400, "echo needs a known phone_id, to and text"
		}
		payload = messageEcho(number, to, text, es.now())
	case "/_sim/es/name-status":
		number := es.Phones[phone]
		if number == nil || !esNameStatuses[status] {
			return nil, 400, "name status needs a known phone_id and a Meta name status"
		}
		number.NameStatus = status
		payload = nameUpdate(number)
	default:
		return nil, 404, "not found"
	}
	es.call(strings.TrimPrefix(path, "/_sim/es/"), waba+phone, "")
	if es.save() != nil {
		return nil, 503, "simulation state unavailable"
	}
	return payload, 200, ""
}

var esTemplateStatuses = map[string]bool{"APPROVED": true, "REJECTED": true, "PAUSED": true, "DISABLED": true}

// templateControl sets the review result of a signup template and sends
// Meta's status webhook for it.
func (s *simulation) templateControl(w http.ResponseWriter, r *http.Request, id, status string) {
	es := s.meta.signup
	if !esTemplateStatuses[status] {
		http.Error(w, "template status needs a Meta review status", 400)
		return
	}
	es.mu.Lock()
	payload, ok := es.templateStatus(id, status)
	saved := ok && es.save() == nil
	es.mu.Unlock()
	if !ok {
		http.Error(w, "unknown template", 404)
		return
	}
	if !saved {
		http.Error(w, "simulation state unavailable", 503)
		return
	}
	s.emit(w, r, payload, false)
}
