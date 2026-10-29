//go:build !windows

package chrome

import (
	"fmt"
	"os"
	"syscall"
)

// terminate asks the browser to quit. Chrome treats SIGTERM like the user
// choosing Quit: it closes every window and flushes its profile to disk.
func terminate(p *os.Process) error {
	if err := p.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("signalling chrome: %w", err)
	}

	return nil
}
