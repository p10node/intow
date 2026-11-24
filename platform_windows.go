//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// Paths are compared without regard to case.
const foldCase = true

func init() {
	tempPrefixes = nil
	for _, d := range []string{os.TempDir(), os.Getenv("TEMP"), os.Getenv("TMP")} {
		if d != "" {
			tempPrefixes = append(tempPrefixes, strings.TrimRight(filepath.Clean(d), sep)+sep)
		}
	}
}

func hasDrive(p string) bool {
	return len(p) >= 2 && p[1] == ':' && (p[0]|0x20) >= 'a' && (p[0]|0x20) <= 'z'
}

func flipDrive(p string) string {
	if !hasDrive(p) {
		return p
	}
	return string(p[0]^0x20) + p[1:]
}

// respell gives p the drive-letter case of like. Programs disagree about it
// (C:\ in a terminal, c:\ from VS Code), and Claude Code keys its data by the
// exact string, so each spelling keeps its own.
func respell(p, like string) string {
	if hasDrive(p) && hasDrive(like) && (p[0]^like[0])&0x20 != 0 {
		return flipDrive(p)
	}
	return p
}

// variants lists the ways a file may spell the pair old -> new: as given, with
// the other drive-letter case, and each with forward slashes.
func variants(old, new string) [][2]string {
	out := [][2]string{{old, new}}
	add := func(o, n string) {
		for _, v := range out {
			if v[0] == o {
				return
			}
		}
		out = append(out, [2]string{o, n})
	}
	add(flipDrive(old), flipDrive(new))
	for _, v := range out[:len(out):len(out)] {
		add(filepath.ToSlash(v[0]), filepath.ToSlash(v[1]))
	}
	return out
}

func isCrossDevice(err error) bool { return errors.Is(err, windows.ERROR_NOT_SAME_DEVICE) }

func crossDeviceMove(src, dst string) error {
	return errors.New("moving a folder to another drive is not supported here: move it with Explorer, then run `ccanchor link " +
		src + " " + dst + "`")
}

// hookCommand is the settings.json command for the hook. Claude Code runs
// hooks through Git Bash, which wants forward slashes.
func hookCommand(bin string) string {
	p := filepath.ToSlash(bin)
	if strings.ContainsAny(p, " ()&'") {
		p = `"` + p + `"`
	}
	return p + " hook"
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer windows.CloseHandle(h)
	var code uint32
	if windows.GetExitCodeProcess(h, &code) != nil {
		return true
	}
	return code == 259 // STILL_ACTIVE
}

// withLock serialises ccanchor runs (several Claude sessions may start at once).
func withLock(timeout time.Duration, fn func() error) error {
	if err := os.MkdirAll(stateDir(), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(lockPath(), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	h := windows.Handle(f.Fd())
	ol := new(windows.Overlapped)
	deadline := time.Now().Add(timeout)
	for {
		err := windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) && !errors.Is(err, windows.ERROR_IO_PENDING) {
			return err
		}
		if time.Now().After(deadline) {
			return errors.New("another ccanchor is running")
		}
		time.Sleep(100 * time.Millisecond)
	}
	defer windows.UnlockFileEx(h, 0, 1, 0, ol)
	return fn()
}
