package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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

// isRegular is isFile for Claude's own files, which may be symlinks into a
// dotfiles folder (stow, chezmoi, home-manager, per-profile config dirs).
func isRegular(p string) bool { fi, err := os.Stat(p); return err == nil && fi.Mode().IsRegular() }

// resolveLink returns the file a write to path has to land on: path itself,
// or what it points at when it is a symlink, even if that does not exist yet.
func resolveLink(path string) string {
	if fi, err := os.Lstat(path); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return path
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	for i := 0; i < 40; i++ { // dangling: follow the chain by hand
		target, err := os.Readlink(path)
		if err != nil {
			break
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		path = target
	}
	return path
}

// sameFile reports whether a and b are one file or directory under two names,
// as with a case-only rename on a case-insensitive volume. Nothing may ever be
// "merged" from one into the other: removing the source removes both.
func sameFile(a, b string) bool {
	fa, err1 := os.Stat(a)
	fb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(fa, fb)
}

func isNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

func isExistErr(err error) bool {
	return errors.Is(err, fs.ErrExist) || errors.Is(err, syscall.ENOTEMPTY)
}

// atomicWrite replaces path with data via a temp file in the same directory.
// A symlink stays a symlink: the file it points at is the one replaced.
func atomicWrite(path string, data []byte, perm fs.FileMode) error {
	path = resolveLink(path)
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

// claudeLockWait is how long to wait for Claude Code to finish a write.
var claudeLockWait = 2 * time.Second

// lockClaudeFile takes the lock Claude Code itself holds while it rewrites one
// of its JSON files: a directory "<real path>.lock" (the proper-lockfile
// protocol). The returned function releases it. When the lock stays busy the
// caller goes ahead without it and relies on editClaudeJSON's re-read.
func lockClaudeFile(path string) func() {
	lock := resolveLink(path) + ".lock"
	deadline := time.Now().Add(claudeLockWait)
	for {
		err := os.Mkdir(lock, 0o755)
		if err == nil {
			return func() { os.Remove(lock) }
		}
		if !errors.Is(err, fs.ErrExist) || time.Now().After(deadline) {
			return func() {}
		}
		time.Sleep(15 * time.Millisecond)
	}
}

// beforeClaudeWrite lets tests play Claude Code writing the file at the worst moment.
var beforeClaudeWrite func(path string)

var errKeepsChanging = errors.New("keeps changing while Claude Code writes it; left as it was")

// editClaudeJSON rewrites one of Claude Code's JSON files. edit gets the
// current content and returns the new one, or nil when there is nothing to
// change. Claude rewrites these files while it runs, so the edit happens under
// Claude's own lock and starts over if the file changed in the meantime.
func editClaudeJSON(path string, edit func(raw []byte) []byte) error {
	raw, err := os.ReadFile(path)
	if err != nil || edit(raw) == nil {
		return nil // no such file, or nothing to do: do not even take the lock
	}
	defer lockClaudeFile(path)()
	backedUp := false
	for attempt := 0; attempt < 6; attempt++ {
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out := edit(raw)
		if out == nil {
			return nil
		}
		fi, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !backedUp {
			backup(path)
			backedUp = true
		}
		if beforeClaudeWrite != nil {
			beforeClaudeWrite(path)
		}
		if cur, err := os.ReadFile(path); err != nil || !bytes.Equal(cur, raw) {
			continue
		}
		return atomicWrite(path, out, fi.Mode().Perm())
	}
	return fmt.Errorf("%s %w", path, errKeepsChanging)
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
