//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly || solaris

package main

import (
	"os"
	"syscall"
)

func reloadSignals() []os.Signal {
	return []os.Signal{syscall.SIGHUP}
}
