//go:build darwin || linux

package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// withLock serialises ccanchor runs (several Claude sessions may start at once).
func withLock(timeout time.Duration, fn func() error) error {
	if err := os.MkdirAll(stateDir(), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(stateDir(), "lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		if time.Now().After(deadline) {
			return errors.New("another ccanchor is running")
		}
		time.Sleep(100 * time.Millisecond)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}
