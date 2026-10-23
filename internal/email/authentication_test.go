package email

import (
	"strings"
	"testing"
)

func TestIngressAuthenticationBoundary(t *testing.T) {
	base := "From: sender@example.invalid\r\nTo: inbox@example.invalid\r\nMessage-ID: <one@example.invalid>\r\n\r\nBody"
	trusted := "Authentication-Results: mx.example.invalid; dmarc=pass header.from=example.invalid\r\nReceived: from outside.invalid by mx.example.invalid; today\r\n"
	client, err := New(Config{AccountID: "a", Host: "smtp.invalid", Port: 587, From: "inbox@example.invalid", Enabled: true, TrustedAuthservID: "mx.example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, prefix, status string }{
		{"trusted", trusted, "pass"},
		{"no evidence", "", "unverified"},
		{"wrong server", strings.ReplaceAll(trusted, "mx.example.invalid", "evil.invalid"), "unverified"},
		{"header injected below edge", "Received: by mx.example.invalid; today\r\n" + trusted, "unverified"},
		{"duplicate results", strings.Split(trusted, "Received:")[0] + trusted, "unverified"},
		{"unaligned", strings.Replace(trusted, "header.from=example.invalid", "header.from=evil.invalid", 1), "unverified"},
		{"fail", strings.Replace(trusted, "dmarc=pass", "dmarc=fail", 1), "fail"},
		{"ambiguous", strings.Replace(trusted, "; dmarc=pass", "; dmarc=fail header.from=example.invalid; dmarc=pass", 1), "unverified"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := client.ParseIngress([]byte(tc.prefix + base))
			if err != nil || got.Authentication.Status != tc.status {
				t.Fatalf("%+v %v", got.Authentication, err)
			}
		})
	}
	got, err := Parse([]byte(trusted + base))
	if err != nil || got.Authentication.Status != "unverified" {
		t.Fatal("unconfigured parser trusted headers")
	}
	if _, err := client.ParseIngress([]byte(trusted + "From: attacker@evil.invalid\r\n" + base)); err == nil {
		t.Fatal("duplicate sender accepted")
	}
}

func TestOnlyAuthenticatedDSNSourceIsUsed(t *testing.T) {
	raw := "Authentication-Results: mx.example.invalid; dmarc=pass header.from=example.invalid\r\nReceived: from outside.invalid by mx.example.invalid; today\r\n" + dsnFixture("failed", "5.1.1")
	for _, tc := range []struct {
		server, domain string
		count          int
	}{{"", "", 0}, {"mx.example.invalid", "other.invalid", 0}, {"mx.example.invalid", "example.invalid", 2}} {
		client, err := New(Config{AccountID: "a", Host: "smtp.invalid", Port: 587, From: "inbox@example.invalid", Enabled: true, TrustedAuthservID: tc.server, TrustedDSNDomain: tc.domain})
		if err != nil {
			t.Fatal(err)
		}
		got, err := client.ParseIngress([]byte(raw))
		if err != nil || len(got.DeliveryStatuses) != tc.count {
			t.Fatalf("%+v %v", got.DeliveryStatuses, err)
		}
	}
}
