package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/andrometiq/relay_hub/internal/attachment"
	"io"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

const simulationBotToken = "123456:simulation_bot_token_local_only"
const simulationBotToken2 = "123457:simulation_bot_two_token_local_only"

// simulationBots maps each fake bot token to its username.
var simulationBots = map[string]string{simulationBotToken: "relay_sim_bot", simulationBotToken2: "relay_sim_bot_2"}

// botToken finds the fake bot token after prefix in path ("/bot" or "/file/bot").
func botToken(path, prefix string) (string, bool) {
	for token := range simulationBots {
		if strings.HasPrefix(path, prefix+token+"/") {
			return token, true
		}
	}
	return "", false
}

type telegramFile struct {
	Data []byte `json:"data"`
	Name string `json:"name"`
	Type string `json:"type"`
}
type telegramState struct {
	Next     int64                   `json:"next"`
	Messages []map[string]any        `json:"messages"`
	Updates  []json.RawMessage       `json:"updates"`
	Files    map[string]telegramFile `json:"files"`
	Faults   []string                `json:"faults"`
}
type fakeTelegram struct {
	state   *providerState[telegramState]
	records *recorder
}

func newFakeTelegram(path string, records *recorder) (*fakeTelegram, error) {
	state, err := openProviderState(path, telegramState{Files: map[string]telegramFile{}})
	if err == nil && (state.value.Files == nil || state.value.Next < 0) {
		err = errors.New("invalid Telegram provider state")
	}
	return &fakeTelegram{state: state, records: records}, err
}
func (t *fakeTelegram) faults(faults []string) error {
	for _, f := range faults {
		switch f {
		case "429", "503", "blocked", "migrate", "timeout_before", "timeout_after", "disconnect_before", "disconnect_after":
		default:
			return errors.New("invalid Telegram fault")
		}
	}
	return t.state.change(func(s *telegramState) error { s.Faults = faults; return nil })
}
func (t *fakeTelegram) queue(raw json.RawMessage) error {
	var canonical any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&canonical) != nil || !json.Valid(raw) {
		return errors.New("invalid update")
	}
	raw, _ = json.Marshal(canonical)
	var update struct {
		ID int64 `json:"update_id"`
	}
	if json.Unmarshal(raw, &update) != nil || update.ID < 1 {
		return errors.New("update_id required")
	}
	return t.state.change(func(s *telegramState) error {
		for _, old := range s.Updates {
			var previous struct {
				ID int64 `json:"update_id"`
			}
			_ = json.Unmarshal(old, &previous)
			if previous.ID == update.ID {
				if string(old) != string(raw) {
					return errors.New("conflicting update_id")
				}
				return nil
			}
		}
		s.Updates = append(s.Updates, raw)
		sort.Slice(s.Updates, func(i, j int) bool {
			var a, b struct {
				ID int64 `json:"update_id"`
			}
			_ = json.Unmarshal(s.Updates[i], &a)
			_ = json.Unmarshal(s.Updates[j], &b)
			return a.ID < b.ID
		})
		return nil
	})
}
func telegramBody(w http.ResponseWriter, r *http.Request) (map[string]any, map[string]telegramFile, error) {
	r.Body = http.MaxBytesReader(w, r.Body, attachment.MaxBytes+1<<20)
	body := map[string]any{}
	files := map[string]telegramFile{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		reader, err := r.MultipartReader()
		if err != nil {
			return nil, nil, err
		}
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, nil, err
			}
			raw, err := io.ReadAll(part)
			if err != nil {
				return nil, nil, err
			}
			if part.FileName() != "" {
				files[part.FormName()] = telegramFile{raw, part.FileName(), part.Header.Get("Content-Type")}
				body[part.FormName()] = map[string]any{"filename": part.FileName(), "size": len(raw)}
			} else {
				var value any
				if json.Unmarshal(raw, &value) != nil {
					value = string(raw)
				}
				body[part.FormName()] = value
			}
		}
	} else if r.Method == "GET" {
		for key, values := range r.URL.Query() {
			var value any
			if json.Unmarshal([]byte(values[0]), &value) != nil {
				value = values[0]
			}
			body[key] = value
		}
	} else if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return nil, nil, err
	}
	return body, files, nil
}
func (t *fakeTelegram) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	if token, ok := botToken(r.URL.Path, "/file/bot"); ok {
		t.download(w, r, token)
		return
	}
	token, ok := botToken(r.URL.Path, "/bot")
	if !ok {
		http.Error(w, "invalid fake bot token", 401)
		return
	}
	prefix := "/bot" + token + "/"
	if r.Method != "POST" && r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	method := strings.TrimPrefix(r.URL.Path, prefix)
	body, files, err := telegramBody(w, r)
	if err != nil {
		http.Error(w, "invalid Telegram body", 400)
		return
	}
	raw, _ := json.Marshal(body)
	status, fault := 200, ""
	var result any
	err = t.state.change(func(s *telegramState) error {
		if len(s.Faults) > 0 {
			fault = s.Faults[0]
			s.Faults = s.Faults[1:]
		}
		switch fault {
		case "429", "503", "blocked", "migrate":
			status = map[string]int{"429": 429, "503": 503, "blocked": 403, "migrate": 400}[fault]
			return nil
		case "timeout_before", "disconnect_before":
			return nil
		}
		switch method {
		case "getMe":
			id, _, _ := strings.Cut(token, ":")
			result = map[string]any{"id": number(id), "is_bot": true, "first_name": "Simulation", "username": simulationBots[token]}
		case "getUpdates":
			offset := number(body["offset"])
			pending := []json.RawMessage{}
			for _, update := range s.Updates {
				var row struct {
					ID int64 `json:"update_id"`
				}
				_ = json.Unmarshal(update, &row)
				if row.ID >= offset {
					pending = append(pending, update)
				}
			}
			s.Updates = pending
			limit := number(body["limit"])
			if limit < 1 || limit > 100 {
				limit = 100
			}
			result = pending[:min(len(pending), int(limit))]
		case "getFile":
			id, _ := body["file_id"].(string)
			file, ok := s.Files[id]
			if !ok {
				status = 400
				break
			}
			result = map[string]any{"file_id": id, "file_unique_id": id, "file_path": file.providerPath(id), "file_size": len(file.Data)}
		case "sendMessage", "sendPhoto", "sendVideo", "sendAnimation", "sendAudio", "sendVoice", "sendDocument", "sendSticker", "sendLocation", "sendContact", "setMessageReaction", "sendChatAction":
			chat := number(body["chat_id"])
			if chat == 0 {
				status = 400
				break
			}
			if method == "setMessageReaction" || method == "sendChatAction" {
				result = true
			} else {
				s.Next++
				receipt := map[string]any{"message_id": s.Next, "chat": map[string]any{"id": chat, "type": "private"}, "date": time.Now().Unix()}
				for key, value := range body {
					if key != "chat_id" {
						receipt[key] = value
					}
				}
				for key, file := range files {
					id := fmt.Sprintf("sim_file_%d_%s", s.Next, key)
					s.Files[id] = file
					value := map[string]any{"file_id": id, "file_unique_id": id, "file_size": len(file.Data), "file_name": file.Name, "mime_type": file.Type}
					if key == "photo" {
						receipt[key] = []any{value}
					} else {
						receipt[key] = value
					}
				}
				result = receipt
			}
			s.Messages = append(s.Messages, map[string]any{"bot": simulationBots[token], "method": method, "request": body, "result": result})
		default:
			status = 404
		}
		return nil
	})
	if err != nil {
		http.Error(w, "provider state unavailable", 503)
		return
	}
	envelope := map[string]any{"ok": true, "result": result}
	if status != 200 {
		envelope = map[string]any{"ok": false, "error_code": status, "description": "Simulation failure"}
		if fault == "429" {
			envelope["parameters"] = map[string]any{"retry_after": 1}
			w.Header().Set("Retry-After", "1")
		}
		if fault == "migrate" {
			envelope["parameters"] = map[string]any{"migrate_to_chat_id": int64(-1009001)}
		}
	}
	reply, _ := json.Marshal(envelope)
	if strings.HasPrefix(fault, "timeout_") || strings.HasPrefix(fault, "disconnect_") {
		t.records.record(r, "telegram", raw, 0, nil, started, fault)
		if strings.HasPrefix(fault, "timeout_") {
			select {
			case <-r.Context().Done():
			case <-time.After(15 * time.Second):
			}
		}
		if h, ok := w.(http.Hijacker); ok {
			conn, _, e := h.Hijack()
			if e == nil {
				conn.Close()
			}
		}
		return
	}
	t.records.record(r, "telegram", raw, status, reply, started, fault)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(reply)
}
func number(value any) int64 {
	switch v := value.(type) {
	case float64:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	case json.Number:
		n, _ := v.Int64()
		return n
	}
	return 0
}
func (t *fakeTelegram) download(w http.ResponseWriter, r *http.Request, token string) {
	filePath := strings.TrimPrefix(r.URL.Path, "/file/bot"+token+"/")
	id, _, _ := strings.Cut(filePath, "/")
	file, ok := t.state.snapshot().Files[id]
	if r.Method != "GET" || !ok || filePath != file.providerPath(id) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", file.Type)
	w.Header().Set("Content-Length", strconv.Itoa(len(file.Data)))
	_, _ = w.Write(file.Data)
	t.records.record(r, "telegram_file", nil, 200, []byte(fmt.Sprintf(`{"size":%d}`, len(file.Data))), time.Now(), "")
}

func (f telegramFile) providerPath(id string) string {
	extension := path.Ext(f.Name)
	valid := len(extension) > 1 && len(extension) <= 11
	for _, r := range strings.TrimPrefix(extension, ".") {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			valid = false
		}
	}
	if !valid {
		extension = ".bin"
	}
	return id + "/file" + extension
}
