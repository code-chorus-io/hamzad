//go:build !windows

package proxy

import "syscall"

// errAddrInUse is the errno the kernel returns when a bind hits a port that is
// already taken.
var errAddrInUse error = syscall.EADDRINUSE
