//go:build !windows

package vsock

import (
	"errors"
	"io"
)

var errUnsupported = errors.New("vsock: only supported on windows")

// DetectAF is not supported on this platform.
func DetectAF() (int, string, error) { return 0, "", errUnsupported }

// Listen is not supported on this platform.
func Listen(af int, port uint32, backlog int) (*Listener, error) { return nil, errUnsupported }

// Dial is not supported on this platform.
func Dial(af int, cid, port uint32) (*Conn, error) { return nil, errUnsupported }

// Listener is a vsock listener (unsupported on this platform).
type Listener struct{}

func (l *Listener) Accept() (*Conn, error) { return nil, errUnsupported }
func (l *Listener) Close() error           { return nil }
func (l *Listener) Addr() Addr             { return Addr{} }

// Conn is a vsock stream connection (unsupported on this platform).
type Conn struct{}

func (c *Conn) Read([]byte) (int, error)  { return 0, io.EOF }
func (c *Conn) Write([]byte) (int, error) { return 0, errUnsupported }
func (c *Conn) CloseWrite() error         { return errUnsupported }
func (c *Conn) Close() error              { return nil }
func (c *Conn) LocalAddr() Addr           { return Addr{} }
func (c *Conn) RemoteAddr() Addr          { return Addr{} }
func (c *Conn) Fd() uintptr               { return ^uintptr(0) }
