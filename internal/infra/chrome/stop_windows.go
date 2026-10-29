//go:build windows

package chrome

import (
	"fmt"
	"os"
)

// terminate stops the browser. Windows has no SIGTERM to send a console-less
// child, so this is a kill, as it was before graceful shutdown existed.
func terminate(p *os.Process) error {
	if err := p.Kill(); err != nil {
		return fmt.Errorf("killing chrome: %w", err)
	}

	return nil
}
