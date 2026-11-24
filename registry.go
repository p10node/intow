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
	Hi    uint64 // upper half of a 128-bit file ID (ReFS on Windows); 0 elsewhere
	Birth int64  // birth time in ns; 0 when the filesystem has none
}

func (id Ident) key() string {
	if id.Hi != 0 {
		return fmt.Sprintf("%d:%d:%d", id.Dev, id.Ino, id.Hi)
	}
	return fmt.Sprintf("%d:%d", id.Dev, id.Ino)
}

type Entry struct {
	Path         string `json:"path"`
	Dev          uint64 `json:"dev"`
	Ino          uint64 `json:"ino"`
	Hi           uint64 `json:"ino_hi,omitempty"`
	Birth        int64  `json:"birth_ns,omitempty"`
	Seen         int64  `json:"seen"`
	MissingSince int64  `json:"missing_since,omitempty"`
	LastScan     int64  `json:"last_scan,omitempty"`
	// Found is where a disk scan last saw the folder, so a migration that was
	// skipped (Claude still running in the old path) can be retried without
	// waiting for the next scan.
	Found string `json:"found,omitempty"`
	// Was is a path this folder used to have that another folder took before
	// its history could follow (app -> app-old, then a new app). The history
	// recorded under Was may belong here; `ccanchor link --force` decides.
	Was string `json:"was,omitempty"`
}

// Drift records that Claude Code named a project folder differently than
// encode does. While it is set ccanchor moves nothing on its own: it would
// put data where Claude does not look.
type Drift struct {
	Since int64  `json:"since"`
	Cwd   string `json:"cwd"`
	Got   string `json:"got"`
	Want  string `json:"want"`
}

func (d *Drift) String() string {
	return fmt.Sprintf("Claude Code stores %s under projects/%s, ccanchor expected projects/%s", d.Cwd, d.Got, d.Want)
}

func (e *Entry) ident() Ident { return Ident{Dev: e.Dev, Ino: e.Ino, Hi: e.Hi, Birth: e.Birth} }

// is reports whether id is this entry's folder. The device is left out: its
// number can change across reboots and remounts.
func (e *Entry) is(id Ident) bool {
	return id.Ino == e.Ino && id.Hi == e.Hi && (e.Birth == 0 || id.Birth == e.Birth)
}

// Registry maps "dev:ino" to the folder last seen with that identity.
type Registry struct {
	Entries map[string]*Entry
	Drift   *Drift
	dirty   bool
}

func registryPath() string { return filepath.Join(stateDir(), "registry.json") }

func loadRegistry() *Registry {
	r := &Registry{Entries: map[string]*Entry{}}
	b, err := os.ReadFile(registryPath())
	if err != nil {
		return r
	}
	var f struct {
		Entries map[string]*Entry `json:"entries"`
		Drift   *Drift            `json:"drift"`
	}
	if json.Unmarshal(b, &f) == nil && f.Entries != nil {
		r.Entries, r.Drift = f.Entries, f.Drift
	}
	return r
}

func (r *Registry) clone() *Registry {
	c := &Registry{Entries: make(map[string]*Entry, len(r.Entries)), Drift: r.Drift}
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
	doc := map[string]any{"version": 1, "entries": r.Entries}
	if r.Drift != nil {
		doc["drift"] = r.Drift
	}
	err := writeJSONFile(registryPath(), doc)
	if err == nil {
		r.dirty = false
	}
	return err
}

func (r *Registry) find(id Ident) (string, *Entry) {
	k := id.key()
	if e, ok := r.Entries[k]; ok {
		return k, e
	}
	for k, e := range r.Entries { // device renumbered after a reboot or remount
		if e.Birth != 0 && e.is(id) {
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
	key := id.key()
	now := time.Now().Unix()
	for k, e := range r.Entries {
		if e.Path != path || k == key {
			continue
		}
		// This path now holds a different folder. When the one recorded here
		// was only moved aside (app -> app-old, then a fresh app), keep its
		// entry: its history is still under this path and can be offered to it
		// once Claude starts there. Otherwise that folder is gone.
		if cur := r.whereIs(e, now); cur != "" && cur != path && !isTrash(cur) {
			if e.Found != cur || e.MissingSince == 0 {
				e.Found, e.MissingSince = cur, now
				r.dirty = true
			}
			continue
		}
		delete(r.Entries, k)
		r.dirty = true
	}
	if k, _ := r.find(id); k != "" && k != key {
		delete(r.Entries, k)
		r.dirty = true
	}
	e := r.Entries[key]
	switch {
	case e == nil || e.Path != path || e.Birth != id.Birth || e.MissingSince != 0:
		r.Entries[key] = &Entry{Path: path, Dev: id.Dev, Ino: id.Ino, Hi: id.Hi, Birth: id.Birth, Seen: now}
		r.dirty = true
	case now-e.Seen > 3600:
		e.Seen = now
		r.dirty = true
	}
	return key, nil
}

// whereIs finds the folder of an entry whose recorded path is taken by
// another folder.
func (r *Registry) whereIs(e *Entry, now int64) string {
	if cur := platformResolve(e); cur != "" {
		return cur
	}
	if e.Found != "" {
		if id, err := statIdent(e.Found); err == nil && id == e.ident() {
			return e.Found
		}
	}
	if scanFallback && now-e.LastScan > 3600 {
		e.LastScan = now
		r.dirty = true
		return scanResolve(e, 300*time.Millisecond)
	}
	return ""
}

// rebase records that the data of old now belongs to new.
func (r *Registry) rebase(old, new string) {
	for _, e := range r.Entries {
		if e.Was != "" && under(e.Was, old) {
			e.Was = ""
			r.dirty = true
		}
		if !under(e.Path, old) {
			continue
		}
		if id, err := statIdent(e.Path); err == nil && e.is(id) {
			continue // still there: `link --force` away from a path that another folder took
		}
		e.Path = rebase(e.Path, old, new)
		// Found again: the next time it goes missing is a new search, not a
		// repeat of this one (scans are limited to one an hour per folder).
		e.LastScan, e.Found, e.MissingSince = 0, "", 0
		r.dirty = true
	}
}

// setDrift pauses automatic following; clearDrift resumes it.
func (r *Registry) setDrift(cwd, got string) {
	if r.Drift != nil && r.Drift.Cwd == cwd && r.Drift.Got == got {
		return
	}
	r.Drift = &Drift{Since: time.Now().Unix(), Cwd: cwd, Got: got, Want: encode(cwd)}
	r.dirty = true
}

func (r *Registry) clearDrift() {
	if r.Drift != nil {
		r.Drift = nil
		r.dirty = true
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
			if e.is(id) {
				if e.MissingSince != 0 || e.Found != "" {
					e.MissingSince, e.Found = 0, ""
					r.dirty = true
				}
				// Still reachable by its old name, yet renamed: only its case changed.
				if followsCaseRenames {
					if cur := platformResolve(e); cur != "" && caseRename(e.Path, cur) {
						out = append(out, action{k, e.Path, cur, "moved"})
					}
				}
				continue
			}
			// Another folder sits at the recorded path; New is where the
			// recorded one was last found, if it still exists.
			out = append(out, action{k, e.Path, r.whereIs(e, now), "replaced"})
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
// above $HOME), skipping hidden and dependency folders that it did not live
// in, within a time budget.
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
			if !de.IsDir() { // symlinks are not IsDir
				continue
			}
			p := filepath.Join(it.path, de.Name())
			// Hidden and dependency folders are skipped, except on the way to
			// where the folder used to be: a project under ~/.config is
			// usually moved within ~/.config.
			if skipInScan(de.Name()) && !under(e.Path, p) {
				continue
			}
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
