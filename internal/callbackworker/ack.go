package callbackworker

import (
	"encoding/json"
	"io"
	"net/http"
)

// Acknowledged requires Core's durable event count, not just HTTP success.
func Acknowledged(resp *http.Response, count int) bool {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	var reply struct {
		Message struct {
			Accepted int `json:"accepted"`
		} `json:"message"`
	}
	return resp.StatusCode == http.StatusOK && err == nil && len(raw) <= 65536 && json.Unmarshal(raw, &reply) == nil && reply.Message.Accepted == count
}
