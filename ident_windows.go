//go:build windows

package main

const exeSuffix = ".exe"

// TODO(v0.2): NTFS file IDs via GetFileInformationByHandle, resolved with
// OpenFileById + GetFinalPathNameByHandle. Until then `mv` and `link` work,
// automatic following does not.
func statIdent(path string) (Ident, error) { return Ident{}, errUnsupported }
