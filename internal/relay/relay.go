// Package relay copies data between connections, accounting for (and in
// verbose mode logging) every read and write.
package relay

import (
	"errors"
	"io"
	"sync"

	"github.com/sadphil/go-winvsocktools/internal/logging"
)

const bufSize = 64 * 1024

// Copy copies from src to dst until src returns EOF or an error occurs. It
// returns the number of bytes written to dst. Reaching EOF is not an error.
// In verbose mode each read and write is logged with its size, tagged by dir
// (for example "vsock->tcp").
func Copy(log *logging.Logger, dir string, dst io.Writer, src io.Reader) (int64, error) {
	buf := make([]byte, bufSize)
	var total int64
	for {
		nr, rerr := src.Read(buf)
		if nr > 0 {
			log.Debugf("%s: read %d bytes", dir, nr)
			nw, werr := dst.Write(buf[:nr])
			if nw > 0 {
				total += int64(nw)
			}
			log.Debugf("%s: wrote %d bytes (total %d)", dir, nw, total)
			if werr != nil {
				return total, werr
			}
			if nw != nr {
				return total, io.ErrShortWrite
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				log.Debugf("%s: EOF after %d bytes", dir, total)
				return total, nil
			}
			return total, rerr
		}
	}
}

// HalfCloser is a full-duplex stream whose sending side can be shut down
// independently (net.TCPConn and vsock.Conn both qualify).
type HalfCloser interface {
	io.ReadWriteCloser
	CloseWrite() error
}

// Result summarises a Join.
type Result struct {
	AtoB, BtoA int64 // bytes delivered in each direction
	Err        error // first error that aborted the relay, nil on clean close
}

// Join relays data in both directions between a and b until both directions
// reach EOF or either fails. EOF in one direction is propagated as a
// half-close (CloseWrite) of the other end; an error aborts both directions.
// Both a and b are closed when Join returns.
func Join(log *logging.Logger, aName string, a HalfCloser, bName string, b HalfCloser) Result {
	var (
		res     Result
		mu      sync.Mutex
		aborted bool
		wg      sync.WaitGroup
	)
	abort := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if aborted {
			return
		}
		aborted = true
		res.Err = err
		a.Close()
		b.Close()
	}
	run := func(srcName, dstName string, dst, src HalfCloser, n *int64) {
		dir := srcName + "->" + dstName
		defer wg.Done()
		var err error
		*n, err = Copy(log, dir, dst, src)
		if err != nil {
			log.Debugf("%s: %v", dir, err)
			abort(err)
			return
		}
		log.Debugf("%s: half-closing %s side", dir, dstName)
		if err := dst.CloseWrite(); err != nil {
			abort(err)
		}
	}
	wg.Add(2)
	go run(aName, bName, b, a, &res.AtoB)
	go run(bName, aName, a, b, &res.BtoA)
	wg.Wait()
	abort(nil)
	return res
}
