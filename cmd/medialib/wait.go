package main

import (
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/demogest/medialib/internal/proc"
	"github.com/demogest/medialib/internal/update"
)

// waitForInterrupt returns on Ctrl+C (or SIGTERM), or when done is closed.
func waitForInterrupt(done <-chan struct{}) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(c)
	select {
	case <-c:
	case <-done:
	}
}

// waitForPredecessor lets a copy started by an update wait until the one it replaces has exited: that one still
// holds the port, the window and the single-instance lock.
func waitForPredecessor() {
	v := os.Getenv(update.WaitEnv)
	if v == "" {
		return
	}
	os.Unsetenv(update.WaitEnv)
	if pid, err := strconv.Atoi(v); err == nil && pid > 0 {
		proc.WaitExit(pid, 30*time.Second)
	}
}

// closeWindow closes the desktop window, which ends the program (an update restarting it); nil when there is none.
var closeWindow func()
