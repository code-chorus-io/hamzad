package chrome

import (
	"os/exec"
	"time"
)

// shutdownGrace is how long a cancelled session gives Chrome to quit on its own
// before it is killed. Chrome writes cookies, Local Storage and the session
// file on a normal quit, and it needs those seconds: the bundle packed after
// the session ends is only as good as what reached the disk.
const shutdownGrace = 15 * time.Second

// stopGracefully makes a ctx cancellation ask Chrome to quit instead of killing
// it outright. exec.CommandContext defaults to SIGKILL, which drops whatever
// Chrome still holds in memory and leaves its ProcessSingleton locks behind —
// and the bundle saved straight afterwards would carry that half-written state
// to every teammate who restores it. SIGKILL stays as the fallback, after
// shutdownGrace, for a browser that does not answer.
func stopGracefully(cmd *exec.Cmd) {
	cmd.Cancel = func() error { return terminate(cmd.Process) }
	cmd.WaitDelay = shutdownGrace
}
