//go:build linux || darwin

package main

import (
	"os"
	"syscall"
)

var interruptSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGINT}
