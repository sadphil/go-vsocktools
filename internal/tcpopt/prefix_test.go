package tcpopt

import (
	"net/netip"
	"testing"
)

func TestPrefixList(t *testing.T) {
	var l PrefixList
	if !l.Allowed(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("empty list must allow every address")
	}
	if err := l.Set("10.1.2.3/8, 192.168.1.5"); err != nil {
		t.Fatal(err)
	}
	if err := l.Set("::1"); err != nil {
		t.Fatal(err)
	}
	if got, want := l.String(), "10.0.0.0/8,192.168.1.5/32,::1/128"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	for addr, want := range map[string]bool{
		"10.200.0.1":         true,
		"::ffff:10.0.0.7":    true, // IPv4-mapped address of a dual-stack listener
		"192.168.1.5":        true,
		"192.168.1.6":        false,
		"::1":                true,
		"2001:db8::1":        false,
		"::ffff:192.168.1.6": false,
	} {
		if got := l.Allowed(netip.MustParseAddr(addr)); got != want {
			t.Errorf("Allowed(%s) = %v, want %v", addr, got, want)
		}
	}
	if err := l.Set("not-an-ip"); err == nil {
		t.Error("expected error")
	}
}
