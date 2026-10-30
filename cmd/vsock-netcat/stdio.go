package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sadphil/go-winvsocktools/internal/logging"
	"github.com/sadphil/go-winvsocktools/internal/relay"
)

// stdinPump reads stdin in a single goroutine for the life of the process.
// A blocking console or pipe read cannot be interrupted, so sessions take
// data from the pump instead of reading stdin themselves; a chunk read
// after one session ended is delivered to the next one (-l -k).
type stdinPump struct {
	data chan []byte
	err  error // set before data is closed
}

// stdin returns the process-wide stdin pump, starting it on first use so
// that stdin is left untouched in -e mode.
var stdin = sync.OnceValue(func() *stdinPump { return newStdinPump(os.Stdin) })

func newStdinPump(r io.Reader) *stdinPump {
	p := &stdinPump{data: make(chan []byte)}
	go func() {
		for {
			buf := make([]byte, 32*1024)
			n, err := r.Read(buf)
			if n > 0 {
				p.data <- buf[:n]
			}
			if err != nil {
				p.err = err
				close(p.data)
				return
			}
		}
	}()
	return p
}

var errSessionDone = errors.New("session finished")

// pumpReader reads from a stdinPump until the session is done.
type pumpReader struct {
	p       *stdinPump
	done    <-chan struct{}
	pending []byte
}

func (r *pumpReader) Read(b []byte) (int, error) {
	if len(r.pending) == 0 {
		select {
		case d, ok := <-r.p.data:
			if !ok {
				if errors.Is(r.p.err, io.EOF) {
					return 0, io.EOF
				}
				return 0, fmt.Errorf("read stdin: %w", r.p.err)
			}
			r.pending = d
		case <-r.done:
			return 0, errSessionDone
		}
	}
	n := copy(b, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

// relayStdio relays data from the pump to c and from c to out. EOF on the
// pump shuts down the sending side of c; quit >= 0 then also closes c after
// that delay. It returns when c has been closed by the peer (or by quit).
func relayStdio(log *logging.Logger, c relay.HalfCloser, p *stdinPump, out io.Writer, quit time.Duration) error {
	defer c.Close()
	done := make(chan struct{})
	inDone := make(chan struct{})
	var (
		inN      int64
		quitting atomic.Bool
	)
	go func() {
		defer close(inDone)
		var err error
		inN, err = relay.Copy(log, "stdin->vsock", c, &pumpReader{p: p, done: done})
		switch {
		case errors.Is(err, errSessionDone):
			return
		case err != nil:
			log.Errorf("stdin->vsock: %v", err)
			c.Close()
			return
		}
		log.Debugf("stdin->vsock: half-closing vsock side")
		c.CloseWrite()
		if quit < 0 {
			return
		}
		select {
		case <-time.After(quit):
			log.Debugf("closing connection %v after EOF on stdin (-q)", quit)
			quitting.Store(true)
			c.Close()
		case <-done:
		}
	}()

	outN, err := relay.Copy(log, "vsock->stdout", out, c)
	close(done)
	c.Close()
	<-inDone
	log.Infof("closed: stdin->vsock %d bytes, vsock->stdout %d bytes", inN, outN)
	if err != nil && !quitting.Load() {
		return fmt.Errorf("vsock->stdout: %w", err)
	}
	return nil
}
