// Package child runs a program for a vsock connection with the program's
// standard input and output connected to the client, either by handing it
// the socket handle itself or through anonymous pipes relayed by the caller.
package child

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sadphil/go-winvsocktools/internal/logging"
	"github.com/sadphil/go-winvsocktools/internal/relay"
	"github.com/sadphil/go-winvsocktools/internal/vsock"
)

// Child stderr destinations.
const (
	StderrInherit = "inherit"
	StderrSocket  = "socket"
	StderrNull    = "null"
)

// Spec describes the program to run and how to connect it.
type Spec struct {
	Path   string   // resolved program path
	Args   []string // argv; Args[0] as given by the user
	Pipe   bool     // relay through pipes instead of passing the socket handle
	Stderr string   // one of the Stderr* constants
}

// RegisterFlags registers -pipe and -stderr on fs. owner is the name of the
// program that relays data and owns the inherited stderr.
func (s *Spec) RegisterFlags(fs *flag.FlagSet, owner string) {
	fs.BoolVar(&s.Pipe, "pipe", false, "connect the child through anonymous pipes relayed by "+owner+
		" instead of handing it the socket handle (needed for byte counts in verbose mode)")
	fs.StringVar(&s.Stderr, "stderr", StderrInherit, "child stderr: `mode` inherit ("+owner+"'s stderr), socket or null")
}

// SetProgram resolves argv[0] through PATH and sets the program to run.
func (s *Spec) SetProgram(argv []string) error {
	if len(argv) == 0 {
		return errors.New("no program given")
	}
	path, err := exec.LookPath(argv[0])
	if err != nil && !errors.Is(err, exec.ErrDot) {
		return err
	}
	if s.Path, err = filepath.Abs(path); err != nil {
		return err
	}
	s.Args = argv
	return nil
}

// Validate checks the options.
func (s *Spec) Validate() error {
	switch s.Stderr {
	case StderrInherit, StderrSocket, StderrNull:
		return nil
	}
	return fmt.Errorf("invalid -stderr %q: want inherit, socket or null", s.Stderr)
}

// String describes the command and connection mode for logging.
func (s *Spec) String() string {
	cmd := s.Path + " " + strings.Join(s.Args[1:], " ")
	mode := "socket handle as stdin/stdout"
	if s.Pipe {
		mode = "pipes relayed to the socket"
	}
	return fmt.Sprintf("%s via %s, child stderr: %s", strings.TrimSpace(cmd), mode, s.Stderr)
}

// Run starts the program for c, waits for it to exit and returns its exit
// code. The child environment gets VSOCK_REMOTE_CID, VSOCK_REMOTE_PORT,
// VSOCK_LOCAL_CID and VSOCK_LOCAL_PORT. c is closed when Run returns.
func Run(log *logging.Logger, s *Spec, c *vsock.Conn) (int, error) {
	defer c.Close()
	local, remote := c.LocalAddr(), c.RemoteAddr()
	env := append(os.Environ(),
		"VSOCK_REMOTE_CID="+strconv.FormatUint(uint64(remote.CID), 10),
		"VSOCK_REMOTE_PORT="+strconv.FormatUint(uint64(remote.Port), 10),
		"VSOCK_LOCAL_CID="+strconv.FormatUint(uint64(local.CID), 10),
		"VSOCK_LOCAL_PORT="+strconv.FormatUint(uint64(local.Port), 10),
	)
	if s.Pipe {
		return runPiped(log, s, env, c)
	}
	log.Debugf("socket handle passed to the child directly; byte counts unavailable (use -pipe)")
	return runDirect(log, s, env, c)
}

// runPiped starts the child with anonymous pipes as stdin/stdout and relays
// them to and from the socket.
func runPiped(log *logging.Logger, s *Spec, env []string, c *vsock.Conn) (int, error) {
	inR, inW, err := os.Pipe()
	if err != nil {
		return 0, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return 0, err
	}
	cmd := &exec.Cmd{Path: s.Path, Args: s.Args, Env: env, Stdin: inR, Stdout: outW}
	switch s.Stderr {
	case StderrInherit:
		cmd.Stderr = os.Stderr
	case StderrSocket:
		cmd.Stderr = outW
	}
	err = cmd.Start()
	// The child owns its ends now (or failed to start).
	inR.Close()
	outW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		return 0, fmt.Errorf("start %s: %w", s.Path, err)
	}
	log.Infof("started child pid %d", cmd.Process.Pid)

	inDone := make(chan int64, 1)
	go func() {
		n, err := relay.Copy(log, "vsock->stdin", inW, c)
		if err != nil {
			log.Debugf("vsock->stdin: %v", err)
		}
		inW.Close()
		inDone <- n
	}()

	// stdout reaches EOF once the child and any inheriting descendants exit.
	outN, err := relay.Copy(log, "stdout->vsock", c, outR)
	outR.Close()
	if err != nil {
		log.Debugf("stdout->vsock: %v", err)
	} else {
		c.CloseWrite()
	}
	werr := cmd.Wait()
	c.Close() // unblocks the stdin relay if the client is still connected
	inN := <-inDone
	log.Infof("closed: vsock->stdin %d bytes, stdout->vsock %d bytes", inN, outN)

	var exitErr *exec.ExitError
	if werr != nil && !errors.As(werr, &exitErr) {
		return 0, fmt.Errorf("wait for pid %d: %w", cmd.Process.Pid, werr)
	}
	return cmd.ProcessState.ExitCode(), nil
}
