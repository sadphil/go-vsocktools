// Package tcpopt holds the TCP socket options shared by the tools that talk
// TCP: TCP_NODELAY and keepalive.
package tcpopt

import (
	"flag"
	"fmt"
	"net"
	"time"
)

// Options are TCP socket options.
type Options struct {
	NoDelay           bool
	KeepAlive         bool
	KeepAliveIdle     time.Duration
	KeepAliveInterval time.Duration
	KeepAliveCount    int
}

// RegisterFlags registers -nodelay and the -keepalive* flags on fs.
func (o *Options) RegisterFlags(fs *flag.FlagSet) {
	fs.BoolVar(&o.NoDelay, "nodelay", true, "set TCP_NODELAY on TCP connections (disable Nagle's algorithm)")
	fs.BoolVar(&o.KeepAlive, "keepalive", false, "enable TCP keepalive on TCP connections")
	fs.DurationVar(&o.KeepAliveIdle, "keepalive-idle", 15*time.Second, "idle time before the first keepalive probe")
	fs.DurationVar(&o.KeepAliveInterval, "keepalive-interval", 15*time.Second, "interval between keepalive probes")
	fs.IntVar(&o.KeepAliveCount, "keepalive-count", 9, "unanswered probes before the connection is dropped")
}

// KeepAliveConfig returns the keepalive settings for net.Dialer and
// net.ListenConfig.
func (o *Options) KeepAliveConfig() net.KeepAliveConfig {
	return net.KeepAliveConfig{
		Enable:   o.KeepAlive,
		Idle:     o.KeepAliveIdle,
		Interval: o.KeepAliveInterval,
		Count:    o.KeepAliveCount,
	}
}

// Dialer returns a dialer applying the keepalive settings.
func (o *Options) Dialer(timeout time.Duration) *net.Dialer {
	// A negative KeepAlive together with Enable=false disables keepalive.
	return &net.Dialer{Timeout: timeout, KeepAlive: -1, KeepAliveConfig: o.KeepAliveConfig()}
}

// ListenConfig returns a listen config applying the keepalive settings to
// accepted connections.
func (o *Options) ListenConfig() *net.ListenConfig {
	return &net.ListenConfig{KeepAlive: -1, KeepAliveConfig: o.KeepAliveConfig()}
}

// Apply sets the options that net.Dialer and net.ListenConfig do not cover.
func (o *Options) Apply(c *net.TCPConn) error {
	if err := c.SetNoDelay(o.NoDelay); err != nil {
		return fmt.Errorf("set TCP_NODELAY=%v: %w", o.NoDelay, err)
	}
	return nil
}

func (o *Options) String() string {
	s := fmt.Sprintf("nodelay=%v keepalive=%v", o.NoDelay, o.KeepAlive)
	if o.KeepAlive {
		s += fmt.Sprintf(" (idle=%v interval=%v count=%d)", o.KeepAliveIdle, o.KeepAliveInterval, o.KeepAliveCount)
	}
	return s
}
