// Package vsock provides stream sockets over virtio vsock.
//
// On Windows it talks to the Winsock provider installed by the virtio-win
// "viosock" driver. That provider registers its own address family, so the
// family number is discovered at run time (see DetectAF) unless given
// explicitly.
package vsock

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Well-known CIDs and ports.
const (
	CIDAny        = 0xFFFFFFFF // VMADDR_CID_ANY
	CIDHypervisor = 0
	CIDLocal      = 1
	CIDHost       = 2
	PortAny       = 0xFFFFFFFF // VMADDR_PORT_ANY

	// DefaultAF is AF_VSOCK as used by the viosock provider when
	// it cannot be discovered from the Winsock catalog.
	DefaultAF = 40
)

// Addr is a vsock endpoint. It implements net.Addr.
type Addr struct {
	CID  uint32
	Port uint32
}

// Network returns "vsock".
func (a Addr) Network() string { return "vsock" }

func (a Addr) String() string {
	return fmt.Sprintf("%s:%s", fmtNum(a.CID), fmtNum(a.Port))
}

func fmtNum(v uint32) string {
	if v == CIDAny {
		return "any"
	}
	return strconv.FormatUint(uint64(v), 10)
}

// CIDList is a set of allowed peer CIDs. An empty list allows every peer.
// It implements flag.Value: values may be comma-separated and the flag may
// be repeated.
type CIDList []uint32

func (l *CIDList) String() string {
	if l == nil || len(*l) == 0 {
		return ""
	}
	s := make([]string, len(*l))
	for i, c := range *l {
		s[i] = strconv.FormatUint(uint64(c), 10)
	}
	return strings.Join(s, ",")
}

// Set parses a comma-separated list of CIDs and appends them to l.
func (l *CIDList) Set(v string) error {
	for f := range strings.SplitSeq(v, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		c, err := strconv.ParseUint(f, 10, 32)
		if err != nil {
			return fmt.Errorf("invalid CID %q", f)
		}
		if !slices.Contains(*l, uint32(c)) {
			*l = append(*l, uint32(c))
		}
	}
	return nil
}

// Allowed reports whether a peer with the given CID may connect.
func (l CIDList) Allowed(cid uint32) bool {
	return len(l) == 0 || slices.Contains(l, cid)
}
