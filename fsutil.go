package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var (
	errNotDir      = errors.New("not a directory")
	errUnsupported = errors.New("not supported on this platform yet")
)

func formatBase36(n int64) string { return strconv.FormatInt(n, 36) }

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func isDir(p string) bool { fi, err := os.Stat(p); return err == nil && fi.IsDir() }

func isFile(p string) bool { fi, err := os.Lstat(p); return err == nil && fi.Mode().IsRegular() }

func isNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

func isExistErr(err error) bool {
	return errors.Is(err, fs.ErrExist) || errors.Is(err, syscall.ENOTEMPTY)
}

// atomicWrite replaces path with data via a temp file in the same directory.
func atomicWrite(path string, data []byte, perm fs.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".ccanchor-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, perm)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// encodeJSON marshals like JSON.stringify: no HTML escaping of <, >, &.
func encodeJSON(v any, indent bool) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func writeJSONFile(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := encodeJSON(v, true)
	if err != nil {
		return err
	}
	return atomicWrite(path, append(b, '\n'), 0o644)
}

func countFiles(p string) int {
	fi, err := os.Lstat(p)
	if err != nil {
		return 0
	}
	if !fi.IsDir() {
		return 1
	}
	n := 0
	filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func filesEqual(a, b string) bool {
	fa, err1 := os.Stat(a)
	fb, err2 := os.Stat(b)
	if err1 != nil || err2 != nil || fa.Size() != fb.Size() {
		return false
	}
	da, err1 := os.ReadFile(a)
	db, err2 := os.ReadFile(b)
	return err1 == nil && err2 == nil && bytes.Equal(da, db)
}

func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(dst), ".ccanchor-*")
	if err != nil {
		return err
	}
	_, err = io.Copy(f, in)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(f.Name(), perm)
	}
	if err == nil {
		err = os.Rename(f.Name(), dst)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// backup copies path into ~/.claude/ccanchor/backups and keeps the newest 20 per file.
func backup(path string) string {
	if !exists(path) {
		return ""
	}
	dir := filepath.Join(stateDir(), "backups")
	base := strings.TrimPrefix(filepath.Base(path), ".")
	dst := filepath.Join(dir, base+"."+time.Now().Format("20060102-150405.000"))
	if err := copyFile(path, dst, 0o600); err != nil {
		return ""
	}
	ents, _ := os.ReadDir(dir)
	var mine []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), base+".") {
			mine = append(mine, e.Name())
		}
	}
	sort.Strings(mine)
	for len(mine) > 20 {
		os.Remove(filepath.Join(dir, mine[0]))
		mine = mine[1:]
	}
	return dst
}

func readHead(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, int64(n)))
	return string(b)
}
