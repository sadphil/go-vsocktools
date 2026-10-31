//go:build linux

// Package e2e runs the built tools against each other over vsock loopback
// (CID 1). The tests are skipped unless the vsock_loopback kernel module is
// loaded.
package e2e

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sadphil/go-vsocktools/internal/vsock"
)

var (
	binDir     string
	skipReason string
)

func TestMain(m *testing.M) {
	skipReason = checkLoopback()
	if skipReason == "" {
		dir, err := os.MkdirTemp("", "vsocktools-e2e-")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		build := exec.Command("go", "build", "-o", dir+"/", "./cmd/...")
		build.Dir = "../.."
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := build.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "build tools: %v\n%s", err, out)
			os.Exit(1)
		}
		binDir = dir
	}
	code := m.Run()
	if binDir != "" {
		os.RemoveAll(binDir)
	}
	os.Exit(code)
}

// checkLoopback returns why vsock loopback is unusable, or "".
func checkLoopback() string {
	port := randPort()
	ln, err := vsock.Listen(0, port, 1)
	if err != nil {
		return fmt.Sprintf("vsock listen: %v", err)
	}
	defer ln.Close()
	c, err := vsock.Dial(0, vsock.CIDLocal, port)
	if err != nil {
		return fmt.Sprintf("vsock loopback dial: %v (load the vsock_loopback kernel module)", err)
	}
	c.Close()
	return ""
}

func requireLoopback(t *testing.T) {
	t.Helper()
	if skipReason != "" {
		t.Skip(skipReason)
	}
}

func randPort() uint32 { return 20000 + mrand.Uint32N(40000) }

func freeTCPAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// syncBuffer is a bytes.Buffer safe for concurrent use.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// proc is a running tool.
type proc struct {
	cmd    *exec.Cmd
	stdout syncBuffer
	stderr syncBuffer
	done   chan struct{}
}

// start runs a tool with stdin and waits until its stderr contains ready
// (if not empty). The process is stopped at the end of the test, and its
// stderr is logged if the test failed.
func start(t *testing.T, stdin io.Reader, ready, name string, args ...string) *proc {
	t.Helper()
	p := &proc{cmd: exec.Command(filepath.Join(binDir, name), args...), done: make(chan struct{})}
	p.cmd.Stdin = stdin
	p.cmd.Stdout = &p.stdout
	errPipe, err := p.cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	readyCh := make(chan struct{})
	go func() {
		var once sync.Once
		sc := bufio.NewScanner(errPipe)
		for sc.Scan() {
			p.stderr.Write([]byte(sc.Text() + "\n"))
			if ready != "" && strings.Contains(sc.Text(), ready) {
				once.Do(func() { close(readyCh) })
			}
		}
		p.cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		p.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-p.done:
		case <-time.After(3 * time.Second):
			p.cmd.Process.Kill()
			<-p.done
		}
		if t.Failed() {
			t.Logf("%s stderr:\n%s", name, p.stderr.String())
		}
	})
	if ready != "" {
		select {
		case <-readyCh:
		case <-p.done:
			t.Fatalf("%s exited before ready:\n%s", name, p.stderr.String())
		case <-time.After(10 * time.Second):
			t.Fatalf("%s not ready:\n%s", name, p.stderr.String())
		}
	}
	return p
}

// wait waits for the process to exit and returns its exit code.
func (p *proc) wait(t *testing.T) int {
	t.Helper()
	select {
	case <-p.done:
	case <-time.After(15 * time.Second):
		t.Fatalf("timeout waiting for %s:\n%s", p.cmd.Path, p.stderr.String())
	}
	return p.cmd.ProcessState.ExitCode()
}

// halfCloser is a stream whose sending side can be shut down.
type halfCloser interface {
	io.ReadWriteCloser
	CloseWrite() error
}

// roundTrip sends data, shuts down sending and returns everything received.
func roundTrip(t *testing.T, c halfCloser, data []byte) []byte {
	t.Helper()
	defer c.Close()
	errc := make(chan error, 1)
	go func() {
		_, err := c.Write(data)
		if err == nil {
			err = c.CloseWrite()
		}
		errc <- err
	}()
	got, err := io.ReadAll(c)
	if err != nil {
		t.Errorf("read: %v", err)
	}
	if err := <-errc; err != nil {
		t.Errorf("write: %v", err)
	}
	return got
}

func randomData(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// echo copies everything back and half-closes at EOF.
func echo(c halfCloser) {
	defer c.Close()
	io.Copy(c, c)
	c.CloseWrite()
}

func TestNetcatBothDirections(t *testing.T) {
	requireLoopback(t)
	port := fmt.Sprint(randPort())
	l := start(t, strings.NewReader("from-listener\n"), "listening on", "vsock-netcat", "-l", "-v", port)
	c := start(t, strings.NewReader("from-client\n"), "", "vsock-netcat", "1", port)
	if code := c.wait(t); code != 0 {
		t.Fatalf("client exit %d", code)
	}
	if code := l.wait(t); code != 0 {
		t.Fatalf("listener exit %d", code)
	}
	if got := c.stdout.String(); got != "from-listener\n" {
		t.Errorf("client got %q", got)
	}
	if got := l.stdout.String(); got != "from-client\n" {
		t.Errorf("listener got %q", got)
	}
}

func TestNetcatFailures(t *testing.T) {
	requireLoopback(t)
	if code := start(t, nil, "", "vsock-netcat", "-z", "1", fmt.Sprint(randPort())).wait(t); code != 1 {
		t.Errorf("-z to closed port: exit %d, want 1", code)
	}
	if code := start(t, nil, "", "vsock-netcat", "-l", "-w", "300ms", fmt.Sprint(randPort())).wait(t); code != 1 {
		t.Errorf("-l -w timeout: exit %d, want 1", code)
	}
}

func TestTCPProxyParallel(t *testing.T) {
	requireLoopback(t)
	tl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tl.Close()
	go func() {
		for {
			c, err := tl.Accept()
			if err != nil {
				return
			}
			go echo(c.(*net.TCPConn))
		}
	}()
	port := randPort()
	start(t, nil, "listening on", "vsock-tcpproxy", "-port", fmt.Sprint(port), "-target", tl.Addr().String(), "-keepalive")

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := vsock.Dial(0, vsock.CIDLocal, port)
			if err != nil {
				t.Errorf("dial: %v", err)
				return
			}
			data := randomData(t, 1<<20)
			if got := roundTrip(t, c, data); sha256.Sum256(got) != sha256.Sum256(data) {
				t.Errorf("client %d: echo mismatch (%d of %d bytes)", i, len(got), len(data))
			}
		}()
	}
	wg.Wait()
}

func TestTCPListen(t *testing.T) {
	requireLoopback(t)
	port := randPort()
	vl, err := vsock.Listen(0, port, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer vl.Close()
	go func() {
		for {
			c, err := vl.Accept()
			if err != nil {
				return
			}
			go echo(c)
		}
	}()
	addr := freeTCPAddr(t)
	start(t, nil, "listening on", "vsock-tcplisten", "-listen", addr, "-cid", "1", "-port", fmt.Sprint(port), "-allow-ip", "127.0.0.1")

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	data := randomData(t, 4<<20)
	if got := roundTrip(t, c.(*net.TCPConn), data); sha256.Sum256(got) != sha256.Sum256(data) {
		t.Errorf("echo mismatch (%d of %d bytes)", len(got), len(data))
	}
}

func TestInetd(t *testing.T) {
	requireLoopback(t)
	script := `echo "cid=$VSOCK_REMOTE_CID local=$VSOCK_LOCAL_CID:$VSOCK_LOCAL_PORT"; tr a-z A-Z`
	for _, mode := range []string{"-direct=true", "-direct=false"} {
		t.Run(mode, func(t *testing.T) {
			port := randPort()
			start(t, nil, "listening on", "vsock-inetd", "-port", fmt.Sprint(port), mode, "--", "/bin/sh", "-c", script)
			for i := 0; i < 2; i++ { // two clients, also concurrently served
				c, err := vsock.Dial(0, vsock.CIDLocal, port)
				if err != nil {
					t.Fatal(err)
				}
				want := fmt.Sprintf("cid=1 local=1:%d\nHELLO %d\n", port, i)
				if got := string(roundTrip(t, c, []byte(fmt.Sprintf("hello %d\n", i)))); got != want {
					t.Errorf("got %q, want %q", got, want)
				}
			}
		})
	}
}

func TestInetdRejectsCID(t *testing.T) {
	requireLoopback(t)
	port := randPort()
	p := start(t, nil, "listening on", "vsock-inetd", "-port", fmt.Sprint(port), "-allow-cid", "3", "--", "/bin/cat")
	c, err := vsock.Dial(0, vsock.CIDLocal, port)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// The rejection may close the connection before or after this write, so
	// a write error is fine; no data may come back either way.
	c.Write([]byte("x"))
	if got, _ := io.ReadAll(c); len(got) != 0 {
		t.Errorf("rejected client got %q", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(p.stderr.String(), "CID 1 not allowed") {
		if time.Now().After(deadline) {
			t.Fatalf("no rejection logged:\n%s", p.stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
