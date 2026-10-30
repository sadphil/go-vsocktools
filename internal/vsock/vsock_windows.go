//go:build windows

package vsock

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modws2_32       = windows.NewLazySystemDLL("ws2_32.dll")
	procBind        = modws2_32.NewProc("bind")
	procAccept      = modws2_32.NewProc("accept")
	procGetsockname = modws2_32.NewProc("getsockname")
	procConnect     = modws2_32.NewProc("connect")
)

// rawSockaddrVM mirrors SOCKADDR_VM from virtio-win's vio_sockets.h.
type rawSockaddrVM struct {
	Family    uint16
	Reserved1 uint16
	Port      uint32
	CID       uint32
	Zero      [4]byte
}

const sockaddrVMLen = int32(unsafe.Sizeof(rawSockaddrVM{}))

var (
	wsaOnce sync.Once
	wsaErr  error
)

func wsaStartup() error {
	wsaOnce.Do(func() {
		var d windows.WSAData
		if err := windows.WSAStartup(0x0202, &d); err != nil {
			wsaErr = os.NewSyscallError("WSAStartup", err)
		}
	})
	return wsaErr
}

// DetectAF looks up the virtio vsock provider in the Winsock catalog and
// returns its address family together with the provider's protocol name.
func DetectAF() (int, string, error) {
	if err := wsaStartup(); err != nil {
		return 0, "", err
	}
	var size uint32
	_, err := windows.WSAEnumProtocols(nil, nil, &size)
	if err != nil && !errors.Is(err, windows.WSAENOBUFS) {
		return 0, "", os.NewSyscallError("WSAEnumProtocols", err)
	}
	entry := uint32(unsafe.Sizeof(windows.WSAProtocolInfo{}))
	protos := make([]windows.WSAProtocolInfo, size/entry+1)
	size = uint32(len(protos)) * entry
	n, err := windows.WSAEnumProtocols(nil, &protos[0], &size)
	if err != nil {
		return 0, "", os.NewSyscallError("WSAEnumProtocols", err)
	}
	var fallback *windows.WSAProtocolInfo
	for i := range protos[:n] {
		p := &protos[i]
		name := strings.ToLower(windows.UTF16ToString(p.ProtocolName[:]))
		if p.SocketType != windows.SOCK_STREAM {
			continue
		}
		if strings.Contains(name, "vsock") || strings.Contains(name, "viosock") || strings.Contains(name, "virtio") {
			return int(p.AddressFamily), windows.UTF16ToString(p.ProtocolName[:]), nil
		}
		if p.AddressFamily == DefaultAF && fallback == nil {
			fallback = p
		}
	}
	if fallback != nil {
		return DefaultAF, windows.UTF16ToString(fallback.ProtocolName[:]), nil
	}
	return 0, "", errors.New("virtio vsock Winsock provider not found (is the viosock driver installed?)")
}

func newSocket(af int) (windows.Handle, error) {
	// Sockets are created without WSA_FLAG_OVERLAPPED: all I/O here is
	// blocking, and a non-overlapped socket handle is what child processes
	// expect when it becomes their standard input/output.
	h, err := windows.WSASocket(int32(af), windows.SOCK_STREAM, 0, nil, 0, windows.WSA_FLAG_NO_HANDLE_INHERIT)
	if err != nil {
		// Older systems or providers may reject the flag.
		h, err = windows.WSASocket(int32(af), windows.SOCK_STREAM, 0, nil, 0, 0)
	}
	if err != nil {
		return windows.InvalidHandle, os.NewSyscallError("WSASocket", err)
	}
	return h, nil
}

// Listener accepts vsock stream connections.
type Listener struct {
	h      windows.Handle
	addr   Addr
	closed atomic.Bool
}

// Listen creates a vsock listener on the given port of any local CID using
// address family af.
func Listen(af int, port uint32, backlog int) (*Listener, error) {
	if err := wsaStartup(); err != nil {
		return nil, err
	}
	h, err := newSocket(af)
	if err != nil {
		return nil, err
	}
	sa := rawSockaddrVM{Family: uint16(af), Port: port, CID: CIDAny}
	if r, _, e := procBind.Call(uintptr(h), uintptr(unsafe.Pointer(&sa)), uintptr(sockaddrVMLen)); int32(r) != 0 {
		windows.Closesocket(h)
		return nil, os.NewSyscallError("bind", e)
	}
	if err := windows.Listen(h, backlog); err != nil {
		windows.Closesocket(h)
		return nil, os.NewSyscallError("listen", err)
	}
	l := &Listener{h: h, addr: Addr{CID: CIDAny, Port: port}}
	if a, err := sockname(h); err == nil {
		l.addr = a
	}
	return l, nil
}

func sockname(h windows.Handle) (Addr, error) {
	var sa rawSockaddrVM
	n := sockaddrVMLen
	if r, _, e := procGetsockname.Call(uintptr(h), uintptr(unsafe.Pointer(&sa)), uintptr(unsafe.Pointer(&n))); int32(r) != 0 {
		return Addr{}, os.NewSyscallError("getsockname", e)
	}
	return Addr{CID: sa.CID, Port: sa.Port}, nil
}

// Addr returns the local address of the listener.
func (l *Listener) Addr() Addr { return l.addr }

// Accept waits for and returns the next connection. After Close it returns
// net.ErrClosed.
func (l *Listener) Accept() (*Conn, error) {
	var sa rawSockaddrVM
	n := sockaddrVMLen
	r, _, e := procAccept.Call(uintptr(l.h), uintptr(unsafe.Pointer(&sa)), uintptr(unsafe.Pointer(&n)))
	if windows.Handle(r) == windows.InvalidHandle {
		if l.closed.Load() {
			return nil, net.ErrClosed
		}
		return nil, os.NewSyscallError("accept", e)
	}
	c := &Conn{h: windows.Handle(r), local: l.addr, remote: Addr{CID: sa.CID, Port: sa.Port}}
	if a, err := sockname(c.h); err == nil {
		c.local = a
	}
	return c, nil
}

// Close stops the listener; a blocked Accept returns net.ErrClosed.
func (l *Listener) Close() error {
	if l.closed.Swap(true) {
		return nil
	}
	return windows.Closesocket(l.h)
}

// Dial connects to the vsock endpoint cid:port using address family af.
func Dial(af int, cid, port uint32) (*Conn, error) {
	if err := wsaStartup(); err != nil {
		return nil, err
	}
	h, err := newSocket(af)
	if err != nil {
		return nil, err
	}
	remote := Addr{CID: cid, Port: port}
	sa := rawSockaddrVM{Family: uint16(af), Port: port, CID: cid}
	if r, _, e := procConnect.Call(uintptr(h), uintptr(unsafe.Pointer(&sa)), uintptr(sockaddrVMLen)); int32(r) != 0 {
		windows.Closesocket(h)
		return nil, &net.OpError{Op: "dial", Net: "vsock", Addr: remote, Err: os.NewSyscallError("connect", e)}
	}
	c := &Conn{h: h, remote: remote}
	if a, err := sockname(h); err == nil {
		c.local = a
	}
	return c, nil
}

// maxIO caps a single WSARecv/WSASend request.
const maxIO = 1 << 30

// Conn is a vsock stream connection using blocking Winsock calls.
//
// Close may be called while another goroutine is blocked in Read or Write:
// the socket is shut down and its pending I/O cancelled to wake the blocked
// call, and the handle is released only after the last in-flight call
// returns, so it can never be reused underneath a pending operation.
type Conn struct {
	h             windows.Handle
	local, remote Addr

	mu       sync.Mutex
	inflight int
	closed   bool
	released bool
}

func (c *Conn) acquire() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	c.inflight++
	return nil
}

func (c *Conn) release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inflight--
	if c.closed && c.inflight == 0 && !c.released {
		c.released = true
		windows.Closesocket(c.h)
	}
}

func (c *Conn) opError(op string, err error) error {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		err = net.ErrClosed
	}
	return &net.OpError{Op: op, Net: "vsock", Source: c.local, Addr: c.remote, Err: err}
}

// Read reads from the connection. It returns io.EOF once the peer has shut
// down its sending side.
func (c *Conn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := c.acquire(); err != nil {
		return 0, c.opError("read", err)
	}
	defer c.release()
	if len(p) > maxIO {
		p = p[:maxIO]
	}
	buf := windows.WSABuf{Len: uint32(len(p)), Buf: &p[0]}
	var n, flags uint32
	if err := windows.WSARecv(c.h, &buf, 1, &n, &flags, nil, nil); err != nil {
		if errors.Is(err, windows.WSAEDISCON) {
			return 0, io.EOF
		}
		return int(n), c.opError("read", os.NewSyscallError("WSARecv", err))
	}
	if n == 0 {
		return 0, io.EOF
	}
	return int(n), nil
}

// Write writes all of p to the connection.
func (c *Conn) Write(p []byte) (int, error) {
	if err := c.acquire(); err != nil {
		return 0, c.opError("write", err)
	}
	defer c.release()
	written := 0
	for written < len(p) {
		chunk := p[written:]
		if len(chunk) > maxIO {
			chunk = chunk[:maxIO]
		}
		buf := windows.WSABuf{Len: uint32(len(chunk)), Buf: &chunk[0]}
		var n uint32
		if err := windows.WSASend(c.h, &buf, 1, &n, 0, nil, nil); err != nil {
			return written + int(n), c.opError("write", os.NewSyscallError("WSASend", err))
		}
		written += int(n)
	}
	return written, nil
}

// CloseWrite shuts down the sending side of the connection.
func (c *Conn) CloseWrite() error {
	if err := c.acquire(); err != nil {
		return c.opError("shutdown", err)
	}
	defer c.release()
	if err := windows.Shutdown(c.h, windows.SHUT_WR); err != nil {
		return c.opError("shutdown", os.NewSyscallError("shutdown", err))
	}
	return nil
}

// Close closes the connection, waking any goroutine blocked in Read or Write.
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if c.inflight > 0 {
		// Wake blocked calls; the last one to return releases the handle.
		windows.Shutdown(c.h, windows.SHUT_RDWR)
		windows.CancelIoEx(c.h, nil)
		return nil
	}
	c.released = true
	if err := windows.Closesocket(c.h); err != nil {
		return fmt.Errorf("closesocket: %w", err)
	}
	return nil
}

// LocalAddr returns the local vsock address.
func (c *Conn) LocalAddr() Addr { return c.local }

// RemoteAddr returns the peer's vsock address.
func (c *Conn) RemoteAddr() Addr { return c.remote }

// Fd returns the underlying SOCKET handle.
func (c *Conn) Fd() uintptr { return uintptr(c.h) }
