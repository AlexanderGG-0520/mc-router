//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly || solaris || windows)

package main

import "os"

func reloadSignals() []os.Signal {
	return nil
}
