package email

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

func TestPublicHostsRefusesPrivateAndSpecialAddresses(t *testing.T) {
	guard := PublicHosts{}
	for _, host := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.9", "192.168.1.1", "169.254.169.254", "100.64.0.5",
		"0.0.0.0", "198.18.0.1", "::1", "fd00::1", "fe80::1", "::ffff:127.0.0.1", "64:ff9b::a00:1", "2002:a00:1::1", "localhost"} {
		if _, err := guard.Resolve(context.Background(), host); !errors.Is(err, ErrHostRefused) {
			t.Errorf("%s: got %v, want refused", host, err)
		}
	}
	for _, host := range []string{"8.8.8.8", "2606:4700:4700::1111"} {
		if _, err := guard.Resolve(context.Background(), host); err != nil {
			t.Errorf("%s: %v", host, err)
		}
	}
}

func TestPublicHostsAllowListIsExplicit(t *testing.T) {
	guard := PublicHosts{Allow: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}}
	if _, err := guard.Resolve(context.Background(), "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if _, err := guard.Resolve(context.Background(), "127.0.0.2"); !errors.Is(err, ErrHostRefused) {
		t.Fatal("only the allowed address may pass")
	}
}

func TestPublicHostsDialRefusesBeforeConnecting(t *testing.T) {
	guard := PublicHosts{}
	for address, want := range map[string]error{"127.0.0.1:587": ErrHostRefused, "8.8.8.8:22": ErrPortRefused, "169.254.169.254:80": ErrPortRefused} {
		if _, err := guard.Dial(context.Background(), "tcp", address); !errors.Is(err, want) {
			t.Errorf("%s: got %v, want %v", address, err, want)
		}
	}
}

func TestCheckPortsAllowsOnlyMailPorts(t *testing.T) {
	for _, ports := range [][2]int{{587, 993}, {465, 0}, {25, 143}, {2525, 993}} {
		if CheckPorts(ports[0], ports[1]) != nil {
			t.Errorf("%v refused", ports)
		}
	}
	for _, ports := range [][2]int{{22, 993}, {587, 6379}, {993, 993}, {587, 587}} {
		if !errors.Is(CheckPorts(ports[0], ports[1]), ErrPortRefused) {
			t.Errorf("%v allowed", ports)
		}
	}
}
