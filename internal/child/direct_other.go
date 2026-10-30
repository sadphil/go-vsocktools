//go:build !windows

package child

import (
	"errors"

	"github.com/sadphil/go-winvsocktools/internal/logging"
	"github.com/sadphil/go-winvsocktools/internal/vsock"
)

func runDirect(log *logging.Logger, s *Spec, env []string, c *vsock.Conn) (int, error) {
	return 0, errors.New("passing the socket handle is only supported on windows; use -pipe")
}
