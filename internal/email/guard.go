package email

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"strconv"
)

// ErrHostRefused means a mail host is not a public internet address.
var ErrHostRefused = errors.New("mail host is not a public internet address")

// ErrPortRefused means a mail port is not a standard SMTP or IMAP port.
var ErrPortRefused = errors.New("mail port is not a standard SMTP or IMAP port")

// SMTPPorts and IMAPPorts are the ports a site may give its mailbox.
var (
	SMTPPorts = []int{25, 465, 587, 2525}
	IMAPPorts = []int{143, 993}
)

// Ranges that are global unicast by Go's rules but still reach private,
// shared, documentation or translated networks.
var refusedRanges = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/32"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fec0::/10"),
}

// PublicHosts guards the mail servers that a site gives the Hub, so a site
// cannot make the Hub connect to its own or another private network. It
// resolves the name once and connects to the address it checked.
type PublicHosts struct {
	// Allow lists ranges that pass anyway. Only the simulator sets it.
	Allow []netip.Prefix
}

// Public reports whether a connects to the public internet or to an allowed range.
func (g PublicHosts) Public(a netip.Addr) bool {
	a = a.Unmap()
	for _, prefix := range g.Allow {
		if prefix.Contains(a) {
			return true
		}
	}
	if !a.IsGlobalUnicast() || a.IsPrivate() {
		return false
	}
	for _, prefix := range refusedRanges {
		if prefix.Contains(a) {
			return false
		}
	}
	return true
}

// Resolve returns the addresses of host. Every address must be public, so a
// name that also points inside a private network is refused.
func (g PublicHosts) Resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	addresses := []netip.Addr{}
	if literal, err := netip.ParseAddr(host); err == nil {
		addresses = append(addresses, literal)
	} else if addresses, err = net.DefaultResolver.LookupNetIP(ctx, "ip", host); err != nil {
		return nil, errors.New("mail host could not be resolved")
	}
	if len(addresses) == 0 {
		return nil, errors.New("mail host could not be resolved")
	}
	for _, address := range addresses {
		if !g.Public(address) {
			return nil, ErrHostRefused
		}
	}
	return addresses, nil
}

// Dial connects to a public address of the host on a standard mail port.
func (g PublicHosts) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, rawPort, err := net.SplitHostPort(address)
	if err != nil || network != "tcp" {
		return nil, errors.New("invalid mail address")
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil || !(slices.Contains(SMTPPorts, port) || slices.Contains(IMAPPorts, port)) {
		return nil, ErrPortRefused
	}
	addresses, err := g.Resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	return (&net.Dialer{}).DialContext(ctx, network, netip.AddrPortFrom(addresses[0], uint16(port)).String())
}

// CheckPorts refuses an SMTP or IMAP port that a site may not use; 0 is an unset IMAP port.
func CheckPorts(smtpPort, imapPort int) error {
	if !slices.Contains(SMTPPorts, smtpPort) || (imapPort != 0 && !slices.Contains(IMAPPorts, imapPort)) {
		return ErrPortRefused
	}
	return nil
}

// NewWithDialer is a client that reaches its servers only through dial, such as PublicHosts.Dial.
func NewWithDialer(cfg Config, dial func(context.Context, string, string) (net.Conn, error)) (*Client, error) {
	if dial == nil {
		return nil, errors.New("dialer is required")
	}
	c, err := New(cfg)
	if err != nil {
		return nil, err
	}
	c.dialContext = dial
	return c, nil
}
