package main

import (
	"bytes"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/sadphil/go-winvsocktools/internal/logging"
)

// tcpPair returns both ends of a loopback TCP connection, standing in for a
// vsock connection.
func tcpPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	acc := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		acc <- c
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return c.(*net.TCPConn), (<-acc).(*net.TCPConn)
}

var testLog = logging.New(io.Discard, "test", logging.LevelDebug)

// The peer sees stdin followed by a half-close, and the session lasts
// until the peer closes.
func TestRelayStdioHalfClose(t *testing.T) {
	local, peer := tcpPair(t)
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- relayStdio(testLog, local, newStdinPump(strings.NewReader("request")), &out, -1)
	}()
	got, err := io.ReadAll(peer) // returns at the half-close
	if err != nil || string(got) != "request" {
		t.Fatalf("peer read %q, %v", got, err)
	}
	select {
	case err := <-done:
		t.Fatalf("session ended before the peer closed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	peer.Write([]byte("response"))
	peer.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if out.String() != "response" {
		t.Fatalf("stdout got %q", out.String())
	}
}

// -q closes the connection after stdin EOF even if the peer stays open.
func TestRelayStdioQuit(t *testing.T) {
	local, peer := tcpPair(t)
	defer peer.Close()
	done := make(chan error, 1)
	go func() {
		done <- relayStdio(testLog, local, newStdinPump(strings.NewReader("x")), io.Discard, 20*time.Millisecond)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("-q did not end the session")
	}
}

// Data read from stdin after a session ended goes to the next session.
func TestStdinPumpAcrossSessions(t *testing.T) {
	pr, pw := io.Pipe()
	pump := newStdinPump(pr)

	local1, peer1 := tcpPair(t)
	done := make(chan error, 1)
	go func() { done <- relayStdio(testLog, local1, pump, io.Discard, -1) }()
	pw.Write([]byte("one"))
	buf := make([]byte, 3)
	if _, err := io.ReadFull(peer1, buf); err != nil || string(buf) != "one" {
		t.Fatalf("session 1 got %q, %v", buf, err)
	}
	peer1.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	go pw.Write([]byte("two")) // read by the pump while no session is active
	time.Sleep(20 * time.Millisecond)
	local2, peer2 := tcpPair(t)
	go func() { done <- relayStdio(testLog, local2, pump, io.Discard, -1) }()
	pw.Close()
	got, err := io.ReadAll(peer2)
	if err != nil || string(got) != "two" {
		t.Fatalf("session 2 got %q, %v", got, err)
	}
	peer2.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
