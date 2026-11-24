//go:build linux

package main

import (
	"syscall"

	"golang.org/x/sys/unix"
)

const exeSuffix = ""

// statIdent uses statx for the birth time (ext4, btrfs, xfs, tmpfs on 4.11+).
// Without one, ccanchor never follows a folder automatically, because Linux
// reuses inode numbers quickly.
func statIdent(path string) (Ident, error) {
	var sx unix.Statx_t
	err := unix.Statx(unix.AT_FDCWD, path, 0, unix.STATX_TYPE|unix.STATX_INO|unix.STATX_BTIME, &sx)
	if err != nil {
		if err == unix.ENOSYS {
			var st syscall.Stat_t
			if err := syscall.Stat(path, &st); err != nil {
				return Ident{}, err
			}
			if st.Mode&syscall.S_IFMT != syscall.S_IFDIR {
				return Ident{}, errNotDir
			}
			return Ident{Dev: uint64(st.Dev), Ino: st.Ino}, nil
		}
		return Ident{}, err
	}
	if uint32(sx.Mode)&unix.S_IFMT != unix.S_IFDIR {
		return Ident{}, errNotDir
	}
	id := Ident{Dev: unix.Mkdev(sx.Dev_major, sx.Dev_minor), Ino: sx.Ino}
	if sx.Mask&unix.STATX_BTIME != 0 {
		id.Birth = sx.Btime.Sec*1e9 + int64(sx.Btime.Nsec)
	}
	return id, nil
}

// caseRename: Linux filesystems are case-sensitive, so a folder that is still
// reachable by its recorded name has not been renamed.
func caseRename(old, new string) bool { return false }

const followsCaseRenames = false
