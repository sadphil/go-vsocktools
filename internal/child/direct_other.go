//go:build !windows && !linux

package child

import (
	"errors"

	"github.com/sadphil/go-vsocktools/internal/logging"
	"github.com/sadphil/go-vsocktools/internal/vsock"
)

const defaultDirect = false

func runDirect(log *logging.Logger, s *Spec, env []string, c *vsock.Conn) (int, error) {
	return 0, errors.New("passing the socket is not supported on this platform; use -direct=false")
}
