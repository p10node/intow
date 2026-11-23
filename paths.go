package main

import (
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

// tempPrefixes are never tracked: Claude often runs there for throwaway work.
// Tests clear it because their sandboxes live in a temp directory.
var tempPrefixes = []string{"/private/var/folders/", "/var/folders/", "/private/tmp/", "/tmp/"}

const sep = string(os.PathSeparator)

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

// configDir mirrors Claude Code: $CLAUDE_CONFIG_DIR or ~/.claude.
func configDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Join(homeDir(), ".claude")
}

// globalJSONPath is ~/.claude.json, or .claude.json inside $CLAUDE_CONFIG_DIR.
func globalJSONPath() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, ".claude.json")
	}
	return filepath.Join(homeDir(), ".claude.json")
}

func projectsDir() string  { return filepath.Join(configDir(), "projects") }
func stateDir() string     { return filepath.Join(configDir(), "ccanchor") }
func settingsPath() string { return filepath.Join(configDir(), "settings.json") }
func installedBin() string { return filepath.Join(stateDir(), "bin", "ccanchor"+exeSuffix) }

// under reports whether path is root or inside it.
func under(path, root string) bool {
	if path == "" || root == "" {
		return false
	}
	root = strings.TrimRight(root, sep)
	if root == "" {
		return strings.HasPrefix(path, sep)
	}
	return path == root || strings.HasPrefix(path, root+sep)
}

// rebase swaps the prefix old of path (which must be under old) for new.
func rebase(path, old, new string) string { return new + path[len(old):] }

func isTrash(p string) bool {
	q := filepath.ToSlash(p) + "/"
	return strings.Contains(q, "/.Trash/") || strings.Contains(q, "/.Trashes/") ||
		strings.Contains(q, "/.local/share/Trash/") || strings.Contains(q, "/$Recycle.Bin/")
}

func isTemp(p string) bool {
	q := p + sep
	for _, t := range tempPrefixes {
		if strings.HasPrefix(q, t) {
			return true
		}
	}
	return false
}

// nearestExisting returns the closest existing ancestor directory of path.
func nearestExisting(path string) string {
	p := path
	for {
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		p = parent
		if isDir(p) {
			return p
		}
	}
}

// ---- Claude Code's project directory naming
//
// Claude Code names a folder's data directory with
//
//	path.replace(/[^a-zA-Z0-9]/g, "-")   // per UTF-16 code unit
//
// and, when that is longer than 200 characters, keeps the first 200 and
// appends "-" + Math.abs(h).toString(36), where h is the 32-bit string hash
// h = h*31 + c over the UTF-16 code units.

const maxName = 200

func sanitize(path string) string {
	units := utf16.Encode([]rune(path))
	b := make([]byte, len(units))
	for i, c := range units {
		if (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
			b[i] = byte(c)
		} else {
			b[i] = '-'
		}
	}
	return string(b)
}

func jsHash(s string) int64 {
	var h int32
	for _, c := range utf16.Encode([]rune(s)) {
		h = h*31 + int32(c) // wraps like JS `|0`
	}
	v := int64(h)
	if v < 0 {
		v = -v
	}
	return v
}

func encode(path string) string {
	s := sanitize(path)
	if len(s) <= maxName {
		return s
	}
	return s[:maxName] + "-" + formatBase36(jsHash(path))
}
