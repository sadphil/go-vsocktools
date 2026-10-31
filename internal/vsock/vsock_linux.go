//go:build linux

package vsock

import (
	"fmt"
	"net"
	"os"
	"sync/atomic"

	mdvsock "github.com/mdlayher/vsock"
	"golang.org/x/sys/unix"
)

// DetectAF returns the kernel's AF_VSOCK; on Linux there is no provider to
// look up.
func DetectAF() (int, string, error) {
	return unix.AF_VSOCK, "Linux kernel AF_VSOCK", nil
}

// Listener accepts vsock stream connections.
type Listener struct {
	l      *mdvsock.Listener
	addr   Addr
	closed atomic.Bool
}

// Listen creates a vsock listener on the given port of any local CID. af and
// backlog are ignored on Linux.
func Listen(af int, port uint32, backlog int) (*Listener, error) {
	l, err := mdvsock.ListenContextID(CIDAny, port, nil)
	if err != nil {
		return nil, err
	}
	return &Listener{l: l, addr: fromNetAddr(l.Addr())}, nil
}

// Addr returns the local address of the listener.
func (l *Listener) Addr() Addr { return l.addr }

// Accept waits for and returns the next connection. After Close it returns
// net.ErrClosed.
func (l *Listener) Accept() (*Conn, error) {
	c, err := l.l.Accept()
	if err != nil {
		if l.closed.Load() {
			return nil, net.ErrClosed
		}
		return nil, err
	}
	return newConn(c.(*mdvsock.Conn)), nil
}

// Close stops the listener; a blocked Accept returns net.ErrClosed.
func (l *Listener) Close() error {
	if l.closed.Swap(true) {
		return nil
	}
	return l.l.Close()
}

// Dial connects to the vsock endpoint cid:port. af is ignored on Linux.
func Dial(af int, cid, port uint32) (*Conn, error) {
	c, err := mdvsock.Dial(cid, port, nil)
	if err != nil {
		return nil, err
	}
	return newConn(c), nil
}

// Conn is a vsock stream connection.
type Conn struct {
	c             *mdvsock.Conn
	local, remote Addr
}

func newConn(c *mdvsock.Conn) *Conn {
	conn := &Conn{c: c, local: fromNetAddr(c.LocalAddr()), remote: fromNetAddr(c.RemoteAddr())}
	// The library reports the listener's wildcard CID for accepted
	// connections; ask the kernel for the actual local address.
	if conn.local.CID == CIDAny {
		if rc, err := c.SyscallConn(); err == nil {
			rc.Control(func(fd uintptr) {
				if sa, err := unix.Getsockname(int(fd)); err == nil {
					if vm, ok := sa.(*unix.SockaddrVM); ok {
						conn.local = Addr{CID: vm.CID, Port: vm.Port}
					}
				}
			})
		}
	}
	// Connected client sockets keep the wildcard CID; the local CID is then
	// the loopback CID or this machine's CID.
	if conn.local.CID == CIDAny {
		if conn.remote.CID == CIDLocal {
			conn.local.CID = CIDLocal
		} else if cid, err := mdvsock.ContextID(); err == nil {
			conn.local.CID = cid
		}
	}
	return conn
}

func fromNetAddr(a net.Addr) Addr {
	if va, ok := a.(*mdvsock.Addr); ok {
		return Addr{CID: va.ContextID, Port: va.Port}
	}
	return Addr{CID: CIDAny, Port: PortAny}
}

// Read reads from the connection. It returns io.EOF once the peer has shut
// down its sending side.
func (c *Conn) Read(p []byte) (int, error) { return c.c.Read(p) }

// Write writes p to the connection.
func (c *Conn) Write(p []byte) (int, error) { return c.c.Write(p) }

// CloseWrite shuts down the sending side of the connection.
func (c *Conn) CloseWrite() error { return c.c.CloseWrite() }

// Close closes the connection, waking any goroutine blocked in Read or Write.
func (c *Conn) Close() error { return c.c.Close() }

// LocalAddr returns the local vsock address.
func (c *Conn) LocalAddr() Addr { return c.local }

// RemoteAddr returns the peer's vsock address.
func (c *Conn) RemoteAddr() Addr { return c.remote }

// File returns a duplicate of the socket in blocking mode, for use as a child
// process's standard input or output. The caller must close it. Clearing
// O_NONBLOCK also affects c, so c must not be used for I/O afterwards.
func (c *Conn) File() (*os.File, error) {
	rc, err := c.c.SyscallConn()
	if err != nil {
		return nil, err
	}
	var (
		nfd  int
		derr error
	)
	if err := rc.Control(func(fd uintptr) {
		nfd, derr = unix.FcntlInt(fd, unix.F_DUPFD_CLOEXEC, 0)
	}); err != nil {
		return nil, err
	}
	if derr != nil {
		return nil, os.NewSyscallError("fcntl", derr)
	}
	if err := unix.SetNonblock(nfd, false); err != nil {
		unix.Close(nfd)
		return nil, os.NewSyscallError("fcntl", err)
	}
	return os.NewFile(uintptr(nfd), fmt.Sprintf("vsock:%s->%s", c.local, c.remote)), nil
}
