// Command vsock-netcat is netcat for virtio vsock.
//
// Usage:
//
//	vsock-netcat [options] CID PORT        connect, relay stdin/stdout
//	vsock-netcat -l [options] PORT         accept one client, relay stdin/stdout
//	vsock-netcat -l -k [options] PORT      accept clients one after another
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sadphil/go-vsocktools/internal/child"
	"github.com/sadphil/go-vsocktools/internal/logging"
	"github.com/sadphil/go-vsocktools/internal/server"
	"github.com/sadphil/go-vsocktools/internal/version"
	"github.com/sadphil/go-vsocktools/internal/vsock"
)

const program = "vsock-netcat"

type options struct {
	server.Common
	verbosity verbosity
	listen    bool
	keep      bool
	zero      bool
	timeout   time.Duration
	quit      time.Duration
	allow     vsock.CIDList
	exec      string
	spec      child.Spec

	cid, port uint32
}

// verbosity counts -v flags; -vv counts twice.
type verbosity int

func (v *verbosity) String() string   { return strconv.Itoa(int(*v)) }
func (v *verbosity) IsBoolFlag() bool { return true }
func (v *verbosity) Set(s string) error {
	b, err := strconv.ParseBool(s)
	if b {
		*v++
	} else {
		*v = 0
	}
	return err
}

type doubleVerbosity struct{ v *verbosity }

func (d doubleVerbosity) String() string   { return "" }
func (d doubleVerbosity) IsBoolFlag() bool { return true }
func (d doubleVerbosity) Set(s string) error {
	if err := d.v.Set(s); err != nil {
		return err
	}
	return d.v.Set(s)
}

func (v verbosity) level() logging.Level {
	switch {
	case v >= 2:
		return logging.LevelDebug
	case v == 1:
		return logging.LevelInfo
	}
	return logging.LevelWarn
}

func main() {
	os.Exit(run())
}

func run() int {
	var opt options
	fs := flag.NewFlagSet(program, flag.ExitOnError)
	fs.BoolVar(&opt.listen, "l", false, "listen for an incoming connection instead of connecting")
	fs.BoolVar(&opt.keep, "k", false, "with -l: keep listening after a client disconnects (clients are served one at a time)")
	fs.BoolVar(&opt.zero, "z", false, "zero-I/O mode: connect and close immediately; exit status tells whether the port is open")
	fs.DurationVar(&opt.timeout, "w", 0, "connect timeout; with -l, how long to wait for the first client (0: no limit)")
	fs.DurationVar(&opt.quit, "q", -1, "after EOF on stdin, close the connection after this `delay` (default: wait for the peer to close)")
	fs.Var(&opt.allow, "allow-cid", "with -l: comma-separated peer `CIDs` allowed to connect; may be repeated (default: any)")
	fs.StringVar(&opt.exec, "e", "", "run `program` with its stdin/stdout connected to the peer")
	opt.spec.RegisterFlags(fs, program)
	fs.Var(&opt.verbosity, "v", "verbose: report connections; -vv also logs every read/write with its size")
	fs.Var(doubleVerbosity{&opt.verbosity}, "vv", "same as -v -v")
	opt.Common.RegisterBaseFlags(fs)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage:\n"+
			"  %[1]s [options] CID PORT     connect to CID:PORT\n"+
			"  %[1]s -l [options] PORT      listen on PORT\n\n"+
			"Relays stdin and stdout over a vsock connection. EOF on stdin shuts down\n"+
			"the sending side; the program exits when the peer closes.\n\nOptions:\n", program)
		fs.PrintDefaults()
	}
	args := parseInterleaved(fs, os.Args[1:])
	if opt.Version {
		fmt.Println(program, version.Version)
		return 0
	}

	log := logging.New(os.Stderr, program, opt.verbosity.level())
	if err := opt.validate(args); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", program, err)
		fs.Usage()
		return 2
	}
	if opt.exec != "" {
		if err := opt.spec.SetProgram([]string{opt.exec}); err != nil {
			log.Errorf("%v", err)
			return 1
		}
		log.Infof("running %s", &opt.spec)
	}
	af, err := opt.ResolveAF(log)
	if err != nil {
		log.Errorf("%v", err)
		return 1
	}
	if opt.listen {
		return listen(log, &opt, af)
	}
	return connect(log, &opt, af)
}

// parseInterleaved parses flags that may follow positional arguments and
// returns the positional arguments.
func parseInterleaved(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for {
		fs.Parse(args)
		args = fs.Args()
		if len(args) == 0 {
			return pos
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func (o *options) validate(args []string) error {
	parse := func(name, s string) (uint32, error) {
		v, err := strconv.ParseUint(s, 10, 32)
		if err != nil || v == vsock.CIDAny {
			return 0, fmt.Errorf("invalid %s %q", name, s)
		}
		return uint32(v), nil
	}
	var err error
	switch {
	case o.listen && len(args) == 1:
		o.port, err = parse("port", args[0])
	case !o.listen && len(args) == 2:
		if o.cid, err = parse("CID", args[0]); err == nil {
			o.port, err = parse("port", args[1])
		}
	default:
		return errors.New("wrong number of arguments")
	}
	if err != nil {
		return err
	}
	if o.port == 0 {
		return errors.New("port must not be 0")
	}
	switch {
	case o.keep && !o.listen:
		return errors.New("-k requires -l")
	case len(o.allow) > 0 && !o.listen:
		return errors.New("-allow-cid requires -l")
	case o.zero && o.listen:
		return errors.New("-z cannot be combined with -l")
	case o.zero && o.exec != "":
		return errors.New("-z cannot be combined with -e")
	}
	return o.spec.Validate()
}

func connect(log *logging.Logger, opt *options, af int) int {
	target := vsock.Addr{CID: opt.cid, Port: opt.port}
	c, err := dial(af, target, opt.timeout)
	if err != nil {
		log.Errorf("connect to vsock %s: %v", target, err)
		return 1
	}
	log.Infof("connected to vsock %s from %s", target, c.LocalAddr())
	if opt.zero {
		c.Close()
		return 0
	}
	return session(log, opt, c)
}

// dial connects to target, giving up after timeout if it is positive.
func dial(af int, target vsock.Addr, timeout time.Duration) (*vsock.Conn, error) {
	if timeout <= 0 {
		return vsock.Dial(af, target.CID, target.Port)
	}
	type result struct {
		c   *vsock.Conn
		err error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := vsock.Dial(af, target.CID, target.Port)
		ch <- result{c, err}
	}()
	select {
	case r := <-ch:
		return r.c, r.err
	case <-time.After(timeout):
		go func() {
			if r := <-ch; r.c != nil {
				r.c.Close()
			}
		}()
		return nil, fmt.Errorf("timed out after %v", timeout)
	}
}

func listen(log *logging.Logger, opt *options, af int) int {
	ln, err := vsock.Listen(af, opt.port, 16)
	if err != nil {
		log.Errorf("listen on vsock port %d: %v", opt.port, err)
		return 1
	}
	log.Infof("listening on vsock %s, allowed peer CIDs: %s", ln.Addr(), server.AllowedCIDs(opt.allow))

	var (
		mu       sync.Mutex
		accepted bool
		timedOut atomic.Bool
		code     atomic.Int32
	)
	code.Store(1) // no client served
	if opt.timeout > 0 {
		time.AfterFunc(opt.timeout, func() {
			mu.Lock()
			defer mu.Unlock()
			if !accepted {
				timedOut.Store(true)
				ln.Close()
			}
		})
	}
	loop := server.VsockLoop(ln, opt.allow, true, func(log *logging.Logger, c *vsock.Conn) {
		mu.Lock()
		accepted = true
		mu.Unlock()
		code.Store(int32(session(log, opt, c)))
		if !opt.keep {
			ln.Close()
		}
	})
	if err := server.Serve(log, loop); err != nil {
		log.Errorf("%v", err)
		return 1
	}
	if timedOut.Load() {
		log.Errorf("no connection within %v", opt.timeout)
		return 1
	}
	return int(code.Load())
}

// session serves one connection with a child program or with stdin/stdout,
// and returns the exit status for it. c is closed on return.
func session(log *logging.Logger, opt *options, c *vsock.Conn) int {
	if opt.spec.Path != "" {
		code, err := child.Run(log, &opt.spec, c)
		if err != nil {
			log.Errorf("%v", err)
			return 1
		}
		log.Infof("child exited with code %d", code)
		return code
	}
	// After the peer finishes sending, keep relaying piped stdin so a peer
	// that half-closes first gets all of it; a console user or the next -k
	// client must not wait for stdin, though.
	waitStdin := !opt.keep && !stdinIsConsole()
	if err := relayStdio(log, c, stdin(), os.Stdout, opt.quit, waitStdin); err != nil {
		log.Errorf("%v", err)
		return 1
	}
	return 0
}
