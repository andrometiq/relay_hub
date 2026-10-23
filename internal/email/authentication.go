package email

import (
	"bufio"
	"bytes"
	"net/mail"
	"net/textproto"
	"regexp"
	"strings"
)

// Authentication describes evidence from the configured receiving edge, not From alone.
type Authentication struct {
	Status string `json:"status"`
	Server string `json:"server,omitempty"`
	Domain string `json:"domain,omitempty"`
}

var authResultRE = regexp.MustCompile(`(?i)^dmarc\s*=\s*(pass|fail|none|temperror|permerror)\s+header\.from\s*=\s*([a-z0-9.-]+)\s*$`)
var receivedByRE = regexp.MustCompile(`(?i)(?:^|\s)by\s+([a-z0-9.-]+)(?:\s|;|$)`)

// The receiving edge must strip forged local results and prepend its own result
// above its Received field. Anything below that boundary is untrusted.
func authenticate(raw []byte, header mail.Header, from, server string) Authentication {
	result := Authentication{Status: "unverified"}
	if server == "" {
		return result
	}
	reader := textproto.NewReader(bufio.NewReader(bytes.NewReader(raw)))
	var evidence string
	for {
		line, err := reader.ReadContinuedLine()
		if err != nil || line == "" {
			return result
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return result
		}
		if strings.EqualFold(key, "Received") {
			by := receivedByRE.FindStringSubmatch(value)
			if len(by) != 2 || !strings.EqualFold(by[1], server) {
				return result
			}
			break
		}
		if strings.EqualFold(key, "Authentication-Results") {
			if evidence != "" {
				return result
			}
			evidence = strings.TrimSpace(value)
		}
	}
	if len(header["From"]) != 1 || evidence == "" {
		return result
	}
	fields := strings.Split(evidence, ";")
	if len(fields) < 2 || !strings.EqualFold(strings.TrimSpace(fields[0]), server) {
		return result
	}
	domain := from[strings.LastIndex(from, "@")+1:]
	seen := false
	for _, field := range fields[1:] {
		field = strings.TrimSpace(field)
		if !strings.HasPrefix(strings.ToLower(field), "dmarc") {
			continue
		}
		if seen {
			return Authentication{Status: "unverified"}
		}
		seen = true
		match := authResultRE.FindStringSubmatch(field)
		if len(match) != 3 || !strings.EqualFold(match[2], domain) {
			return Authentication{Status: "unverified"}
		}
		result = Authentication{Status: strings.ToLower(match[1]), Server: strings.ToLower(server), Domain: strings.ToLower(domain)}
	}
	return result
}

func (c *Client) ParseIngress(raw []byte) (Received, error) {
	received, err := parse(raw, c.cfg.TrustedAuthservID)
	if err != nil {
		return Received{}, err
	}
	if received.Authentication.Status == "pass" && c.cfg.TrustedDSNDomain != "" && strings.EqualFold(received.Authentication.Domain, c.cfg.TrustedDSNDomain) {
		received.DeliveryStatuses, err = ParseDeliveryStatuses(raw)
	}
	return received, err
}
