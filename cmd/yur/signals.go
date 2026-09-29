package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync/atomic"
)

// Ctrl+C stops a command at a place where the state can be kept whole: between files, not inside one.
// A second one stops at once.
var interruptFlag atomic.Bool

var errInterrupted = errors.New("stopped by Ctrl+C: nothing was saved, and yur status shows the files as they are now")

func watchInterrupt(release func()) {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt)
	go func() {
		<-ch
		interruptFlag.Store(true)
		fmt.Fprintln(os.Stderr, paint(dim, "\nStopping at a safe place (Ctrl+C again stops at once)"))
		<-ch
		release()
		restoreTerminal()
		os.Exit(130)
	}()
}

func interrupted() bool { return interruptFlag.Load() }
