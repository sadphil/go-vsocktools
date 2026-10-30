package tcpopt

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// PrefixList is a set of allowed client networks. An empty list allows every
// client. It implements flag.Value: entries are comma-separated CIDR
// prefixes or plain addresses, and the flag may be repeated.
type PrefixList []netip.Prefix

func (l *PrefixList) String() string {
	if l == nil {
		return ""
	}
	s := make([]string, len(*l))
	for i, p := range *l {
		s[i] = p.String()
	}
	return strings.Join(s, ",")
}

// Set parses a comma-separated list of prefixes and appends them to l.
func (l *PrefixList) Set(v string) error {
	for f := range strings.SplitSeq(v, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		p, err := netip.ParsePrefix(f)
		if err != nil {
			a, aerr := netip.ParseAddr(f)
			if aerr != nil {
				return fmt.Errorf("invalid address or prefix %q", f)
			}
			p = netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen())
		}
		p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()).Masked()
		if !slices.Contains(*l, p) {
			*l = append(*l, p)
		}
	}
	return nil
}

// Allowed reports whether a client with address a may connect.
func (l PrefixList) Allowed(a netip.Addr) bool {
	if len(l) == 0 {
		return true
	}
	a = a.Unmap()
	return slices.ContainsFunc(l, func(p netip.Prefix) bool { return p.Contains(a) })
}
