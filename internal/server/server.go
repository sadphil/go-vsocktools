// Package server implements the accept loop shared by the tools: listening,
// peer filtering, per-connection logging and dispatching every client to its
// own goroutine (inetd "nowait"), or serving clients one at a time (inetd
// "wait").
package server

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/sadphil/go-winvsocktools/internal/logging"
	"github.com/sadphil/go-winvsocktools/internal/version"
	"github.com/sadphil/go-winvsocktools/internal/vsock"
)

// Common holds the options shared by every tool.
type Common struct {
	AF      int
	Verbose bool
	Version bool
}

// RegisterFlags registers -af, -v and -version on fs.
func (c *Common) RegisterFlags(fs *flag.FlagSet) {
	c.RegisterBaseFlags(fs)
	fs.BoolVar(&c.Verbose, "v", false, "verbose: log every read/write with its size in bytes")
}

// RegisterBaseFlags registers -af and -version on fs.
func (c *Common) RegisterBaseFlags(fs *flag.FlagSet) {
	fs.IntVar(&c.AF, "af", 0, "vsock address `family`; 0 detects the viosock Winsock provider")
	fs.BoolVar(&c.Version, "version", false, "print version and exit")
}

// Level returns the log level selected by -v.
func (c *Common) Level() logging.Level {
	if c.Verbose {
		return logging.LevelDebug
	}
	return logging.LevelInfo
}

// ResolveAF returns c.AF, or detects the vsock address family if it is 0.
func (c *Common) ResolveAF(log *logging.Logger) (int, error) {
	if c.AF != 0 {
		return c.AF, nil
	}
	af, name, err := vsock.DetectAF()
	if err != nil {
		return 0, err
	}
	log.Infof("using Winsock provider %q, address family %d", name, af)
	return af, nil
}

// Config holds the options of the tools that listen on a vsock port.
type Config struct {
	Common
	Port    uint64
	Allow   vsock.CIDList
	Backlog int
	Wait    bool // serve one client at a time instead of one goroutine per client
}

// RegisterFlags registers the vsock listener flags on fs.
func (c *Config) RegisterFlags(fs *flag.FlagSet) {
	fs.Uint64Var(&c.Port, "port", 0, "vsock `port` to listen on (required)")
	fs.Var(&c.Allow, "allow-cid", "comma-separated peer `CIDs` allowed to connect; may be repeated (default: any)")
	fs.IntVar(&c.Backlog, "backlog", 128, "listen backlog")
	c.Common.RegisterFlags(fs)
}

// Validate checks the vsock listener options.
func (c *Config) Validate() error {
	return ValidatePort(c.Port)
}

// ValidatePort checks that p is a usable vsock port number.
func ValidatePort(p uint64) error {
	if p == 0 || p >= vsock.PortAny {
		return fmt.Errorf("-port must be in range 1..%d", uint32(vsock.PortAny-1))
	}
	return nil
}

// Handler serves one accepted client. log is tagged with the connection id
// and peer address. The handler owns c and must close it.
type Handler func(log *logging.Logger, c *vsock.Conn)

// Run listens on a vsock port according to cfg and serves clients with h
// until interrupted.
func Run(log *logging.Logger, cfg *Config, h Handler) error {
	af, err := cfg.ResolveAF(log)
	if err != nil {
		return err
	}
	ln, err := vsock.Listen(af, uint32(cfg.Port), cfg.Backlog)
	if err != nil {
		return fmt.Errorf("listen on vsock port %d: %w", cfg.Port, err)
	}
	log.Infof("%s listening on vsock %s (af %d), allowed peer CIDs: %s, mode: %s",
		version.Version, ln.Addr(), af, AllowedCIDs(cfg.Allow), modeName(cfg.Wait))
	return Serve(log, VsockLoop(ln, cfg.Allow, cfg.Wait, h))
}

// AllowedCIDs describes an allow list for logging.
func AllowedCIDs(l vsock.CIDList) string {
	if len(l) == 0 {
		return "any"
	}
	return l.String()
}

// VsockLoop returns the accept loop for a vsock listener admitting peers
// whose CID is in allow.
func VsockLoop(ln *vsock.Listener, allow vsock.CIDList, wait bool, h Handler) Loop[*vsock.Conn] {
	return Loop[*vsock.Conn]{
		Accept: ln.Accept,
		Close:  ln.Close,
		Wait:   wait,
		Admit: func(c *vsock.Conn) (string, string, error) {
			peer := c.RemoteAddr()
			tag := fmt.Sprintf("cid=%d port=%d", peer.CID, peer.Port)
			desc := fmt.Sprintf("%s on %s", peer, c.LocalAddr())
			if !allow.Allowed(peer.CID) {
				return tag, desc, fmt.Errorf("CID %d not allowed", peer.CID)
			}
			return tag, desc, nil
		},
		Handle: h,
	}
}

func modeName(wait bool) string {
	if wait {
		return "wait (one client at a time)"
	}
	return "nowait (one goroutine per client)"
}

// Loop describes an accept loop over connections of type C.
type Loop[C io.Closer] struct {
	// Accept returns the next connection, or an error wrapping
	// net.ErrClosed once Close has been called.
	Accept func() (C, error)
	// Close closes the listener; it is called on interrupt.
	Close func() error
	// Admit returns the log tag and a description of a new connection,
	// and a non-nil error if the connection must be rejected.
	Admit func(C) (tag, desc string, reject error)
	// Handle serves an admitted connection and must close it.
	Handle func(*logging.Logger, C)
	// Wait serves one client at a time instead of one goroutine per client.
	Wait bool
}

// Serve runs l until its listener is closed, either by an interrupt or by
// the caller. An interrupt also returns from a client being served in wait
// mode; the caller is expected to exit then.
func Serve[C io.Closer](log *logging.Logger, l Loop[C]) error {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	stop := make(chan struct{})
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case s := <-sig:
			log.Infof("received %v, closing listener", s)
			close(stop)
			l.Close()
		case <-finished:
		}
	}()

	var (
		nextID int64
		active atomic.Int64
		delay  time.Duration
	)
	for {
		c, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				log.Infof("listener closed, %d connection(s) still active", active.Load())
				return nil
			}
			// Transient failure (resources, aborted handshake): back off.
			delay = min(max(2*delay, 5*time.Millisecond), time.Second)
			log.Errorf("accept: %v; retrying in %v", err, delay)
			time.Sleep(delay)
			continue
		}
		delay = 0
		nextID++
		tag, desc, reject := l.Admit(c)
		clog := log.With("#%d %s", nextID, tag)
		if reject != nil {
			clog.Warnf("rejected connection from %s: %v", desc, reject)
			c.Close()
			continue
		}
		clog.Infof("accepted connection from %s (active: %d)", desc, active.Add(1))
		serve := func() {
			start := time.Now()
			defer func() {
				if r := recover(); r != nil {
					clog.Errorf("panic: %v\n%s", r, debug.Stack())
					c.Close()
				}
				clog.Infof("connection finished after %v (active: %d)",
					time.Since(start).Round(time.Millisecond), active.Add(-1))
			}()
			l.Handle(clog, c)
		}
		if !l.Wait {
			go serve()
			continue
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			serve()
		}()
		select {
		case <-done:
		case <-stop:
			log.Infof("interrupted while serving a client")
			return nil
		}
	}
}
