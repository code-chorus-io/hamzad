//go:build windows

package proxy

import "golang.org/x/sys/windows"

// errAddrInUse is the error Winsock returns when a bind hits a port that is
// already taken.
//
// It comes from x/sys rather than syscall because the standard library's
// Windows syscall package defines only a handful of WSA codes and this is not
// one of them — syscall.EADDRINUSE there belongs to the generic errno block and
// never matches what a socket operation reports.
var errAddrInUse error = windows.WSAEADDRINUSE
