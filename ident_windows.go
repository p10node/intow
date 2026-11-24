//go:build windows

package main

import (
	"encoding/binary"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const exeSuffix = ".exe"

// Nothing to scan for: NTFS and ReFS find a directory by its file ID.
const scanFallback = false

const shareAll = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE

// fileIDInfo is FILE_ID_INFO. ReFS (Dev Drive) identifies files by 128 bits;
// on NTFS the upper 64 are zero.
type fileIDInfo struct {
	VolumeSerialNumber uint64
	FileID             [16]byte
}

// fileIDDescriptor is FILE_ID_DESCRIPTOR, the argument of OpenFileById.
type fileIDDescriptor struct {
	Size uint32
	Type uint32 // 0: 64-bit file ID, 2: 128-bit (extended) file ID
	ID   [16]byte
}

var procOpenFileByID = windows.NewLazySystemDLL("kernel32.dll").NewProc("OpenFileById")

// openForInfo opens a file or directory to ask about it. That needs no access
// right; FILE_FLAG_BACKUP_SEMANTICS is what allows opening a directory.
func openForInfo(path string) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(p, 0, shareAll, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
}

func identOf(h windows.Handle) (Ident, error) {
	var bh windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &bh); err != nil {
		return Ident{}, err
	}
	if bh.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return Ident{}, errNotDir
	}
	id := Ident{
		Dev:   uint64(bh.VolumeSerialNumber),
		Ino:   uint64(bh.FileIndexHigh)<<32 | uint64(bh.FileIndexLow),
		Birth: bh.CreationTime.Nanoseconds(),
	}
	var x fileIDInfo
	if windows.GetFileInformationByHandleEx(h, windows.FileIdInfo, (*byte)(unsafe.Pointer(&x)), uint32(unsafe.Sizeof(x))) == nil {
		lo, hi := binary.LittleEndian.Uint64(x.FileID[:8]), binary.LittleEndian.Uint64(x.FileID[8:])
		if lo != 0 || hi != 0 {
			id.Ino, id.Hi = lo, hi
		}
	}
	return id, nil
}

// statIdent is the directory's file ID, which NTFS and ReFS keep when it is
// moved or renamed within a volume, with its creation time against reuse.
// FAT and exFAT derive the ID from the directory entry, so it changes with a
// rename and such folders are simply never found again.
func statIdent(path string) (Ident, error) {
	h, err := openForInfo(path)
	if err != nil {
		return Ident{}, err
	}
	defer windows.CloseHandle(h)
	return identOf(h)
}

// platformResolve finds a directory's current path from its file ID with
// OpenFileById and GetFinalPathNameByHandle.
func platformResolve(e *Entry) string {
	if procOpenFileByID.Find() != nil {
		return ""
	}
	// Windows only takes the volume from the hint, so the first try settles
	// it. Wine looks for the ID below the hint, so widen it step by step.
	for dir := nearestExisting(e.Path); ; {
		if p := resolveFrom(dir, e); p != "" {
			return p
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// resolveFrom asks the volume that hintDir is on for the directory with e's file ID.
func resolveFrom(hintDir string, e *Entry) string {
	hint, err := openForInfo(hintDir)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(hint)
	d := fileIDDescriptor{Size: uint32(unsafe.Sizeof(fileIDDescriptor{}))}
	binary.LittleEndian.PutUint64(d.ID[:8], e.Ino)
	if e.Hi != 0 {
		d.Type = 2
		binary.LittleEndian.PutUint64(d.ID[8:], e.Hi)
	}
	r, _, _ := procOpenFileByID.Call(uintptr(hint), uintptr(unsafe.Pointer(&d)), 0, uintptr(shareAll), 0,
		uintptr(windows.FILE_FLAG_BACKUP_SEMANTICS))
	h := windows.Handle(r)
	if h == 0 || h == windows.InvalidHandle {
		return ""
	}
	defer windows.CloseHandle(h)
	if id, err := identOf(h); err != nil || !e.is(id) {
		return "" // not a directory any more, or the ID was reused
	}
	buf := make([]uint16, 1024)
	n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
	if err == nil && int(n) > len(buf) {
		buf = make([]uint16, n)
		n, err = windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
	}
	if err != nil || n == 0 || int(n) > len(buf) {
		return ""
	}
	p := windows.UTF16ToString(buf[:n])
	switch {
	case strings.HasPrefix(p, `\\?\UNC\`):
		p = `\\` + p[len(`\\?\UNC\`):]
	case strings.HasPrefix(p, `\\?\`):
		p = p[len(`\\?\`):]
	}
	return respell(p, e.Path)
}

// caseRename: Windows paths are case-insensitive and programs spell the same
// folder differently (drive letter, typed case), so a recorded path that still
// leads to the folder is as good as any other spelling.
func caseRename(old, new string) bool { return false }

const followsCaseRenames = false
