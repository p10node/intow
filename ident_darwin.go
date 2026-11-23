//go:build darwin

package main

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const exeSuffix = ""

func statIdent(path string) (Ident, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return Ident{}, err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return Ident{}, errNotDir
	}
	return Ident{Dev: uint64(uint32(st.Dev)), Ino: st.Ino, Birth: st.Birthtimespec.Nano()}, nil
}

// /.vol is authoritative on macOS, so there is no need to scan the disk.
const scanFallback = false

// platformResolve finds a directory's current path from its (device, inode)
// through /.vol/<dev>/<ino> and fcntl(F_GETPATH).
func platformResolve(e *Entry) string {
	devs := []uint64{e.Dev}
	// Device numbers can change across reboots or remounts; also try the
	// volume that holds the old location now.
	if id, err := statIdent(nearestExisting(e.Path)); err == nil && id.Dev != e.Dev {
		devs = append(devs, id.Dev)
	}
	for _, dev := range devs {
		p := volLookup(dev, e.Ino)
		if p == "" {
			continue
		}
		id, err := statIdent(p)
		if err != nil || id.Ino != e.Ino {
			continue
		}
		if e.Birth != 0 && id.Birth != e.Birth {
			continue // inode number reused by an unrelated directory
		}
		return p
	}
	return ""
}

func volLookup(dev, ino uint64) string {
	fd, err := unix.Open(fmt.Sprintf("/.vol/%d/%d", dev, ino), unix.O_RDONLY, 0)
	if err != nil {
		return ""
	}
	defer unix.Close(fd)
	buf := make([]byte, unix.PathMax)
	_, _, errno := unix.Syscall(unix.SYS_FCNTL, uintptr(fd), uintptr(unix.F_GETPATH), uintptr(unsafe.Pointer(&buf[0])))
	if errno != 0 {
		return ""
	}
	for i, c := range buf {
		if c == 0 {
			return string(buf[:i])
		}
	}
	return ""
}
