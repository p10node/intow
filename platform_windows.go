//go:build windows

package main

import (
	"os"
	"time"
)

const scanFallback = false

func platformResolve(e *Entry) string { return "" }

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	p.Release()
	return true
}

func withLock(timeout time.Duration, fn func() error) error {
	if err := os.MkdirAll(stateDir(), 0o755); err != nil {
		return err
	}
	return fn()
}
