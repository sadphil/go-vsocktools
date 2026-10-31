package relay

import (
	"bytes"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/sadphil/go-vsocktools/internal/logging"
)

// tcpPair returns both ends of a loopback TCP connection.
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

func TestJoin(t *testing.T) {
	var logBuf bytes.Buffer
	log := logging.New(&logBuf, "test", logging.LevelDebug)

	client, a := tcpPair(t) // client <-> a ... b <-> server
	b, server := tcpPair(t)
	done := make(chan Result, 1)
	go func() { done <- Join(log, "a", a, "b", b) }()

	go func() {
		client.Write([]byte("hello"))
		client.CloseWrite()
	}()
	got, err := io.ReadAll(server)
	if err != nil || string(got) != "hello" {
		t.Fatalf("server read %q, %v", got, err)
	}
	server.Write([]byte("world!"))
	server.CloseWrite()
	got, err = io.ReadAll(client)
	if err != nil || string(got) != "world!" {
		t.Fatalf("client read %q, %v", got, err)
	}

	res := <-done
	if res.Err != nil || res.AtoB != 5 || res.BtoA != 6 {
		t.Fatalf("result = %+v", res)
	}
	for _, want := range []string{"a->b: read 5 bytes", "b->a: wrote 6 bytes (total 6)", "half-closing b side"} {
		if !strings.Contains(logBuf.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, logBuf.String())
		}
	}
}
