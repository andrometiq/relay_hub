package email

import "testing"

func TestEmailConfig(t *testing.T) {
	if _, e := New(Config{Enabled: true, AccountID: "a", Host: "smtp.example.com", Port: 587, From: "Relay <relay@example.com>"}); e != nil {
		t.Fatal(e)
	}
}
func TestRejectBadRecipient(t *testing.T) {
	c, _ := New(Config{Enabled: true, AccountID: "a", Host: "smtp.example.com", Port: 587, From: "relay@example.com"})
	if e := c.Send(t.Context(), []string{"bad"}, "x", "body", nil, nil); e == nil {
		t.Fatal("bad recipient accepted")
	}
}
