//go:build linux

package child

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/sadphil/go-vsocktools/internal/logging"
	"github.com/sadphil/go-vsocktools/internal/vsock"
)

// The socket is a plain file descriptor, so classic inetd semantics work.
const defaultDirect = true

// runDirect starts the child with the socket as its standard input and
// output, waits for it to exit and returns its exit code.
func runDirect(log *logging.Logger, s *Spec, env []string, c *vsock.Conn) (int, error) {
	f, err := c.File()
	if err != nil {
		return 0, fmt.Errorf("duplicate socket: %w", err)
	}
	defer f.Close()
	cmd := &exec.Cmd{Path: s.Path, Args: s.Args, Env: env, Stdin: f, Stdout: f}
	switch s.Stderr {
	case StderrInherit:
		cmd.Stderr = os.Stderr
	case StderrSocket:
		cmd.Stderr = f
	}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start %s: %w", s.Path, err)
	}
	log.Infof("started child pid %d", cmd.Process.Pid)
	var exitErr *exec.ExitError
	if err := cmd.Wait(); err != nil && !errors.As(err, &exitErr) {
		return 0, fmt.Errorf("wait for pid %d: %w", cmd.Process.Pid, err)
	}
	return cmd.ProcessState.ExitCode(), nil
}
