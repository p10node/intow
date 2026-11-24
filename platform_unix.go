//go:build darwin || linux

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Paths are compared exactly: that is how Claude Code keys its data.
const foldCase = false

// respell and variants deal with the several spellings one folder has on
// Windows; here a path has one.
func respell(p, like string) string { return p }

func variants(old, new string) [][2]string { return [][2]string{{old, new}} }

func isCrossDevice(err error) bool { return errors.Is(err, syscall.EXDEV) }

// crossDeviceMove hands over to the system mv, which keeps symlinks,
// permissions and timestamps.
func crossDeviceMove(src, dst string) error {
	if out, err := exec.Command("mv", "--", src, dst).CombinedOutput(); err != nil {
		return fmt.Errorf("mv: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func hookCommand(bin string) string { return shellQuote(bin) + " hook" }

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// withLock serialises intow runs (several Claude sessions may start at once).
func withLock(timeout time.Duration, fn func() error) error {
	if err := os.MkdirAll(stateDir(), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(lockPath(), os.O_CREATE|os.O_RDWR, 0o644)
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
			return errors.New("another intow is running")
		}
		time.Sleep(100 * time.Millisecond)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}
