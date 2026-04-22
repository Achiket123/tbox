// internal/engine/detach_linux.go
//go:build linux

package engine

import "syscall"

// detachSysProcAttr returns a SysProcAttr that places the child in a new
// session (Setsid), detaching it from the parent's controlling terminal.
// On Android/Linux this is the correct and sufficient mechanism to daemonise
// a process without a double-fork: the child becomes a session leader with no
// controlling terminal, so SIGHUP and SIGINT from the parent's terminal do
// not propagate to it.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setsid: true,
	}
}
	