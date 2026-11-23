package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Ident is a directory's filesystem identity. Directories keep it when they
// are moved or renamed within a volume.
type Ident struct {
	Dev   uint64
	Ino   uint64
	Birth int64 // birth time in ns; 0 when the filesystem has none
}

type Entry struct {
	Path         string `json:"path"`
	Dev          uint64 `json:"dev"`
	Ino          uint64 `json:"ino"`
	Birth        int64  `json:"birth_ns,omitempty"`
	Seen         int64  `json:"seen"`
	MissingSince int64  `json:"missing_since,omitempty"`
	LastScan     int64  `json:"last_scan,omitempty"`
	// Found is where a disk scan last saw the folder, so a migration that was
	// skipped (Claude still running in the old path) can be retried without
	// waiting for the next scan.
	Found string `json:"found,omitempty"`
}

func (e *Entry) ident() Ident { return Ident{Dev: e.Dev, Ino: e.Ino, Birth: e.Birth} }

// Registry maps "dev:ino" to the folder last seen with that identity.
type Registry struct {
	Entries map[string]*Entry
	dirty   bool
}

func identKey(dev, ino uint64) string { return fmt.Sprintf("%d:%d", dev, ino) }

func registryPath() string { return filepath.Join(stateDir(), "registry.json") }

func loadRegistry() *Registry {
	r := &Registry{Entries: map[string]*Entry{}}
	b, err := os.ReadFile(registryPath())
	if err != nil {
		return r
	}
	var f struct {
		Entries map[string]*Entry `json:"entries"`
	}
	if json.Unmarshal(b, &f) == nil && f.Entries != nil {
		r.Entries = f.Entries
	}
	return r
}

func (r *Registry) clone() *Registry {
	c := &Registry{Entries: make(map[string]*Entry, len(r.Entries))}
	for k, e := range r.Entries {
		v := *e
		c.Entries[k] = &v
	}
	return c
}

func (r *Registry) save() error {
	if !r.dirty {
		return nil
	}
	err := writeJSONFile(registryPath(), map[string]any{"version": 1, "entries": r.Entries})
	if err == nil {
		r.dirty = false
	}
	return err
}

func (r *Registry) find(id Ident) (string, *Entry) {
	k := identKey(id.Dev, id.Ino)
	if e, ok := r.Entries[k]; ok {
		return k, e
	}
	for k, e := range r.Entries { // device renumbered after a reboot or remount
		if e.Ino == id.Ino && e.Birth != 0 && e.Birth == id.Birth {
			return k, e
		}
	}
	return "", nil
}

// track records the identity of the folder at path.
func (r *Registry) track(path string) (string, error) {
	id, err := statIdent(path)
	if err != nil {
		return "", err
	}
	key := identKey(id.Dev, id.Ino)
	for k, e := range r.Entries {
		if e.Path == path && k != key {
			delete(r.Entries, k) // this path now holds a different folder
			r.dirty = true
		}
	}
	if k, _ := r.find(id); k != "" && k != key {
		delete(r.Entries, k)
		r.dirty = true
	}
	now := time.Now().Unix()
	e := r.Entries[key]
	switch {
	case e == nil || e.Path != path || e.Birth != id.Birth || e.MissingSince != 0:
		r.Entries[key] = &Entry{Path: path, Dev: id.Dev, Ino: id.Ino, Birth: id.Birth, Seen: now}
		r.dirty = true
	case now-e.Seen > 3600:
		e.Seen = now
		r.dirty = true
	}
	return key, nil
}

func (r *Registry) rebase(old, new string) {
	for _, e := range r.Entries {
		if under(e.Path, old) {
			e.Path = rebase(e.Path, old, new)
			r.dirty = true
		}
	}
}

func (r *Registry) sortedKeys() []string {
	keys := make([]string, 0, len(r.Entries))
	for k := range r.Entries {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := r.Entries[keys[i]].Path, r.Entries[keys[j]].Path
		if len(a) != len(b) {
			return len(a) < len(b)
		}
		return a < b
	})
	return keys
}

type action struct {
	Key, Old, New string
	State         string // moved | missing | trashed | replaced
}

// plan lists tracked folders that are no longer at their recorded path.
func plan(r *Registry) []action {
	var out []action
	now := time.Now().Unix()
	for _, k := range r.sortedKeys() {
		e := r.Entries[k]
		id, err := statIdent(e.Path)
		if err == nil {
			if id.Ino == e.Ino && (e.Birth == 0 || id.Birth == e.Birth) {
				if e.MissingSince != 0 || e.Found != "" {
					e.MissingSince, e.Found = 0, ""
					r.dirty = true
				}
				continue
			}
			out = append(out, action{k, e.Path, "", "replaced"})
			continue
		}
		if err == errNotDir {
			out = append(out, action{k, e.Path, "", "replaced"})
			continue
		}
		if !isNotExist(err) {
			continue // e.g. permission denied: cannot tell
		}
		cur := platformResolve(e)
		if cur == "" && e.Found != "" {
			if id, err := statIdent(e.Found); err == nil && id == e.ident() {
				cur = e.Found
			} else {
				e.Found = "" // moved again since the scan
				r.dirty = true
			}
		}
		if cur == "" && scanFallback && now-e.LastScan > 3600 &&
			(e.MissingSince == 0 || now-e.MissingSince < 30*86400) {
			e.LastScan = now
			r.dirty = true
			if cur = scanResolve(e, 800*time.Millisecond); cur != "" {
				e.Found = cur
			}
		}
		switch {
		case cur == "":
			if e.MissingSince == 0 {
				e.MissingSince = now
				r.dirty = true
			}
			out = append(out, action{k, e.Path, "", "missing"})
		case isTrash(cur):
			out = append(out, action{k, e.Path, cur, "trashed"})
		default:
			out = append(out, action{k, e.Path, cur, "moved"})
		}
	}
	return out
}

const maxScanDepth = 5

// scanResolve looks for the folder by identity near where it used to be:
// breadth-first from two levels above its nearest surviving ancestor (not
// above $HOME), skipping hidden and dependency folders, within a time budget.
func scanResolve(e *Entry, budget time.Duration) string {
	if e.Birth == 0 {
		return "" // without a birth time an inode number may have been reused
	}
	root := nearestExisting(e.Path)
	home := homeDir()
	for i := 0; i < 2; i++ {
		parent := filepath.Dir(root)
		if parent == root || root == home || (under(root, home) && !under(parent, home)) {
			break
		}
		root = parent
	}
	deadline := time.Now().Add(budget)
	type item struct {
		path  string
		depth int
	}
	queue := []item{{root, 0}}
	for len(queue) > 0 && time.Now().Before(deadline) {
		it := queue[0]
		queue = queue[1:]
		ents, err := os.ReadDir(it.path)
		if err != nil {
			continue
		}
		for _, de := range ents {
			if !de.IsDir() || skipInScan(de.Name()) { // symlinks are not IsDir
				continue
			}
			p := filepath.Join(it.path, de.Name())
			if id, err := statIdent(p); err == nil && id == e.ident() {
				return p
			}
			if it.depth+1 < maxScanDepth {
				queue = append(queue, item{p, it.depth + 1})
			}
		}
	}
	return ""
}

func skipInScan(name string) bool {
	switch name {
	case "node_modules", "vendor", "target", "dist", "build", "venv", "__pycache__", "Library":
		return true
	}
	return strings.HasPrefix(name, ".")
}
