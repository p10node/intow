package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Report describes one migration of a folder's Claude Code data.
type Report struct {
	Old        string      `json:"old"`
	New        string      `json:"new"`
	Dirs       [][3]string `json:"dirs"` // source name, destination name, mode
	Sessions   int         `json:"sessions"`
	Memory     int         `json:"memory"`
	ConfigKeys int         `json:"config_keys"`
	History    int         `json:"history"`
	Conflicts  []string    `json:"conflicts,omitempty"`
	Notes      []string    `json:"notes,omitempty"`
	Skipped    string      `json:"-"`
}

func (r *Report) changed() bool { return len(r.Dirs) > 0 || r.ConfigKeys > 0 || r.History > 0 }

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func (r *Report) short() string {
	var bits []string
	if r.Sessions > 0 {
		bits = append(bits, plural(r.Sessions, "session"))
	}
	if r.Memory > 0 {
		bits = append(bits, plural(r.Memory, "memory file"))
	}
	if r.ConfigKeys > 0 {
		bits = append(bits, "settings")
	}
	if len(bits) == 0 {
		bits = []string{"no data"}
	}
	return fmt.Sprintf("%s -> %s (%s)", r.Old, r.New, strings.Join(bits, ", "))
}

func (r *Report) lines(dryRun bool) []string {
	if r.Skipped != "" {
		return []string{fmt.Sprintf("skip %s -> %s: %s", r.Old, r.New, r.Skipped)}
	}
	head := "moved"
	if dryRun {
		head = "would move"
	}
	out := []string{head + " " + r.short()}
	for _, d := range r.Dirs {
		out = append(out, fmt.Sprintf("    projects/%s -> %s  [%s]", d[0], d[1], d[2]))
	}
	if r.History > 0 {
		out = append(out, fmt.Sprintf("    history.jsonl: %d line(s)", r.History))
	}
	for _, c := range r.Conflicts {
		out = append(out, "    ! left in place (name clash): "+c)
	}
	for _, n := range r.Notes {
		out = append(out, "    note: "+n)
	}
	return out
}

type migrateOpts struct {
	DryRun         bool
	DstOverride    string // project dir Claude already uses for the new path (from the hook)
	ExcludeSession string
	Force          bool
	Reg            *Registry
}

// migrate re-keys all Claude Code data of folder old (and its subfolders) to new.
func migrate(old, new string, o migrateOpts) (*Report, error) {
	rep := &Report{Old: old, New: new}
	if old == new {
		rep.Skipped = "same path"
		return rep, nil
	}
	if under(new, old) || under(old, new) {
		rep.Skipped = "one path is inside the other"
		return rep, nil
	}
	if !o.Force {
		var pids []string
		for _, s := range liveSessions() {
			if under(s.Cwd, old) && s.SessionID != o.ExcludeSession {
				pids = append(pids, strconv.Itoa(s.PID))
			}
		}
		if len(pids) > 0 {
			rep.Skipped = "Claude is still running there (pid " + strings.Join(pids, ", ") + "); will retry later"
			return rep, nil
		}
	}
	for _, pd := range projectDirsUnder(old) {
		dst := encode(rebase(pd.cwd, old, new))
		if o.DstOverride != "" && pd.cwd == old && filepath.Dir(o.DstOverride) == projectsDir() {
			dst = filepath.Base(o.DstOverride)
		}
		src := filepath.Join(projectsDir(), pd.name)
		if err := moveProjectDir(src, filepath.Join(projectsDir(), dst), old, new, rep, o.DryRun); err != nil {
			return rep, err
		}
	}
	if err := updateGlobalJSON(old, new, rep, o.DryRun); err != nil {
		return rep, err
	}
	if err := updateHistory(old, new, rep, o.DryRun); err != nil {
		return rep, err
	}
	if o.Reg != nil {
		o.Reg.rebase(old, new)
	}
	if !o.DryRun && rep.changed() {
		appendLog(rep)
	}
	return rep, nil
}

func moveProjectDir(src, dst, old, new string, rep *Report, dryRun bool) error {
	sname, dname := filepath.Base(src), filepath.Base(dst)
	if src == dst { // encoding unchanged (e.g. "a_b" -> "a-b"): only the recorded cwd changes
		rep.Sessions += len(transcripts(src))
		if !dryRun {
			if _, err := rewriteTree(dst, old, new); err != nil {
				return err
			}
		}
		rep.Dirs = append(rep.Dirs, [3]string{sname, dname, "in place"})
		return nil
	}
	if !exists(dst) {
		renamed := true
		if !dryRun {
			if err := os.Rename(src, dst); err != nil {
				if !isExistErr(err) {
					return err
				}
				renamed = false // Claude created dst in the meantime: merge instead
			}
		}
		if renamed {
			target := dst
			if dryRun {
				target = src
			}
			rep.Sessions += len(transcripts(target))
			if mem := filepath.Join(target, "memory"); isDir(mem) {
				rep.Memory += countFiles(mem)
			}
			if !dryRun {
				if _, err := rewriteTree(dst, old, new); err != nil {
					return err
				}
			}
			rep.Dirs = append(rep.Dirs, [3]string{sname, dname, "rename"})
			return nil
		}
	}
	if err := mergeProjectDir(src, dst, old, new, rep, dryRun); err != nil {
		return err
	}
	rep.Dirs = append(rep.Dirs, [3]string{sname, dname, "merge"})
	return nil
}

func mergeProjectDir(src, dst, old, new string, rep *Report, dryRun bool) error {
	ents, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	var moved []string
	for _, de := range ents {
		name := de.Name()
		s, d := filepath.Join(src, name), filepath.Join(dst, name)
		if name == "memory" && de.IsDir() {
			if err := mergeMemory(s, d, rep, dryRun); err != nil {
				return err
			}
			continue
		}
		switch {
		case !exists(d):
			if strings.HasSuffix(name, ".jsonl") {
				rep.Sessions++
			}
			if !dryRun {
				if err := os.Rename(s, d); err != nil {
					return err
				}
				moved = append(moved, d)
			}
		case isFile(s) && isFile(d) && filesEqual(s, d):
			if !dryRun {
				os.Remove(s)
			}
		default:
			rep.Conflicts = append(rep.Conflicts, s)
		}
	}
	for _, p := range moved {
		if _, err := rewriteTree(p, old, new); err != nil {
			return err
		}
	}
	if !dryRun {
		os.Remove(src) // only succeeds when empty
	}
	return nil
}

func mergeMemory(s, d string, rep *Report, dryRun bool) error {
	if !exists(d) {
		rep.Memory += countFiles(s)
		if dryRun {
			return nil
		}
		return os.Rename(s, d)
	}
	ents, err := os.ReadDir(s)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.SliceStable(names, func(i, j int) bool { // MEMORY.md last, after renames are known
		return names[i] != "MEMORY.md" && names[j] == "MEMORY.md"
	})
	renamed := map[string]string{}
	for _, name := range names {
		sp, dp := filepath.Join(s, name), filepath.Join(d, name)
		switch {
		case !exists(dp):
			rep.Memory += countFiles(sp)
			if !dryRun {
				if err := os.Rename(sp, dp); err != nil {
					return err
				}
			}
		case isFile(sp) && isFile(dp) && filesEqual(sp, dp):
			if !dryRun {
				os.Remove(sp)
			}
		case name == "MEMORY.md" && isFile(sp) && isFile(dp):
			n, err := mergeIndex(sp, dp, renamed, dryRun)
			if err != nil {
				return err
			}
			if n > 0 {
				rep.Notes = append(rep.Notes, "memory index merged")
			}
			if !dryRun {
				os.Remove(sp)
			}
		case isDir(sp) && isDir(dp):
			if err := mergeMemory(sp, dp, rep, dryRun); err != nil {
				return err
			}
		default:
			alt := uniqueName(d, name)
			renamed[name] = alt
			rep.Memory++
			rep.Notes = append(rep.Notes, fmt.Sprintf("memory/%s exists in both; old copy kept as memory/%s", name, alt))
			if !dryRun {
				if err := os.Rename(sp, filepath.Join(d, alt)); err != nil {
					return err
				}
			}
		}
	}
	if !dryRun {
		os.Remove(s)
	}
	return nil
}

// mergeIndex appends lines of the old MEMORY.md that the new one lacks,
// pointing links at renamed files.
func mergeIndex(sp, dp string, renamed map[string]string, dryRun bool) (int, error) {
	src, err := os.ReadFile(sp)
	if err != nil {
		return 0, err
	}
	dstB, err := os.ReadFile(dp)
	if err != nil {
		return 0, err
	}
	dst := string(dstB)
	have := map[string]bool{}
	for _, l := range strings.Split(strings.ReplaceAll(dst, "\r\n", "\n"), "\n") {
		have[strings.TrimSpace(l)] = true
	}
	var add []string
	for _, line := range strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n") {
		for a, b := range renamed {
			line = strings.ReplaceAll(line, "("+a+")", "("+b+")")
		}
		if t := strings.TrimSpace(line); t != "" && !have[t] {
			add = append(add, line)
			have[t] = true
		}
	}
	if len(add) > 0 && !dryRun {
		if dst != "" && !strings.HasSuffix(dst, "\n") {
			dst += "\n"
		}
		if err := atomicWrite(dp, []byte(dst+strings.Join(add, "\n")+"\n"), 0o644); err != nil {
			return 0, err
		}
	}
	return len(add), nil
}

func uniqueName(dir, name string) string {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	cand := stem + ".moved" + ext
	for i := 2; exists(filepath.Join(dir, cand)); i++ {
		cand = fmt.Sprintf("%s.moved%d%s", stem, i, ext)
	}
	return cand
}

func jsonFragment(s string) []byte {
	b, _ := encodeJSON(s, false)
	return b[1 : len(b)-1]
}

// rewriteField rewrites `"field":"<old>"` and `"field":"<old>/...` to the new
// prefix in a JSONL file. Message text is history and stays as it was, and the
// file keeps its mtime so /resume keeps its order.
func rewriteField(path, field, old, new string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	o, n := jsonFragment(old), jsonFragment(new)
	key := []byte(`"` + field + `":"`)
	count := 0
	for _, term := range [][]byte{[]byte(`"`), jsonFragment(sep)} {
		a := bytes.Join([][]byte{key, o, term}, nil)
		b := bytes.Join([][]byte{key, n, term}, nil)
		if c := bytes.Count(data, a); c > 0 {
			data = bytes.ReplaceAll(data, a, b)
			count += c
		}
	}
	if count == 0 {
		return 0, nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if err := atomicWrite(path, data, fi.Mode().Perm()); err != nil {
		return 0, err
	}
	os.Chtimes(path, fi.ModTime(), fi.ModTime())
	return count, nil
}

func rewriteTree(root, old, new string) (int, error) {
	total := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
			return nil
		}
		n, err := rewriteField(p, "cwd", old, new)
		total += n
		return err
	})
	return total, err
}

func updateGlobalJSON(old, new string, rep *Report, dryRun bool) error {
	path := globalJSONPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	root, err := parseObject(raw)
	if err != nil {
		return nil
	}
	pj, ok := root.get("projects")
	if !ok {
		return nil
	}
	projects, err := parseObject(pj)
	if err != nil {
		return nil
	}
	var keys []string
	for _, k := range projects.keys {
		if under(k, old) {
			keys = append(keys, k)
		}
	}
	rep.ConfigKeys = len(keys)
	if len(keys) == 0 || dryRun {
		return nil
	}
	for _, k := range keys {
		nk := rebase(k, old, new)
		v := projects.vals[k]
		projects.del(k)
		if cur, ok := projects.get(nk); ok {
			projects.set(nk, mergeSettings(v, cur))
		} else {
			projects.set(nk, v)
		}
	}
	root.set("projects", projects.marshal())
	out := pretty(root.marshal())
	if bytes.HasSuffix(raw, []byte("\n")) {
		out = append(out, '\n')
	}
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	backup(path)
	return atomicWrite(path, out, fi.Mode().Perm())
}

func updateHistory(old, new string, rep *Report, dryRun bool) error {
	path := filepath.Join(configDir(), "history.jsonl")
	if !isFile(path) {
		return nil
	}
	if dryRun {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		o := append([]byte(`"project":"`), jsonFragment(old)...)
		rep.History = bytes.Count(data, append(append([]byte{}, o...), '"')) +
			bytes.Count(data, append(append([]byte{}, o...), jsonFragment(sep)...))
		return nil
	}
	n, err := rewriteField(path, "project", old, new)
	rep.History = n
	return err
}

func appendLog(rep *Report) {
	if os.MkdirAll(stateDir(), 0o755) != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(stateDir(), "log.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	b, err := encodeJSON(struct {
		TS int64 `json:"ts"`
		*Report
	}{time.Now().Unix(), rep}, false)
	if err == nil {
		f.Write(append(b, '\n'))
	}
}

// syncAll follows every tracked folder that moved. Without apply it works on
// a copy of the registry and only reports.
func syncAll(reg *Registry, apply bool, excludeSession string) ([]*Report, error) {
	work := reg
	if !apply {
		work = reg.clone()
	}
	var reports []*Report
	tried := map[string]bool{}
	for i := 0; i < 100; i++ {
		var next *action
		for _, a := range plan(work) {
			if a.State == "moved" && !tried[a.Key] {
				next = &a
				break
			}
		}
		if next == nil {
			break
		}
		tried[next.Key] = true
		// A skipped migration (Claude still running in the old path) leaves the
		// entry at the old path, so the next run retries it.
		rep, err := migrate(next.Old, next.New, migrateOpts{DryRun: !apply, ExcludeSession: excludeSession, Reg: work})
		reports = append(reports, rep)
		if err != nil {
			return reports, err
		}
		if rep.Skipped == "" && isDir(next.New) && !isTemp(next.New) {
			work.track(next.New)
		}
	}
	return reports, nil
}
