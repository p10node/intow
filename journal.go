package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// A journal records what one migration did, step by step, so that `undo` can
// take back exactly that and nothing else: sessions Claude has continued in
// the new place since, and settings it has changed there, stay where they are.

const (
	maxJournals     = 50
	maxIndexBytes   = 256 << 10 // a MEMORY.md larger than this is not kept in the journal
	maxHistoryLines = 50000     // more rewritten history lines than this are not remembered
)

// jop is one step. Which fields are set depends on Op:
//
//	move     From -> To: a file (Size, Mtime at that moment) or a directory (Dir)
//	drop     From was removed because To held the same bytes
//	rewrite  Field in the JSONL file Path got the new folder; Pre counts values
//	         that named the new folder already; Size, Mtime as left behind
//	index    the memory index Path took the lines of the index From, which was
//	         removed; Text and Was are their previous contents, Sum the result
//	config   ~/.claude.json "projects": the entry From (Before) went to To,
//	         which had Had and now holds After
//	repos    ~/.claude.json "githubRepoPaths"[Name]: Before -> After
//	plugins  installed_plugins.json "plugins"[Name]: Before -> After
//	history  lines of the JSONL file Path whose Field was rewritten, by hash
type jop struct {
	Op     string          `json:"op"`
	From   string          `json:"from,omitempty"`
	To     string          `json:"to,omitempty"`
	Dir    bool            `json:"dir,omitempty"`
	Size   int64           `json:"size,omitempty"`
	Mtime  int64           `json:"mtime_ns,omitempty"`
	Path   string          `json:"path,omitempty"`
	Field  string          `json:"field,omitempty"`
	Pre    int             `json:"pre,omitempty"`
	Name   string          `json:"name,omitempty"`
	Before json.RawMessage `json:"before,omitempty"`
	Had    json.RawMessage `json:"had,omitempty"`
	After  json.RawMessage `json:"after,omitempty"`
	Text   *string         `json:"text,omitempty"`
	Was    *string         `json:"was,omitempty"`
	Sum    string          `json:"sum,omitempty"`
	Lines  []string        `json:"lines,omitempty"`
	Lost   bool            `json:"lost,omitempty"` // too large to remember: cannot be undone
}

type journal struct {
	Version int    `json:"version"`
	TS      int64  `json:"ts"`     // unix seconds, for display
	End     int64  `json:"end_ns"` // when the migration finished
	Old     string `json:"old"`
	New     string `json:"new"`
	Ops     []jop  `json:"ops"`

	file string
}

func journalDir() string { return filepath.Join(stateDir(), "journal") }

func newJournal(old, new string) *journal {
	return &journal{Version: 1, TS: time.Now().Unix(), Old: old, New: new}
}

// add is safe on a nil journal (dry runs keep none).
func (j *journal) add(op jop) {
	if j != nil {
		j.Ops = append(j.Ops, op)
	}
}

func (j *journal) move(from, to string) {
	if j == nil {
		return
	}
	op := jop{Op: "move", From: from, To: to}
	if fi, err := os.Lstat(to); err == nil {
		if fi.IsDir() {
			op.Dir = true
		} else {
			op.Size, op.Mtime = fi.Size(), fi.ModTime().UnixNano()
		}
	}
	j.add(op)
}

// movedEntries records a directory renamed from -> to as a move of each entry,
// because that is how it is taken back: entry by entry, leaving what is new.
func (j *journal) movedEntries(from, to string) {
	if j == nil {
		return
	}
	ents, _ := os.ReadDir(to)
	for _, e := range ents {
		j.move(filepath.Join(from, e.Name()), filepath.Join(to, e.Name()))
	}
}

func (j *journal) drop(from, same string) {
	if j == nil {
		return
	}
	op := jop{Op: "drop", From: from, To: same}
	if fi, err := os.Stat(same); err == nil {
		op.Size, op.Mtime = fi.Size(), fi.ModTime().UnixNano()
	}
	j.add(op)
}

func (j *journal) rewrote(path, field string, pre int) {
	if j == nil {
		return
	}
	op := jop{Op: "rewrite", Path: path, Field: field, Pre: pre}
	if fi, err := os.Stat(path); err == nil {
		op.Size, op.Mtime = fi.Size(), fi.ModTime().UnixNano()
	}
	j.add(op)
}

func sumOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:12])
}

func compactJSON(raw json.RawMessage) json.RawMessage {
	var c bytes.Buffer
	if json.Compact(&c, raw) != nil {
		return raw
	}
	return c.Bytes()
}

func sameJSON(a, b json.RawMessage) bool { return bytes.Equal(compactJSON(a), compactJSON(b)) }

// save writes the journal, if the migration did anything, and prunes old ones.
func (j *journal) save() {
	if j == nil || len(j.Ops) == 0 {
		return
	}
	now := time.Now()
	j.End = now.UnixNano()
	dir := journalDir()
	name := now.Format("20060102-150405.000000000") + ".json"
	if writeJSONFile(filepath.Join(dir, name), j) != nil {
		return
	}
	ents, _ := os.ReadDir(dir)
	var names []string
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > maxJournals {
		os.Remove(filepath.Join(dir, names[0]))
		names = names[1:]
	}
}

// lastJournal is the newest migration that has not been undone, optionally the
// newest one for a given folder (either its old or its new path).
func lastJournal(folder string) *journal {
	dir := journalDir()
	ents, _ := os.ReadDir(dir)
	var names []string
	for _, e := range ents {
		if n := e.Name(); strings.HasSuffix(n, ".json") && !strings.HasSuffix(n, ".undone.json") {
			names = append(names, n)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		j := &journal{}
		if json.Unmarshal(b, j) != nil || j.Old == "" || j.New == "" {
			continue
		}
		if folder != "" && j.Old != folder && j.New != folder {
			continue
		}
		j.file = filepath.Join(dir, n)
		return j
	}
	return nil
}

func (j *journal) markUndone() {
	if j.file != "" {
		os.Rename(j.file, strings.TrimSuffix(j.file, ".json")+".undone.json")
	}
}

// ---- undo

type undone struct {
	Sessions   int // session files back under the old path
	Kept       int // session files that stay: continued, or in the way, since
	Memory     int
	ConfigKeys int
	RepoPaths  int
	Plugins    int
	History    int
	Notes      []string
}

func (u *undone) changed() bool {
	return u.Sessions+u.Memory+u.ConfigKeys+u.RepoPaths+u.Plugins+u.History > 0
}

func (u *undone) note(format string, a ...any) {
	s := fmt.Sprintf(format, a...)
	for _, n := range u.Notes {
		if n == s {
			return
		}
	}
	u.Notes = append(u.Notes, s)
}

// unchanged reports whether the file at path is still as the journal left it.
func unchanged(path string, size, mtime int64) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode().IsRegular() && fi.Size() == size && fi.ModTime().UnixNano() == mtime
}

// untouched reports whether nothing under dir was written after the migration.
func untouched(dir string, end int64) bool {
	ok := true
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			ok = false
			return filepath.SkipAll
		}
		if d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err != nil || fi.ModTime().UnixNano() > end {
			ok = false
			return filepath.SkipAll
		}
		return nil
	})
	return ok
}

func inMemory(path string) bool {
	return strings.Contains(filepath.ToSlash(path), "/memory/") || filepath.Base(path) == "memory"
}

// sessionKey names the session a top-level entry of a project dir belongs to
// ("<id>.jsonl" and the folder "<id>" travel together); "" for anything else.
func sessionKey(path string) string {
	if filepath.Dir(filepath.Dir(path)) != projectsDir() || filepath.Base(path) == "memory" {
		return ""
	}
	return strings.TrimSuffix(path, ".jsonl")
}

// undo takes the migration back. With dry it only works out what would happen.
func (j *journal) undo(dry bool) (*undone, error) {
	u := &undone{}
	rewritten := map[string]bool{} // files whose rewrite was (or would be) reversed
	var moves []jop
	jsonDone := map[string]bool{}

	for i := len(j.Ops) - 1; i >= 0; i-- {
		op := j.Ops[i]
		switch op.Op {
		case "history":
			if op.Lost {
				u.note("history.jsonl: too many lines were rewritten to remember; they keep the new path")
				continue
			}
			only := make(map[string]bool, len(op.Lines))
			for _, l := range op.Lines {
				only[l] = true
			}
			n, err := restoreLines(op.Path, op.Field, j.New, j.Old, only, dry)
			if err != nil {
				return u, err
			}
			u.History += n

		case "plugins":
			if jsonDone[op.Op] {
				continue
			}
			jsonDone[op.Op] = true
			path := filepath.Join(configDir(), "plugins", "installed_plugins.json")
			if err := j.undoJSON(path, dry, u, func(raw []byte, u *undone) []byte { return unrebasePlugins(raw, j.Ops, u) }); err != nil {
				return u, err
			}

		case "config", "repos":
			if jsonDone["global"] {
				continue
			}
			jsonDone["global"] = true
			if err := j.undoJSON(globalJSONPath(), dry, u, func(raw []byte, u *undone) []byte { return unrekeyGlobalJSON(raw, j.Ops, u) }); err != nil {
				return u, err
			}

		case "rewrite":
			if op.Pre > 0 || !unchanged(op.Path, op.Size, op.Mtime) {
				continue // continued since, or ambiguous: the move below leaves it too
			}
			rewritten[op.Path] = true
			if !dry {
				if _, _, err := rewriteField(op.Path, op.Field, j.New, j.Old); err != nil {
					return u, err
				}
			}

		case "index":
			if err := j.undoIndex(op, dry, u); err != nil {
				return u, err
			}

		case "drop":
			if exists(op.From) || !unchanged(op.To, op.Size, op.Mtime) {
				continue
			}
			if !dry {
				if err := copyFile(op.To, op.From, 0o600); err != nil {
					return u, err
				}
				mt := time.Unix(0, op.Mtime)
				os.Chtimes(op.From, mt, mt)
			}
			switch {
			case inMemory(op.From):
				u.Memory++
			case strings.HasSuffix(op.From, ".jsonl"):
				u.Sessions++
			}

		case "move":
			moves = append(moves, op)
		}
	}

	// Which moves can be taken back? A session's transcript and its folder
	// stay together.
	ok := make([]bool, len(moves))
	stay := map[string]bool{}
	for i, op := range moves {
		switch {
		case exists(op.From) || !exists(op.To):
		case op.Dir:
			ok[i] = untouched(op.To, j.End)
		default:
			// A rewritten transcript has its old size again once the rewrite is reversed.
			ok[i] = unchanged(op.To, op.Size, op.Mtime) || (dry && rewritten[op.To])
		}
		if !ok[i] {
			if k := sessionKey(op.To); k != "" {
				stay[k] = true
			}
		}
	}
	var emptied []string
	for i, op := range moves {
		isSession := sessionKey(op.To) != "" && strings.HasSuffix(op.To, ".jsonl")
		if !ok[i] || stay[sessionKey(op.To)] {
			switch {
			case !exists(op.To): // gone, or taken back already
			case isSession:
				u.Kept++
			case inMemory(op.To):
				u.note("memory was written in %s since; %s stays there", j.New, filepath.Base(op.To))
			}
			continue
		}
		n := 1
		if op.Dir {
			n = countFiles(op.To)
		}
		if !dry {
			if err := os.MkdirAll(filepath.Dir(op.From), 0o755); err != nil {
				return u, err
			}
			if err := os.Rename(op.To, op.From); err != nil {
				return u, err
			}
			emptied = append(emptied, filepath.Dir(op.To))
		}
		switch {
		case isSession:
			u.Sessions++
		case inMemory(op.To):
			u.Memory += n
		}
	}
	// In-place migrations (same project dir name) have no moves: count rewrites.
	if len(moves) == 0 {
		for p := range rewritten {
			if sessionKey(p) != "" {
				u.Sessions++
			}
		}
	}
	if u.Kept > 0 {
		u.note("%s continued or created in %s since stay there", plural(u.Kept, "session"), j.New)
	}
	// Drop the directories the undo emptied, deepest first, never projects/ itself.
	sort.Slice(emptied, func(a, b int) bool { return len(emptied[a]) > len(emptied[b]) })
	for _, d := range emptied {
		for d != projectsDir() && under(d, projectsDir()) && os.Remove(d) == nil {
			d = filepath.Dir(d)
		}
	}
	return u, nil
}

// undoJSON applies edit to one of Claude's JSON files; edit returns nil when
// there is nothing to take back.
func (j *journal) undoJSON(path string, dry bool, u *undone, edit func(raw []byte, u *undone) []byte) error {
	if dry {
		if raw, err := os.ReadFile(path); err == nil {
			edit(raw, u)
		}
		return nil
	}
	var last undone
	err := editClaudeJSON(path, func(raw []byte) []byte {
		last = undone{} // the edit may run more than once: count only the run that is written
		return edit(raw, &last)
	})
	if err != nil {
		return err
	}
	u.ConfigKeys += last.ConfigKeys
	u.RepoPaths += last.RepoPaths
	u.Plugins += last.Plugins
	for _, n := range last.Notes {
		u.note("%s", n)
	}
	return nil
}

func (j *journal) undoIndex(op jop, dry bool, u *undone) error {
	if op.Lost || op.Was == nil {
		u.note("memory index of %s was too large to remember; it stays merged", j.New)
		return nil
	}
	if !exists(op.From) {
		if !dry {
			if err := os.MkdirAll(filepath.Dir(op.From), 0o755); err != nil {
				return err
			}
			if err := atomicWrite(op.From, []byte(*op.Was), 0o644); err != nil {
				return err
			}
		}
		u.Memory++
	}
	cur, err := os.ReadFile(op.Path)
	switch {
	case err != nil || op.Text == nil:
	case sumOf(cur) != op.Sum:
		u.note("memory index of %s changed since; the lines merged into it stay", j.New)
	case string(cur) != *op.Text && !dry:
		return atomicWrite(op.Path, []byte(*op.Text), 0o644)
	}
	return nil
}

// unrekeyGlobalJSON gives the old path its ~/.claude.json entries back. The
// entry of the new path returns to what it was before only if nothing has
// changed it since.
func unrekeyGlobalJSON(raw []byte, ops []jop, u *undone) []byte {
	root, err := parseObject(raw)
	if err != nil {
		return nil
	}
	changed := false
	projects, repos := newOmap(), newOmap()
	hasProjects, hasRepos := false, false
	if v, ok := root.get("projects"); ok {
		if m, err := parseObject(v); err == nil {
			projects, hasProjects = m, true
		}
	}
	if v, ok := root.get("githubRepoPaths"); ok {
		if m, err := parseObject(v); err == nil {
			repos, hasRepos = m, true
		}
	}
	touchedProjects, touchedRepos := false, false
	for i := len(ops) - 1; i >= 0; i-- {
		op := ops[i]
		switch op.Op {
		case "config":
			if !hasProjects {
				continue
			}
			if _, taken := projects.get(op.From); taken {
				continue // the old path has settings again already
			}
			if cur, ok := projects.get(op.To); ok && sameJSON(cur, op.After) {
				if op.Had != nil {
					projects.set(op.To, op.Had)
				} else {
					projects.del(op.To)
				}
			} else if ok {
				u.note("settings of %s changed since; they stay, and %s has its own back", op.To, op.From)
			}
			projects.set(op.From, op.Before)
			u.ConfigKeys++
			touchedProjects = true
		case "repos":
			if !hasRepos {
				continue
			}
			if cur, ok := repos.get(op.Name); ok && sameJSON(cur, op.After) {
				repos.set(op.Name, op.Before)
				u.RepoPaths++
				touchedRepos = true
			}
		}
	}
	if touchedProjects {
		root.set("projects", projects.marshal())
		changed = true
	}
	if touchedRepos {
		root.set("githubRepoPaths", repos.marshal())
		changed = true
	}
	if !changed {
		return nil
	}
	out := pretty(root.marshal())
	if bytes.HasSuffix(raw, []byte("\n")) {
		out = append(out, '\n')
	}
	return out
}

func unrebasePlugins(raw []byte, ops []jop, u *undone) []byte {
	root, err := parseObject(raw)
	if err != nil {
		return nil
	}
	pl, ok := root.get("plugins")
	if !ok {
		return nil
	}
	plugins, err := parseObject(pl)
	if err != nil {
		return nil
	}
	changed := false
	for i := len(ops) - 1; i >= 0; i-- {
		op := ops[i]
		if op.Op != "plugins" {
			continue
		}
		if cur, ok := plugins.get(op.Name); ok && sameJSON(cur, op.After) {
			plugins.set(op.Name, op.Before)
			u.Plugins++
			changed = true
		}
	}
	if !changed {
		return nil
	}
	root.set("plugins", plugins.marshal())
	out := pretty(root.marshal())
	if bytes.HasSuffix(raw, []byte("\n")) {
		out = append(out, '\n')
	}
	return out
}
